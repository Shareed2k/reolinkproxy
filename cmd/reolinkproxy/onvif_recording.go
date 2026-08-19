package main

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/shareed2k/reolinkproxy/pkg/baichuan"
)

// ONVIF Recording (ver10/recording) and Search (ver10/search) services backed
// by the Baichuan SD-card clip search. Each camera is modeled as one
// recording ("Recording_<camera>"); individual clips surface as
// tns1:RecordingHistory events via FindEvents. Replay streaming over ONVIF is
// not implemented (the BcMedia replay wire format cannot be validated without
// hardware), so no replay service is advertised.

const (
	onvifSearchSessionTTL = 5 * time.Minute
	onvifSearchMaxRange   = 31 * 24 * time.Hour
)

type onvifSearchSession struct {
	expires time.Time
	files   []recordingSearchHit
	isEvent bool // FindEvents vs FindRecordings
	tokens  []string
}

type recordingSearchHit struct {
	recordingToken string
	file           baichuan.RecordingFile
}

type onvifSearchStore struct {
	mu      sync.Mutex
	counter uint64
	byToken map[string]*onvifSearchSession
}

func (s *onvifSearchStore) put(session *onvifSearchSession) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.byToken == nil {
		s.byToken = make(map[string]*onvifSearchSession)
	}
	now := time.Now()
	for token, existing := range s.byToken {
		if now.After(existing.expires) {
			delete(s.byToken, token)
		}
	}
	s.counter++
	token := "Search_" + strconv.FormatUint(s.counter, 10)
	session.expires = now.Add(onvifSearchSessionTTL)
	s.byToken[token] = session
	return token
}

func (s *onvifSearchStore) get(token string) *onvifSearchSession {
	s.mu.Lock()
	defer s.mu.Unlock()
	session := s.byToken[token]
	if session != nil && time.Now().After(session.expires) {
		delete(s.byToken, token)
		return nil
	}
	return session
}

func (s *onvifSearchStore) end(token string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.byToken[token]; !ok {
		return false
	}
	delete(s.byToken, token)
	return true
}

func onvifRecordingToken(cameraName string) string {
	replacer := strings.NewReplacer(" ", "_", "/", "_", "\\", "_")
	return "Recording_" + replacer.Replace(strings.TrimSpace(cameraName))
}

// recordingCameras returns one representative meta per camera.
func (s *onvifServer) recordingCameras() []*streamMetadata {
	return s.ptzCameras()
}

func (s *onvifServer) metaForRecordingToken(token string) *streamMetadata {
	for _, m := range s.recordingCameras() {
		if onvifRecordingToken(m.cameraName) == token {
			return m
		}
	}
	return nil
}

func (s *onvifServer) recordingServiceURL(r *http.Request) string {
	path := s.cfg.RecordingPath
	if path == "" {
		path = "/onvif/recording_service"
	}
	return buildURL("http", s.authorityForRequest(r, s.cfg.Address), path)
}

func (s *onvifServer) searchServiceURL(r *http.Request) string {
	path := s.cfg.SearchPath
	if path == "" {
		path = "/onvif/search_service"
	}
	return buildURL("http", s.authorityForRequest(r, s.cfg.Address), path)
}

func (s *onvifServer) handleRecording(w http.ResponseWriter, r *http.Request) {
	body, ok := s.readSOAPBody(w, r)
	if !ok {
		return
	}

	switch action := soapAction(r, body, []string{
		"GetServiceCapabilities",
		"GetRecordings",
	}); action {
	case "GetServiceCapabilities":
		writeSOAPResponse(w, `<trc:GetServiceCapabilitiesResponse><trc:Capabilities DynamicRecordings="false" DynamicTracks="false" MaxRate="0" MaxTotalRate="0" MaxRecordings="255" MaxRecordingJobs="0" Options="false" MetadataRecording="false"/></trc:GetServiceCapabilitiesResponse>`)
	case "GetRecordings":
		writeSOAPResponse(w, s.recordingsResponse())
	default:
		log.Printf("onvif recording: unsupported action %q (body: %s)", action, body)
		writeSOAPFault(w, http.StatusBadRequest, "ter:ActionNotSupported", "recording action not supported")
	}
}

func (s *onvifServer) recordingsResponse() string {
	var b strings.Builder
	b.WriteString(`<trc:GetRecordingsResponse>`)
	for _, m := range s.recordingCameras() {
		token := xmlEscape(onvifRecordingToken(m.cameraName))
		fmt.Fprintf(&b,
			`<trc:RecordingItem><trc:RecordingToken>%s</trc:RecordingToken><trc:Configuration><tt:Source><tt:SourceId>%s</tt:SourceId><tt:Name>%s</tt:Name><tt:Location></tt:Location><tt:Description>SD card recordings</tt:Description><tt:Address></tt:Address></tt:Source><tt:Content>Camera SD card</tt:Content><tt:MaximumRetentionTime>P0D</tt:MaximumRetentionTime></trc:Configuration><trc:Tracks></trc:Tracks></trc:RecordingItem>`,
			token, xmlEscape("VideoSource_"+m.cameraName), xmlEscape(m.cameraName),
		)
	}
	b.WriteString(`</trc:GetRecordingsResponse>`)
	return b.String()
}

func (s *onvifServer) handleSearch(w http.ResponseWriter, r *http.Request) {
	body, ok := s.readSOAPBody(w, r)
	if !ok {
		return
	}

	switch action := soapAction(r, body, []string{
		"GetServiceCapabilities",
		"FindRecordings",
		"GetRecordingSearchResults",
		"FindEvents",
		"GetEventSearchResults",
		"EndSearch",
	}); action {
	case "GetServiceCapabilities":
		writeSOAPResponse(w, `<tse:GetServiceCapabilitiesResponse><tse:Capabilities MetadataSearch="false" GeneralStartEvents="false"/></tse:GetServiceCapabilitiesResponse>`)
	case "FindRecordings":
		s.searchFind(w, r.Context(), body, false)
	case "FindEvents":
		s.searchFind(w, r.Context(), body, true)
	case "GetRecordingSearchResults":
		s.searchRecordingResults(w, body)
	case "GetEventSearchResults":
		s.searchEventResults(w, body)
	case "EndSearch":
		token := extractTokenValue(body, "SearchToken")
		if !s.searches.end(token) {
			writeSOAPFault(w, http.StatusBadRequest, "ter:InvalidArgVal", "unknown search token")
			return
		}
		writeSOAPResponse(w, fmt.Sprintf(`<tse:EndSearchResponse><tse:Endpoint>%s</tse:Endpoint></tse:EndSearchResponse>`, time.Now().UTC().Format(time.RFC3339)))
	default:
		log.Printf("onvif search: unsupported action %q (body: %s)", action, body)
		writeSOAPFault(w, http.StatusBadRequest, "ter:ActionNotSupported", "search action not supported")
	}
}

func (s *onvifServer) readSOAPBody(w http.ResponseWriter, r *http.Request) (string, bool) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return "", false
	}
	rawBody, err := io.ReadAll(io.LimitReader(r.Body, 1024*1024)) // 1MB max payload
	if err != nil {
		writeSOAPFault(w, http.StatusBadRequest, "ter:InvalidArgVal", "failed to read request body")
		return "", false
	}
	body := string(rawBody)
	if !s.authenticate(body) {
		writeSOAPFault(w, http.StatusUnauthorized, "ter:NotAuthorized", "The action requires authorization")
		return "", false
	}
	return body, true
}

// searchFind runs the Baichuan clip search synchronously and stores the
// result set behind a search token, as the async ONVIF search flow expects.
func (s *onvifServer) searchFind(w http.ResponseWriter, ctx context.Context, body string, isEvent bool) {
	start, err1 := time.Parse(time.RFC3339Nano, extractTokenValue(body, "StartPoint"))
	if err1 != nil {
		writeSOAPFault(w, http.StatusBadRequest, "ter:InvalidArgVal", "FindRecordings requires an xs:dateTime StartPoint")
		return
	}
	end := time.Now()
	if raw := extractTokenValue(body, "EndPoint"); raw != "" {
		if parsed, err := time.Parse(time.RFC3339Nano, raw); err == nil {
			end = parsed
		}
	}
	if end.Sub(start) > onvifSearchMaxRange {
		end = start.Add(onvifSearchMaxRange)
	}

	// Optional RecordingToken narrows the search to one camera.
	cameras := s.recordingCameras()
	if token := extractTokenValue(body, "RecordingToken"); token != "" {
		meta := s.metaForRecordingToken(token)
		if meta == nil {
			writeSOAPFault(w, http.StatusBadRequest, "ter:InvalidArgVal", "unknown recording token")
			return
		}
		cameras = []*streamMetadata{meta}
	}

	session := &onvifSearchSession{isEvent: isEvent}
	for _, meta := range cameras {
		if meta.device == nil {
			continue
		}
		searchCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		var files []baichuan.RecordingFile
		err := meta.device.WithClient(searchCtx, func(bc *baichuan.Client) error {
			var err error
			files, err = bc.FindRecordings(searchCtx, meta.channel, start.Local(), end.Local())
			return err
		})
		cancel()
		if err != nil {
			log.Printf("onvif search: camera %s clip search failed: %v", meta.cameraName, err)
			continue
		}
		recordingToken := onvifRecordingToken(meta.cameraName)
		session.tokens = append(session.tokens, recordingToken)
		for _, file := range files {
			session.files = append(session.files, recordingSearchHit{recordingToken: recordingToken, file: file})
		}
	}

	token := s.searches.put(session)
	tag := "FindRecordings"
	if isEvent {
		tag = "FindEvents"
	}
	writeSOAPResponse(w, fmt.Sprintf(`<tse:%sResponse><tse:SearchToken>%s</tse:SearchToken></tse:%sResponse>`, tag, token, tag))
}

func (s *onvifServer) searchRecordingResults(w http.ResponseWriter, body string) {
	session := s.searches.get(extractTokenValue(body, "SearchToken"))
	if session == nil {
		writeSOAPFault(w, http.StatusBadRequest, "ter:InvalidArgVal", "unknown search token")
		return
	}

	var b strings.Builder
	b.WriteString(`<tse:GetRecordingSearchResultsResponse><tse:ResultList><tt:SearchState>Completed</tt:SearchState>`)
	seen := make(map[string]bool)
	for _, hit := range session.files {
		if seen[hit.recordingToken] {
			continue
		}
		seen[hit.recordingToken] = true
		earliest, latest := sessionRangeFor(session, hit.recordingToken)
		fmt.Fprintf(&b,
			`<tt:RecordInformation><tt:RecordingToken>%s</tt:RecordingToken><tt:Source><tt:SourceId>%s</tt:SourceId><tt:Name>%s</tt:Name><tt:Location></tt:Location><tt:Description>SD card recordings</tt:Description><tt:Address></tt:Address></tt:Source><tt:EarliestRecording>%s</tt:EarliestRecording><tt:LatestRecording>%s</tt:LatestRecording><tt:Content>Camera SD card</tt:Content><tt:RecordingStatus>Stopped</tt:RecordingStatus></tt:RecordInformation>`,
			xmlEscape(hit.recordingToken), xmlEscape(hit.recordingToken), xmlEscape(hit.recordingToken),
			earliest.UTC().Format(time.RFC3339), latest.UTC().Format(time.RFC3339),
		)
	}
	b.WriteString(`</tse:ResultList></tse:GetRecordingSearchResultsResponse>`)
	writeSOAPResponse(w, b.String())
}

func sessionRangeFor(session *onvifSearchSession, recordingToken string) (time.Time, time.Time) {
	var earliest, latest time.Time
	for _, hit := range session.files {
		if hit.recordingToken != recordingToken {
			continue
		}
		if earliest.IsZero() || hit.file.StartTime.Before(earliest) {
			earliest = hit.file.StartTime
		}
		if latest.IsZero() || hit.file.EndTime.After(latest) {
			latest = hit.file.EndTime
		}
	}
	return earliest, latest
}

// searchEventResults renders each clip as a RecordingHistory start/stop event
// pair, which is how Profile G clients discover individual segments.
func (s *onvifServer) searchEventResults(w http.ResponseWriter, body string) {
	session := s.searches.get(extractTokenValue(body, "SearchToken"))
	if session == nil {
		writeSOAPFault(w, http.StatusBadRequest, "ter:InvalidArgVal", "unknown search token")
		return
	}

	var b strings.Builder
	b.WriteString(`<tse:GetEventSearchResultsResponse><tse:ResultList><tt:SearchState>Completed</tt:SearchState>`)
	for _, hit := range session.files {
		writeRecordingEvent(&b, hit, hit.file.StartTime, true)
		writeRecordingEvent(&b, hit, hit.file.EndTime, false)
	}
	b.WriteString(`</tse:ResultList></tse:GetEventSearchResultsResponse>`)
	writeSOAPResponse(w, b.String())
}

func writeRecordingEvent(b *strings.Builder, hit recordingSearchHit, at time.Time, isRecording bool) {
	fmt.Fprintf(b,
		`<tt:Result><tt:RecordingToken>%s</tt:RecordingToken><tt:TrackToken>VIDEO001</tt:TrackToken><tt:Time>%s</tt:Time><tt:Event><wsnt:Topic Dialect="http://www.onvif.org/ver10/tev/topicExpression/ConcreteSet">tns1:RecordingHistory/Recording</wsnt:Topic><wsnt:Message><tt:Message UtcTime="%s" PropertyOperation="Changed"><tt:Source><tt:SimpleItem Name="RecordingToken" Value="%s"/></tt:Source><tt:Data><tt:SimpleItem Name="IsRecording" Value="%t"/><tt:SimpleItem Name="FileName" Value="%s"/><tt:SimpleItem Name="AlarmType" Value="%s"/></tt:Data></tt:Message></wsnt:Message></tt:Event><tt:StartStateEvent>false</tt:StartStateEvent></tt:Result>`,
		xmlEscape(hit.recordingToken),
		at.UTC().Format(time.RFC3339),
		at.UTC().Format(time.RFC3339),
		xmlEscape(hit.recordingToken),
		isRecording,
		xmlEscape(hit.file.FileName),
		xmlEscape(hit.file.AlarmType),
	)
}
