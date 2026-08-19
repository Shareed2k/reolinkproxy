package main

import (
	"bytes"
	"crypto/sha1" //#nosec G505
	"crypto/subtle"
	"encoding/base64"
	"encoding/xml"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

type onvifConfig struct {
	Address         string
	DevicePath      string
	MediaPath       string
	Media2Path      string
	PTZPath         string
	EventPath       string
	ImagingPath     string
	AnalyticsPath   string
	RecordingPath   string
	SearchPath      string
	AdvertiseHost   string
	RTSPAddress     string
	RTSPPath        string
	DeviceName      string
	Manufacturer    string
	Model           string
	FirmwareVersion string
	SerialNumber    string
	HardwareID      string
	ProfileToken    string
	Username        string
	Password        string
}

type onvifServer struct {
	cfg      onvifConfig
	metas    []*streamMetadata
	mux      *http.ServeMux
	nonces   wsseNonceCache
	events   *onvifEventManager
	ptzMoves ptzMoveTracker
	searches onvifSearchStore
}

func newONVIFHandler(cfg onvifConfig, metas []*streamMetadata, events *onvifEventManager) http.Handler {
	mux := http.NewServeMux()
	server := &onvifServer{cfg: cfg, metas: metas, mux: mux, events: events}
	mux.HandleFunc(cfg.DevicePath, server.handleDevice)
	mux.HandleFunc(cfg.MediaPath, server.handleMedia)
	if cfg.Media2Path != "" {
		mux.HandleFunc(cfg.Media2Path, server.handleMedia2)
	}
	mux.HandleFunc("/api/snapshot/", server.handleSnapshot)
	mux.HandleFunc("/healthz", server.handleHealthz)
	ptzPath := cfg.PTZPath
	if ptzPath == "" {
		ptzPath = "/onvif/ptz_service"
	}
	mux.HandleFunc(ptzPath, server.handlePTZ)
	eventPath := cfg.EventPath
	if eventPath == "" {
		eventPath = "/onvif/event_service"
	}
	mux.HandleFunc(eventPath, server.handleEvents)
	imagingPath := cfg.ImagingPath
	if imagingPath == "" {
		imagingPath = "/onvif/imaging_service"
	}
	mux.HandleFunc(imagingPath, server.handleImaging)
	analyticsPath := cfg.AnalyticsPath
	if analyticsPath == "" {
		analyticsPath = "/onvif/analytics_service"
	}
	mux.HandleFunc(analyticsPath, server.handleAnalytics)
	recordingPath := cfg.RecordingPath
	if recordingPath == "" {
		recordingPath = "/onvif/recording_service"
	}
	mux.HandleFunc(recordingPath, server.handleRecording)
	searchPath := cfg.SearchPath
	if searchPath == "" {
		searchPath = "/onvif/search_service"
	}
	mux.HandleFunc(searchPath, server.handleSearch)
	return mux
}

// handleHealthz reports stream liveness. With ?max_video_age=<duration>, it
// returns 503 when any stream that has RTSP subscribers has not delivered a
// video packet within the threshold — the "connected but frozen" state a
// DESCRIBE-based probe cannot see (issue #24).
func (s *onvifServer) handleHealthz(w http.ResponseWriter, r *http.Request) {
	var maxAge time.Duration
	if raw := r.URL.Query().Get("max_video_age"); raw != "" {
		d, err := time.ParseDuration(raw)
		if err != nil {
			http.Error(w, fmt.Sprintf("invalid max_video_age %q: %v", raw, err), http.StatusBadRequest)
			return
		}
		maxAge = d
	}

	now := time.Now()
	var report strings.Builder
	stale := 0
	for _, meta := range s.metas {
		age, started := meta.videoAge(now)
		hasClients := meta.rtspHandler != nil && meta.rtspHandler.hasClients()
		state := "ok"
		if maxAge > 0 && started && hasClients && age > maxAge {
			stale++
			state = "stale"
		}
		fmt.Fprintf(&report, "camera=%s stream=%s clients=%t video_age=%s state=%s\n",
			meta.cameraName, meta.name, hasClients, age.Round(time.Millisecond), state)
	}

	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	if stale > 0 {
		w.WriteHeader(http.StatusServiceUnavailable)
	}
	_, _ = io.WriteString(w, report.String())
}

// wsseMaxClockSkew bounds how far a UsernameToken Created timestamp may
// deviate from the server clock. Replayed nonces are remembered for twice
// this window, so an expired-Created request can never be replayed either.
const wsseMaxClockSkew = 5 * time.Minute

// wsseNonceCache remembers recently accepted digest nonces to reject replays
// of captured Security headers within the Created freshness window.
type wsseNonceCache struct {
	mu   sync.Mutex
	seen map[string]time.Time // nonce -> expiry
}

// checkAndStore returns false when the nonce was already used and not yet
// expired. Fresh nonces are recorded until now+ttl.
func (c *wsseNonceCache) checkAndStore(nonce string, now time.Time, ttl time.Duration) bool {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.seen == nil {
		c.seen = make(map[string]time.Time)
	}
	for key, expiry := range c.seen {
		if now.After(expiry) {
			delete(c.seen, key)
		}
	}
	if _, used := c.seen[nonce]; used {
		return false
	}
	c.seen[nonce] = now.Add(ttl)
	return true
}

func (s *onvifServer) authenticate(body string) bool {
	if s.cfg.Username == "" && s.cfg.Password == "" {
		return true
	}

	type Password struct {
		Type  string `xml:"Type,attr"`
		Value string `xml:",chardata"`
	}
	type UsernameToken struct {
		Username string   `xml:"Username"`
		Password Password `xml:"Password"`
		Nonce    string   `xml:"Nonce"`
		Created  string   `xml:"Created"`
	}
	type Envelope struct {
		Token UsernameToken `xml:"Header>Security>UsernameToken"`
	}

	var env Envelope
	if err := xml.Unmarshal([]byte(body), &env); err != nil {
		log.Printf("onvif auth: xml unmarshal error: %v", err)
		return false
	}
	token := env.Token

	if subtle.ConstantTimeCompare([]byte(token.Username), []byte(s.cfg.Username)) != 1 {
		log.Printf("onvif auth: unknown username %q", token.Username)
		return false
	}

	// Password/@Type is authoritative per the WS-Security UsernameToken
	// profile: an explicit PasswordText token authenticates as plaintext even
	// when Nonce/Created are present. An untyped token carrying Nonce/Created
	// MUST authenticate via digest — never fall back to plaintext for it,
	// otherwise digest auth could be downgraded to a guessed plaintext value.
	isDigest := strings.Contains(token.Password.Type, "PasswordDigest") ||
		(token.Password.Type == "" && (token.Nonce != "" || token.Created != ""))
	if !isDigest {
		if subtle.ConstantTimeCompare([]byte(token.Password.Value), []byte(s.cfg.Password)) != 1 {
			log.Printf("onvif auth: plaintext password mismatch for user %q", token.Username)
			return false
		}
		return true
	}

	created, err := time.Parse(time.RFC3339Nano, token.Created)
	if err != nil {
		log.Printf("onvif auth: invalid Created timestamp for user %q: %v", token.Username, err)
		return false
	}
	now := time.Now()
	if skew := now.Sub(created); skew > wsseMaxClockSkew || skew < -wsseMaxClockSkew {
		log.Printf("onvif auth: Created timestamp outside ±%v window for user %q", wsseMaxClockSkew, token.Username)
		return false
	}

	nonce, err := base64.StdEncoding.DecodeString(token.Nonce)
	if err != nil {
		log.Printf("onvif auth: failed to decode nonce: %v", err)
		return false
	}

	h := sha1.New() //#nosec G401 -- SHA1 is mandated by the WS-Security UsernameToken profile
	h.Write(nonce)
	h.Write([]byte(token.Created))
	h.Write([]byte(s.cfg.Password))
	expected := base64.StdEncoding.EncodeToString(h.Sum(nil))

	if subtle.ConstantTimeCompare([]byte(expected), []byte(token.Password.Value)) != 1 {
		log.Printf("onvif auth: digest mismatch for user %q", token.Username)
		return false
	}

	if !s.nonces.checkAndStore(token.Nonce, now, 2*wsseMaxClockSkew) {
		log.Printf("onvif auth: replayed nonce rejected for user %q", token.Username)
		return false
	}

	return true
}

func (s *onvifServer) handleDevice(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, 1024*1024)) // 1MB max payload
	if err != nil {
		writeSOAPFault(w, http.StatusBadRequest, "ter:InvalidArgVal", "failed to read request body")
		return
	}

	action := soapAction(r, string(body), []string{
		"GetCapabilities",
		"GetDeviceInformation",
		"GetScopes",
		"GetServices",
		"GetServiceCapabilities",
		"GetSystemDateAndTime",
		"GetNetworkInterfaces",
		"GetEndpointReference",
	})

	if action != "GetSystemDateAndTime" && !s.authenticate(string(body)) {
		writeSOAPFault(w, http.StatusUnauthorized, "ter:NotAuthorized", "The action requires authorization")
		return
	}

	switch action {
	case "GetCapabilities":
		writeSOAPResponse(w, s.deviceCapabilitiesResponse(r))
	case "GetDeviceInformation":
		writeSOAPResponse(w, s.deviceInformationResponse())
	case "GetScopes":
		writeSOAPResponse(w, s.deviceScopesResponse())
	case "GetServices":
		writeSOAPResponse(w, s.deviceServicesResponse(r))
	case "GetServiceCapabilities":
		writeSOAPResponse(w, deviceServiceCapabilitiesResponse())
	case "GetSystemDateAndTime":
		writeSOAPResponse(w, s.deviceSystemDateAndTimeResponse())
	case "GetNetworkInterfaces":
		writeSOAPResponse(w, s.deviceNetworkInterfacesResponse())
	case "GetEndpointReference":
		writeSOAPResponse(w, s.deviceEndpointReferenceResponse())
	default:
		log.Printf("onvif device: unsupported action %q (body: %s)", action, body)
		writeSOAPFault(w, http.StatusBadRequest, "ter:ActionNotSupported", "device action not supported")
	}
}

func (s *onvifServer) handleMedia(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, 1024*1024)) // 1MB max payload
	if err != nil {
		writeSOAPFault(w, http.StatusBadRequest, "ter:InvalidArgVal", "failed to read request body")
		return
	}

	if !s.authenticate(string(body)) {
		writeSOAPFault(w, http.StatusUnauthorized, "ter:NotAuthorized", "The action requires authorization")
		return
	}

	switch action := soapAction(r, string(body), []string{
		"GetAudioEncoderConfigurations",
		"GetAudioDecoderConfigurations",
		"GetAudioSources",
		"GetAudioOutputs",
		"GetAudioOutputConfigurations",
		"AddAudioOutputConfiguration",
		"AddAudioDecoderConfiguration",
		"SetSynchronizationPoint",
		"GetProfile",
		"GetProfiles",
		"GetServiceCapabilities",
		"GetStreamUri",
		"GetSnapshotUri",
		"GetVideoEncoderConfigurations",
		"GetVideoSources",
	}); action {
	case "GetProfiles":
		writeSOAPResponse(w, s.mediaProfilesResponse())
	case "GetProfile":
		xmlBody, ok := s.mediaProfileResponse(string(body))
		writeProfileScopedResponse(w, xmlBody, ok)
	case "GetStreamUri":
		xmlBody, ok := s.mediaStreamURIResponse(r, string(body))
		writeProfileScopedResponse(w, xmlBody, ok)
	case "GetSnapshotUri":
		xmlBody, ok := s.mediaSnapshotURIResponse(r, string(body))
		writeProfileScopedResponse(w, xmlBody, ok)
	case "GetServiceCapabilities":
		writeSOAPResponse(w, `<trt:GetServiceCapabilitiesResponse><trt:Capabilities SnapshotUri="true" Rotation="false" VideoSourceMode="false" OSD="false" TemporaryOSDText="false" EXICompression="false"/></trt:GetServiceCapabilitiesResponse>`)
	case "GetVideoSources":
		writeSOAPResponse(w, s.mediaVideoSourcesResponse(string(body)))
	case "GetVideoEncoderConfigurations":
		writeSOAPResponse(w, s.mediaVideoEncoderConfigurationsResponse(string(body)))
	case "GetAudioSources":
		writeSOAPResponse(w, s.mediaAudioSourcesResponse(string(body)))
	case "GetAudioOutputs":
		writeSOAPResponse(w, s.mediaAudioOutputsResponse(string(body)))
	case "GetAudioOutputConfigurations":
		writeSOAPResponse(w, s.mediaAudioOutputConfigurationsResponse(string(body)))
	case "GetAudioEncoderConfigurations":
		writeSOAPResponse(w, s.mediaAudioEncoderConfigurationsResponse(string(body)))
	case "GetAudioDecoderConfigurations":
		writeSOAPResponse(w, s.mediaAudioDecoderConfigurationsResponse(string(body)))
	case "AddAudioOutputConfiguration", "AddAudioDecoderConfiguration", "SetSynchronizationPoint":
		// These configuration actions are effectively no-ops since the profiles are statically generated
		// and already include the audio output/decoder tokens. SetSynchronizationPoint (I-Frame request)
		// is ignored since the camera dictates keyframes or we'd need to send a Baichuan command.
		writeSOAPResponse(w, fmt.Sprintf(`<trt:%sResponse></trt:%sResponse>`, action, action))
	default:
		log.Printf("onvif media: unsupported action %q (body: %s)", action, body)
		writeSOAPFault(w, http.StatusBadRequest, "ter:ActionNotSupported", "media action not supported")
	}
}

func (s *onvifServer) handleMedia2(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, 1024*1024))
	if err != nil {
		writeSOAPFault(w, http.StatusBadRequest, "ter:InvalidArgVal", "failed to read request body")
		return
	}

	if !s.authenticate(string(body)) {
		writeSOAPFault(w, http.StatusUnauthorized, "ter:NotAuthorized", "The action requires authorization")
		return
	}

	switch action := soapAction(r, string(body), []string{
		"GetProfiles",
		"GetStreamUri",
		"GetSnapshotUri",
		"GetServiceCapabilities",
		"GetVideoEncoderConfigurations",
		"GetAudioEncoderConfigurations",
		"GetAudioDecoderConfigurations",
		"GetAudioOutputs",
		"GetAudioOutputConfigurations",
		"GetAudioSources",
		"AddAudioOutputConfiguration",
		"AddAudioDecoderConfiguration",
		"SetSynchronizationPoint",
	}); action {
	case "GetProfiles":
		writeSOAPResponse(w, s.media2ProfilesResponse(string(body)))
	case "GetStreamUri":
		xmlBody, ok := s.media2StreamURIResponse(r, string(body))
		writeProfileScopedResponse(w, xmlBody, ok)
	case "GetSnapshotUri":
		xmlBody, ok := s.media2SnapshotURIResponse(r, string(body))
		writeProfileScopedResponse(w, xmlBody, ok)
	case "GetServiceCapabilities":
		writeSOAPResponse(w, `<tr2:GetServiceCapabilitiesResponse><tr2:Capabilities><tr2:ProfileCapabilities MaximumNumberOfProfiles="`+fmt.Sprint(len(s.metas))+`"/><tr2:StreamingCapabilities RTSPWebSocketUri="false"/></tr2:Capabilities></tr2:GetServiceCapabilitiesResponse>`)
	case "GetVideoEncoderConfigurations":
		writeSOAPResponse(w, s.media2VideoEncoderConfigurationsResponse(string(body)))
	case "GetAudioEncoderConfigurations":
		writeSOAPResponse(w, s.media2AudioEncoderConfigurationsResponse(string(body)))
	case "GetAudioDecoderConfigurations":
		writeSOAPResponse(w, s.media2AudioDecoderConfigurationsResponse(string(body)))
	case "GetAudioOutputs":
		writeSOAPResponse(w, s.media2AudioOutputsResponse(string(body)))
	case "GetAudioOutputConfigurations":
		writeSOAPResponse(w, s.media2AudioOutputConfigurationsResponse(string(body)))
	case "GetAudioSources":
		writeSOAPResponse(w, s.media2AudioSourcesResponse(string(body)))
	case "AddAudioOutputConfiguration", "AddAudioDecoderConfiguration", "SetSynchronizationPoint":
		// Accept the configuration addition / I-Frame request
		writeSOAPResponse(w, fmt.Sprintf(`<tr2:%sResponse></tr2:%sResponse>`, action, action))
	default:
		log.Printf("onvif media2: unsupported action %q (body: %s)", action, body)
		writeSOAPFault(w, http.StatusBadRequest, "ter:ActionNotSupported", "media2 action not supported")
	}
}

func (s *onvifServer) deviceInformationResponse() string {
	return fmt.Sprintf(
		`<tds:GetDeviceInformationResponse><tds:Manufacturer>%s</tds:Manufacturer><tds:Model>%s</tds:Model><tds:FirmwareVersion>%s</tds:FirmwareVersion><tds:SerialNumber>%s</tds:SerialNumber><tds:HardwareId>%s</tds:HardwareId></tds:GetDeviceInformationResponse>`,
		xmlEscape(s.cfg.Manufacturer),
		xmlEscape(s.cfg.Model),
		xmlEscape(s.cfg.FirmwareVersion),
		xmlEscape(s.cfg.SerialNumber),
		xmlEscape(s.cfg.HardwareID),
	)
}

func (s *onvifServer) deviceServicesResponse(r *http.Request) string {
	deviceXAddr := xmlEscape(s.deviceServiceURL(r))
	mediaXAddr := xmlEscape(s.mediaServiceURL(r))
	media2XAddr := xmlEscape(s.media2ServiceURL(r))
	ptzXAddr := xmlEscape(s.ptzServiceURL(r))

	return fmt.Sprintf(
		`<tds:GetServicesResponse>`+
			`<tds:Service><tds:Namespace>http://www.onvif.org/ver10/device/wsdl</tds:Namespace><tds:XAddr>%s</tds:XAddr><tds:Version><tt:Major>1</tt:Major><tt:Minor>0</tt:Minor></tds:Version></tds:Service>`+
			`<tds:Service><tds:Namespace>http://www.onvif.org/ver10/media/wsdl</tds:Namespace><tds:XAddr>%s</tds:XAddr><tds:Version><tt:Major>1</tt:Major><tt:Minor>0</tt:Minor></tds:Version></tds:Service>`+
			`<tds:Service><tds:Namespace>http://www.onvif.org/ver20/media/wsdl</tds:Namespace><tds:XAddr>%s</tds:XAddr><tds:Version><tt:Major>2</tt:Major><tt:Minor>0</tt:Minor></tds:Version></tds:Service>`+
			`<tds:Service><tds:Namespace>http://www.onvif.org/ver20/ptz/wsdl</tds:Namespace><tds:XAddr>%s</tds:XAddr><tds:Version><tt:Major>2</tt:Major><tt:Minor>0</tt:Minor></tds:Version></tds:Service>`+
			`<tds:Service><tds:Namespace>http://www.onvif.org/ver10/events/wsdl</tds:Namespace><tds:XAddr>%s</tds:XAddr><tds:Version><tt:Major>1</tt:Major><tt:Minor>0</tt:Minor></tds:Version></tds:Service>`+
			`<tds:Service><tds:Namespace>http://www.onvif.org/ver20/imaging/wsdl</tds:Namespace><tds:XAddr>%s</tds:XAddr><tds:Version><tt:Major>2</tt:Major><tt:Minor>0</tt:Minor></tds:Version></tds:Service>`+
			`<tds:Service><tds:Namespace>http://www.onvif.org/ver20/analytics/wsdl</tds:Namespace><tds:XAddr>%s</tds:XAddr><tds:Version><tt:Major>2</tt:Major><tt:Minor>0</tt:Minor></tds:Version></tds:Service>`+
			`<tds:Service><tds:Namespace>http://www.onvif.org/ver10/recording/wsdl</tds:Namespace><tds:XAddr>%s</tds:XAddr><tds:Version><tt:Major>1</tt:Major><tt:Minor>0</tt:Minor></tds:Version></tds:Service>`+
			`<tds:Service><tds:Namespace>http://www.onvif.org/ver10/search/wsdl</tds:Namespace><tds:XAddr>%s</tds:XAddr><tds:Version><tt:Major>1</tt:Major><tt:Minor>0</tt:Minor></tds:Version></tds:Service>`+
			`</tds:GetServicesResponse>`,
		deviceXAddr,
		mediaXAddr,
		media2XAddr,
		ptzXAddr,
		xmlEscape(s.eventServiceURL(r)),
		xmlEscape(s.imagingServiceURL(r)),
		xmlEscape(s.analyticsServiceURL(r)),
		xmlEscape(s.recordingServiceURL(r)),
		xmlEscape(s.searchServiceURL(r)),
	)
}

func (s *onvifServer) deviceCapabilitiesResponse(r *http.Request) string {
	deviceXAddr := xmlEscape(s.deviceServiceURL(r))
	mediaXAddr := xmlEscape(s.mediaServiceURL(r))

	return fmt.Sprintf(
		`<tds:GetCapabilitiesResponse><tds:Capabilities>`+
			`<tt:Device>`+
			`<tt:XAddr>%s</tt:XAddr>`+
			`<tt:Network><tt:IPFilter>false</tt:IPFilter><tt:ZeroConfiguration>false</tt:ZeroConfiguration><tt:IPVersion6>false</tt:IPVersion6><tt:DynDNS>false</tt:DynDNS></tt:Network>`+
			`<tt:System><tt:DiscoveryResolve>false</tt:DiscoveryResolve><tt:DiscoveryBye>false</tt:DiscoveryBye><tt:RemoteDiscovery>false</tt:RemoteDiscovery><tt:SystemBackup>false</tt:SystemBackup><tt:SystemLogging>false</tt:SystemLogging><tt:FirmwareUpgrade>false</tt:FirmwareUpgrade></tt:System>`+
			`<tt:IO><tt:InputConnectors>0</tt:InputConnectors><tt:RelayOutputs>0</tt:RelayOutputs></tt:IO>`+
			`<tt:Security><tt:TLS1.1>false</tt:TLS1.1><tt:TLS1.2>false</tt:TLS1.2><tt:OnboardKeyGeneration>false</tt:OnboardKeyGeneration><tt:AccessPolicyConfig>false</tt:AccessPolicyConfig><tt:X.509Token>false</tt:X.509Token><tt:SAMLToken>false</tt:SAMLToken><tt:KerberosToken>false</tt:KerberosToken><tt:RELToken>false</tt:RELToken></tt:Security>`+
			`</tt:Device>`+
			`<tt:Media>`+
			`<tt:XAddr>%s</tt:XAddr>`+
			`<tt:StreamingCapabilities><tt:RTPMulticast>false</tt:RTPMulticast><tt:RTP_TCP>true</tt:RTP_TCP><tt:RTP_RTSP_TCP>true</tt:RTP_RTSP_TCP></tt:StreamingCapabilities>`+
			`<tt:ProfileCapabilities><tt:MaximumNumberOfProfiles>%d</tt:MaximumNumberOfProfiles></tt:ProfileCapabilities>`+
			`</tt:Media>`+
			`<tt:Events>`+
			`<tt:XAddr>%s</tt:XAddr>`+
			`<tt:WSSubscriptionPolicySupport>false</tt:WSSubscriptionPolicySupport>`+
			`<tt:WSPullPointSupport>true</tt:WSPullPointSupport>`+
			`<tt:WSPausableSubscriptionManagerInterfaceSupport>false</tt:WSPausableSubscriptionManagerInterfaceSupport>`+
			`</tt:Events>`+
			`<tt:Imaging><tt:XAddr>%s</tt:XAddr></tt:Imaging>`+
			`<tt:PTZ><tt:XAddr>%s</tt:XAddr></tt:PTZ>`+
			`<tt:Extension><tt:Search><tt:XAddr>%s</tt:XAddr><tt:MetadataSearch>false</tt:MetadataSearch></tt:Search></tt:Extension>`+
			`</tds:Capabilities></tds:GetCapabilitiesResponse>`,
		deviceXAddr,
		mediaXAddr,
		len(s.metas),
		xmlEscape(s.eventServiceURL(r)),
		xmlEscape(s.imagingServiceURL(r)),
		xmlEscape(s.ptzServiceURL(r)),
		xmlEscape(s.searchServiceURL(r)),
	)
}

func (s *onvifServer) deviceScopesResponse() string {
	var b strings.Builder
	b.WriteString(`<tds:GetScopesResponse>`)
	for _, scope := range onvifScopes(s.cfg) {
		fmt.Fprintf(&b, `<tds:Scopes><tt:ScopeDef>Fixed</tt:ScopeDef><tt:ScopeItem>%s</tt:ScopeItem></tds:Scopes>`, xmlEscape(scope))
	}
	b.WriteString(`</tds:GetScopesResponse>`)
	return b.String()
}

func (s *onvifServer) deviceSystemDateAndTimeResponse() string {
	now := time.Now().UTC()
	return fmt.Sprintf(
		`<tds:GetSystemDateAndTimeResponse><tds:SystemDateAndTime><tt:DateTimeType>NTP</tt:DateTimeType><tt:DaylightSavings>false</tt:DaylightSavings><tt:TimeZone><tt:TZ>UTC</tt:TZ></tt:TimeZone><tt:UTCDateTime><tt:Time><tt:Hour>%d</tt:Hour><tt:Minute>%d</tt:Minute><tt:Second>%d</tt:Second></tt:Time><tt:Date><tt:Year>%d</tt:Year><tt:Month>%d</tt:Month><tt:Day>%d</tt:Day></tt:Date></tt:UTCDateTime><tt:LocalDateTime><tt:Time><tt:Hour>%d</tt:Hour><tt:Minute>%d</tt:Minute><tt:Second>%d</tt:Second></tt:Time><tt:Date><tt:Year>%d</tt:Year><tt:Month>%d</tt:Month><tt:Day>%d</tt:Day></tt:Date></tt:LocalDateTime></tds:SystemDateAndTime></tds:GetSystemDateAndTimeResponse>`,
		now.Hour(), now.Minute(), now.Second(),
		now.Year(), int(now.Month()), now.Day(),
		now.Hour(), now.Minute(), now.Second(),
		now.Year(), int(now.Month()), now.Day(),
	)
}

func (s *onvifServer) deviceNetworkInterfacesResponse() string {
	host := "127.0.0.1"
	if s.cfg.AdvertiseHost != "" && s.cfg.AdvertiseHost != "0.0.0.0" && s.cfg.AdvertiseHost != "::" {
		host = s.cfg.AdvertiseHost
	} else if outbound := getOutboundIP(); outbound != "" {
		host = outbound
	} else if s.cfg.Address != "" {
		if parsedHost, _, err := net.SplitHostPort(s.cfg.Address); err == nil && parsedHost != "" && parsedHost != "0.0.0.0" && parsedHost != "::" {
			host = parsedHost
		}
	}

	return fmt.Sprintf(`<tds:GetNetworkInterfacesResponse><tds:NetworkInterfaces token="eth0"><tt:Enabled>true</tt:Enabled><tt:Info><tt:Name>eth0</tt:Name><tt:HwAddress>00:00:00:00:00:00</tt:HwAddress><tt:MTU>1500</tt:MTU></tt:Info><tt:IPv4><tt:Enabled>true</tt:Enabled><tt:Config><tt:Manual><tt:Address>%s</tt:Address><tt:PrefixLength>24</tt:PrefixLength></tt:Manual><tt:DHCP>false</tt:DHCP></tt:Config></tt:IPv4></tds:NetworkInterfaces></tds:GetNetworkInterfacesResponse>`, xmlEscape(host))
}

func (s *onvifServer) deviceEndpointReferenceResponse() string {
	return fmt.Sprintf(
		`<tds:GetEndpointReferenceResponse><tds:GUID>urn:uuid:%s</tds:GUID></tds:GetEndpointReferenceResponse>`,
		deviceUUID(s.cfg),
	)
}

// deviceServiceCapabilitiesResponse serves the mandatory Device
// GetServiceCapabilities action (Device Management spec).
func deviceServiceCapabilitiesResponse() string {
	return `<tds:GetServiceCapabilitiesResponse><tds:Capabilities>` +
		`<tds:Network IPFilter="false" ZeroConfiguration="false" IPVersion6="false" DynDNS="false" Dot11Configuration="false" HostnameFromDHCP="false" NTP="0" DHCPv6="false"/>` +
		`<tds:Security TLS1.0="false" TLS1.1="false" TLS1.2="false" OnboardKeyGeneration="false" AccessPolicyConfig="false" DefaultAccessPolicy="false" Dot1X="false" RemoteUserHandling="false" X.509Token="false" SAMLToken="false" KerberosToken="false" UsernameToken="true" HttpDigest="false" RELToken="false"/>` +
		`<tds:System DiscoveryResolve="false" DiscoveryBye="false" RemoteDiscovery="false" SystemBackup="false" SystemLogging="false" FirmwareUpgrade="false" HttpFirmwareUpgrade="false" HttpSystemBackup="false" HttpSystemLogging="false" HttpSupportInformation="false" StorageConfiguration="false"/>` +
		`</tds:Capabilities></tds:GetServiceCapabilitiesResponse>`
}

func (s *onvifServer) getMeta(token string) *streamMetadata {
	for _, m := range s.metas {
		if m.token == token || m.name == token {
			return m
		}
	}
	if len(s.metas) > 0 {
		return s.metas[0]
	}
	return nil
}

func (s *onvifServer) media2ProfilesResponse(body string) string {
	token := s.extractToken(body, "Token")

	var b strings.Builder
	b.WriteString(`<tr2:GetProfilesResponse>`)
	for _, m := range s.metas {
		if token != "" && token != m.token && token != m.name {
			continue
		}
		b.WriteString(s.profile2XML("tr2:Profiles", m.token, m))
	}
	b.WriteString(`</tr2:GetProfilesResponse>`)
	return b.String()
}

func (s *onvifServer) profile2XML(tag string, token string, m *streamMetadata) string {
	var snap streamMetadataSnapshot
	var cameraName string
	if m != nil {
		snap = m.snapshot().normalized()
		cameraName = m.cameraName
	} else {
		snap = streamMetadataSnapshot{}.normalized()
		cameraName = "0"
	}

	videoSourceToken := xmlEscape("VideoSource_" + cameraName)
	audioSourceToken := xmlEscape("AudioSource_" + cameraName)
	profileToken := xmlEscape(token)
	name := xmlEscape(s.cfg.DeviceName + "_" + token)

	var b strings.Builder
	fmt.Fprintf(&b, `<%s token="%s" fixed="true">`, tag, profileToken)
	fmt.Fprintf(&b, `<tr2:Name>%s</tr2:Name>`, name)
	fmt.Fprintf(&b, `<tr2:Configurations>`)

	// VideoSource
	fmt.Fprintf(&b, `<tr2:VideoSource token="VideoSourceConfig_%s"><tt:Name>VideoSourceConfig_%s</tt:Name><tt:UseCount>1</tt:UseCount><tt:SourceToken>%s</tt:SourceToken><tt:Bounds x="0" y="0" width="%d" height="%d"/></tr2:VideoSource>`, profileToken, profileToken, videoSourceToken, snap.Width, snap.Height)

	// AudioSource
	if snap.AudioCodec != "" {
		fmt.Fprintf(&b, `<tr2:AudioSource token="AudioSourceConfig_%s"><tt:Name>AudioSourceConfig_%s</tt:Name><tt:UseCount>1</tt:UseCount><tt:SourceToken>%s</tt:SourceToken></tr2:AudioSource>`, profileToken, profileToken, audioSourceToken)
	}

	// VideoEncoder
	b.WriteString(s.videoEncoder2ConfigXML("tr2:VideoEncoder", token, snap))

	// AudioEncoder
	if snap.AudioCodec != "" {
		b.WriteString(s.audioEncoder2ConfigXML("tr2:AudioEncoder", token, snap))
	}

	// Analytics precedes PTZ in the media2 ConfigurationSet sequence
	b.WriteString(analyticsConfigurationXML("tr2:Analytics", cameraName))

	// PTZ (clients like Frigate/HA require the profile to carry a PTZ configuration)
	b.WriteString(ptzConfigurationXML("tr2:PTZ", cameraName))

	// Per the media2 ConfigurationSet sequence, AudioOutput/AudioDecoder come
	// after PTZ (required for 2-way audio ONVIF capabilities in tr2).
	audioOutputToken := xmlEscape("AudioOutput_" + cameraName)
	fmt.Fprintf(&b, `<tr2:AudioOutput token="AudioOutputConfig_%s"><tt:Name>AudioOutputConfig_%s</tt:Name><tt:UseCount>1</tt:UseCount><tt:OutputToken>%s</tt:OutputToken></tr2:AudioOutput>`, profileToken, profileToken, audioOutputToken)
	b.WriteString(s.audioDecoder2ConfigXML("tr2:AudioDecoder", cameraName, snap))

	fmt.Fprintf(&b, `</tr2:Configurations>`)
	fmt.Fprintf(&b, `</%s>`, tag)
	return b.String()
}

func (s *onvifServer) media2StreamURIResponse(r *http.Request, body string) (string, bool) {
	m, ok := s.resolveMeta(body)
	if !ok {
		return "", false
	}
	path := s.cfg.RTSPPath
	if m != nil && m.path != "" {
		path = m.path
	}

	return fmt.Sprintf(
		`<tr2:GetStreamUriResponse><tr2:Uri>%s</tr2:Uri></tr2:GetStreamUriResponse>`,
		xmlEscape(buildURL("rtsp", s.authorityForRequest(r, s.cfg.RTSPAddress), path)),
	), true
}

func (s *onvifServer) mediaSnapshotURIResponse(r *http.Request, body string) (string, bool) {
	m, ok := s.resolveMeta(body)
	if !ok {
		return "", false
	}

	// If we have metadata, we use the actual RTSP path since that's where the stream is mounted
	path := "camera/main"
	if m != nil && m.path != "" {
		path = m.path
	}

	return fmt.Sprintf(
		`<trt:GetSnapshotUriResponse><trt:MediaUri><tt:Uri>%s</tt:Uri><tt:InvalidAfterConnect>false</tt:InvalidAfterConnect><tt:InvalidAfterReboot>false</tt:InvalidAfterReboot><tt:Timeout>PT0S</tt:Timeout></trt:MediaUri></trt:GetSnapshotUriResponse>`,
		xmlEscape(buildURL("http", s.authorityForRequest(r, s.cfg.Address), fmt.Sprintf("/api/snapshot/%s", path))),
	), true
}

func (s *onvifServer) media2SnapshotURIResponse(r *http.Request, body string) (string, bool) {
	m, ok := s.resolveMeta(body)
	if !ok {
		return "", false
	}

	path := "camera/main"
	if m != nil && m.path != "" {
		path = m.path
	}

	return fmt.Sprintf(
		`<tr2:GetSnapshotUriResponse><tr2:Uri>%s</tr2:Uri></tr2:GetSnapshotUriResponse>`,
		xmlEscape(buildURL("http", s.authorityForRequest(r, s.cfg.Address), fmt.Sprintf("/api/snapshot/%s", path))),
	), true
}

func (s *onvifServer) media2VideoEncoderConfigurationsResponse(body string) string {
	token := s.extractToken(body, "ProfileToken")
	var b strings.Builder
	b.WriteString(`<tr2:GetVideoEncoderConfigurationsResponse>`)
	for _, m := range s.metas {
		if token != "" && token != m.token && token != m.name {
			continue
		}
		tok := m.token
		if tok == "" {
			tok = m.name
		}
		b.WriteString(s.videoEncoder2ConfigXML("tr2:Configurations", tok, m.snapshot().normalized()))
	}
	b.WriteString(`</tr2:GetVideoEncoderConfigurationsResponse>`)
	return b.String()
}

func (s *onvifServer) media2AudioEncoderConfigurationsResponse(body string) string {
	token := s.extractToken(body, "ProfileToken")
	var b strings.Builder
	b.WriteString(`<tr2:GetAudioEncoderConfigurationsResponse>`)
	for _, m := range s.metas {
		if token != "" && token != m.token && token != m.name {
			continue
		}
		snap := m.snapshot().normalized()
		if snap.AudioCodec != "" {
			tok := m.token
			if tok == "" {
				tok = m.name
			}
			b.WriteString(s.audioEncoder2ConfigXML("tr2:Configurations", tok, snap))
		}
	}
	b.WriteString(`</tr2:GetAudioEncoderConfigurationsResponse>`)
	return b.String()
}

func (s *onvifServer) media2AudioDecoderConfigurationsResponse(body string) string {
	token := s.extractToken(body, "ProfileToken")
	var b strings.Builder
	b.WriteString(`<tr2:GetAudioDecoderConfigurationsResponse>`)

	added := make(map[string]bool)
	for _, m := range s.metas {
		if token != "" && token != m.token && token != m.name {
			continue
		}
		if added[m.cameraName] {
			continue
		}
		added[m.cameraName] = true

		b.WriteString(s.audioDecoder2ConfigXML("tr2:Configurations", m.cameraName, m.snapshot().normalized()))
	}
	b.WriteString(`</tr2:GetAudioDecoderConfigurationsResponse>`)
	return b.String()
}

func (s *onvifServer) media2AudioOutputsResponse(_ string) string {
	var b strings.Builder
	b.WriteString(`<tr2:GetAudioOutputsResponse>`)

	added := make(map[string]bool)

	for _, m := range s.metas {
		if added[m.cameraName] {
			continue
		}
		added[m.cameraName] = true

		fmt.Fprintf(
			&b,
			`<tr2:AudioOutputs token="AudioOutput_%s"></tr2:AudioOutputs>`,
			xmlEscape(m.cameraName),
		)
	}

	b.WriteString(`</tr2:GetAudioOutputsResponse>`)
	return b.String()
}

func (s *onvifServer) media2AudioOutputConfigurationsResponse(_ string) string {
	var b strings.Builder
	b.WriteString(`<tr2:GetAudioOutputConfigurationsResponse>`)

	added := make(map[string]bool)

	for _, m := range s.metas {
		if added[m.cameraName] {
			continue
		}
		added[m.cameraName] = true

		token := "AudioOutputConfig_" + xmlEscape(m.cameraName)
		fmt.Fprintf(
			&b,
			`<tr2:Configurations token="%s"><tt:Name>%s</tt:Name><tt:UseCount>1</tt:UseCount><tt:OutputToken>AudioOutput_%s</tt:OutputToken></tr2:Configurations>`,
			token, token, xmlEscape(m.cameraName),
		)
	}

	b.WriteString(`</tr2:GetAudioOutputConfigurationsResponse>`)
	return b.String()
}

func (s *onvifServer) media2AudioSourcesResponse(_ string) string {
	var b strings.Builder
	b.WriteString(`<tr2:GetAudioSourcesResponse>`)

	added := make(map[string]bool)
	hasAudio := false

	for _, m := range s.metas {
		if added[m.cameraName] {
			continue
		}
		added[m.cameraName] = true

		snap := m.snapshot().normalized()
		if snap.AudioCodec != "" {
			hasAudio = true
			fmt.Fprintf(
				&b,
				`<tr2:AudioSources token="AudioSource_%s"><tt:Channels>%d</tt:Channels></tr2:AudioSources>`,
				xmlEscape(m.cameraName),
				snap.AudioChannels,
			)
		}
	}

	if !hasAudio && len(s.metas) == 0 {
		return `<tr2:GetAudioSourcesResponse/>`
	}

	b.WriteString(`</tr2:GetAudioSourcesResponse>`)
	return b.String()
}

// videoEncoder2ConfigXML emits a ver20 tt:VideoEncoder2Configuration.
// Sequence per media2 schema: Encoding, Resolution, RateControl?, Multicast?,
// Quality (last); GovLength and Profile are attributes, and RateControl2 has
// no EncodingInterval.
func (s *onvifServer) videoEncoder2ConfigXML(tag string, token string, snap streamMetadataSnapshot) string {
	encoding := snap.VideoCodec
	if encoding == "" {
		encoding = "H265"
	}

	return fmt.Sprintf(
		`<%s token="VideoEncoder_%s" GovLength="50" Profile="Main"><tt:Name>VideoEncoder_%s</tt:Name><tt:UseCount>1</tt:UseCount><tt:Encoding>%s</tt:Encoding><tt:Resolution><tt:Width>%d</tt:Width><tt:Height>%d</tt:Height></tt:Resolution><tt:RateControl><tt:FrameRateLimit>%d</tt:FrameRateLimit><tt:BitrateLimit>4096</tt:BitrateLimit></tt:RateControl><tt:Quality>5</tt:Quality></%s>`,
		tag,
		xmlEscape(token),
		xmlEscape(token),
		encoding,
		snap.Width,
		snap.Height,
		snap.FPS,
		tag,
	)
}

func (s *onvifServer) audioEncoder2ConfigXML(tag string, token string, snap streamMetadataSnapshot) string {
	if snap.AudioSampleRate == 0 {
		snap.AudioSampleRate = 16000
	}
	if snap.AudioChannels == 0 {
		snap.AudioChannels = 1
	}

	encoding := snap.AudioCodec
	if encoding == "" {
		encoding = "AAC"
	}
	if encoding == "PCMA" || encoding == "PCMU" {
		encoding = "G711"
	}

	// ver20 audio Encoding uses IANA MIME subtype names, not the ver10 enum.
	switch encoding {
	case "AAC":
		encoding = "MP4A-LATM"
	case "G711":
		encoding = "PCMA"
	}

	return fmt.Sprintf(
		`<%s token="AudioEncoder_%s"><tt:Name>AudioEncoder_%s</tt:Name><tt:UseCount>1</tt:UseCount><tt:Encoding>%s</tt:Encoding><tt:Bitrate>128</tt:Bitrate><tt:SampleRate>%d</tt:SampleRate></%s>`,
		tag,
		xmlEscape(token),
		xmlEscape(token),
		encoding,
		snap.AudioSampleRate,
		tag,
	)
}

func (s *onvifServer) audioDecoder2ConfigXML(tag string, token string, _ streamMetadataSnapshot) string {
	return fmt.Sprintf(
		`<%s token="AudioDecoder_%s">`+
			`<tt:Name>AudioDecoder_%s</tt:Name>`+
			`<tt:UseCount>1</tt:UseCount>`+
			`</%s>`,
		tag,
		xmlEscape(token),
		xmlEscape(token),
		tag,
	)
}

func (s *onvifServer) mediaProfilesResponse() string {
	var b strings.Builder
	b.WriteString(`<trt:GetProfilesResponse>`)
	for _, m := range s.metas {
		b.WriteString(s.profileXML("trt:Profiles", m.token, m))
	}
	b.WriteString(`</trt:GetProfilesResponse>`)
	return b.String()
}

func (s *onvifServer) mediaProfileResponse(body string) (string, bool) {
	m, ok := s.resolveMeta(body)
	if !ok || m == nil {
		return "", false
	}
	return `<trt:GetProfileResponse>` + s.profileXML("trt:Profile", m.token, m) + `</trt:GetProfileResponse>`, true
}

// extractTokenValue is a namespace-agnostic XML element extraction with no
// fallback: it returns "" when the element is absent.
func extractTokenValue(body, element string) string {
	start := -1
	if i := strings.Index(body, ":"+element+">"); i != -1 {
		start = i + 1 // past ':'
	} else if i := strings.Index(body, "<"+element+">"); i != -1 {
		start = i + 1 // past '<'
	}
	if start == -1 {
		return ""
	}

	valStart := start + len(element) + 1 // past the element name and '>'
	if valStart > len(body) {
		return ""
	}
	if valEnd := strings.Index(body[valStart:], "<"); valEnd != -1 {
		return strings.TrimSpace(body[valStart : valStart+valEnd])
	}
	return ""
}

func (s *onvifServer) extractToken(body, element string) string {
	if token := extractTokenValue(body, element); token != "" {
		return token
	}

	// default fallback
	if len(s.metas) > 0 {
		if s.metas[0].token != "" {
			return s.metas[0].token
		}
		return s.metas[0].name
	}
	return "main"
}

// resolveMeta resolves an explicit ProfileToken to its stream. ok is false
// when the request names a token that does not exist (spec: ter:NoProfile);
// an absent token resolves leniently to the first profile.
func (s *onvifServer) resolveMeta(body string) (*streamMetadata, bool) {
	token := extractTokenValue(body, "ProfileToken")
	if token == "" {
		if len(s.metas) > 0 {
			return s.metas[0], true
		}
		return nil, false
	}
	for _, m := range s.metas {
		if m.token == token || m.name == token {
			return m, true
		}
	}
	return nil, false
}

func (s *onvifServer) mediaStreamURIResponse(r *http.Request, body string) (string, bool) {
	m, ok := s.resolveMeta(body)
	if !ok {
		return "", false
	}
	path := s.cfg.RTSPPath
	if m != nil && m.path != "" {
		path = m.path
	}

	return fmt.Sprintf(
		`<trt:GetStreamUriResponse><trt:MediaUri><tt:Uri>%s</tt:Uri><tt:InvalidAfterConnect>false</tt:InvalidAfterConnect><tt:InvalidAfterReboot>false</tt:InvalidAfterReboot><tt:Timeout>PT0S</tt:Timeout></trt:MediaUri></trt:GetStreamUriResponse>`,
		xmlEscape(buildURL("rtsp", s.authorityForRequest(r, s.cfg.RTSPAddress), path)),
	), true
}

func (s *onvifServer) mediaVideoSourcesResponse(_ string) string {
	var b strings.Builder
	b.WriteString(`<trt:GetVideoSourcesResponse>`)

	// Map to track cameras we've already added a VideoSource for
	added := make(map[string]bool)

	for _, m := range s.metas {
		if added[m.cameraName] {
			continue
		}
		added[m.cameraName] = true

		snap := m.snapshot().normalized()
		fmt.Fprintf(
			&b,
			`<trt:VideoSources token="VideoSource_%s"><tt:Framerate>%d</tt:Framerate><tt:Resolution><tt:Width>%d</tt:Width><tt:Height>%d</tt:Height></tt:Resolution></trt:VideoSources>`,
			xmlEscape(m.cameraName),
			snap.FPS,
			snap.Width,
			snap.Height,
		)
	}

	if len(s.metas) == 0 {
		b.WriteString(`<trt:VideoSources token="VideoSource_0"><tt:Framerate>15</tt:Framerate><tt:Resolution><tt:Width>3840</tt:Width><tt:Height>2160</tt:Height></tt:Resolution></trt:VideoSources>`)
	}

	b.WriteString(`</trt:GetVideoSourcesResponse>`)
	return b.String()
}

func (s *onvifServer) mediaVideoEncoderConfigurationsResponse(_ string) string {
	var b strings.Builder
	b.WriteString(`<trt:GetVideoEncoderConfigurationsResponse>`)
	for _, m := range s.metas {
		token := m.token
		if token == "" {
			token = m.name
		}
		b.WriteString(s.videoEncoderConfigXML("trt:Configurations", token, m.snapshot().normalized()))
	}
	b.WriteString(`</trt:GetVideoEncoderConfigurationsResponse>`)
	return b.String()
}

func (s *onvifServer) mediaAudioSourcesResponse(_ string) string {
	var b strings.Builder
	b.WriteString(`<trt:GetAudioSourcesResponse>`)

	added := make(map[string]bool)
	hasAudio := false

	for _, m := range s.metas {
		if added[m.cameraName] {
			continue
		}
		added[m.cameraName] = true

		snap := m.snapshot().normalized()
		if snap.AudioCodec != "" {
			hasAudio = true
			fmt.Fprintf(
				&b,
				`<trt:AudioSources token="AudioSource_%s"><tt:Channels>%d</tt:Channels></trt:AudioSources>`,
				xmlEscape(m.cameraName),
				snap.AudioChannels,
			)
		}
	}

	if !hasAudio && len(s.metas) == 0 {
		return `<trt:GetAudioSourcesResponse/>`
	}

	b.WriteString(`</trt:GetAudioSourcesResponse>`)
	return b.String()
}

func (s *onvifServer) mediaAudioOutputsResponse(_ string) string {
	var b strings.Builder
	b.WriteString(`<trt:GetAudioOutputsResponse>`)

	added := make(map[string]bool)

	for _, m := range s.metas {
		if added[m.cameraName] {
			continue
		}
		added[m.cameraName] = true

		// If a stream exists, assume the camera has a speaker for 2-way talk
		fmt.Fprintf(
			&b,
			`<trt:AudioOutputs token="AudioOutput_%s"></trt:AudioOutputs>`,
			xmlEscape(m.cameraName),
		)
	}

	b.WriteString(`</trt:GetAudioOutputsResponse>`)
	return b.String()
}

func (s *onvifServer) mediaAudioOutputConfigurationsResponse(_ string) string {
	var b strings.Builder
	b.WriteString(`<trt:GetAudioOutputConfigurationsResponse>`)

	added := make(map[string]bool)

	for _, m := range s.metas {
		if added[m.cameraName] {
			continue
		}
		added[m.cameraName] = true

		token := "AudioOutputConfig_" + xmlEscape(m.cameraName)
		fmt.Fprintf(
			&b,
			`<trt:Configurations token="%s"><tt:Name>%s</tt:Name><tt:UseCount>1</tt:UseCount><tt:OutputToken>AudioOutput_%s</tt:OutputToken></trt:Configurations>`,
			token, token, xmlEscape(m.cameraName),
		)
	}

	b.WriteString(`</trt:GetAudioOutputConfigurationsResponse>`)
	return b.String()
}

func (s *onvifServer) mediaAudioDecoderConfigurationsResponse(_ string) string {
	var b strings.Builder
	b.WriteString(`<trt:GetAudioDecoderConfigurationsResponse>`)

	added := make(map[string]bool)

	for _, m := range s.metas {
		if added[m.cameraName] {
			continue
		}
		added[m.cameraName] = true

		b.WriteString(s.audioDecoderConfigXML("trt:Configurations", m.cameraName, m.snapshot().normalized()))
	}

	b.WriteString(`</trt:GetAudioDecoderConfigurationsResponse>`)
	return b.String()
}

func (s *onvifServer) mediaAudioEncoderConfigurationsResponse(_ string) string {
	var b strings.Builder
	b.WriteString(`<trt:GetAudioEncoderConfigurationsResponse>`)
	for _, m := range s.metas {
		snap := m.snapshot().normalized()
		if snap.AudioCodec != "" {
			token := m.token
			if token == "" {
				token = m.name
			}
			b.WriteString(s.audioEncoderConfigXML("trt:Configurations", token, snap))
		}
	}
	b.WriteString(`</trt:GetAudioEncoderConfigurationsResponse>`)
	return b.String()
}

func (s *onvifServer) audioDecoderConfigXML(tag string, token string, _ streamMetadataSnapshot) string {
	// Expose PCMU (G.711) as a supported decoder so the client knows how to send audio.
	return fmt.Sprintf(
		`<%s token="AudioDecoder_%s">`+
			`<tt:Name>AudioDecoder_%s</tt:Name>`+
			`<tt:UseCount>1</tt:UseCount>`+
			`</%s>`,
		tag,
		xmlEscape(token),
		xmlEscape(token),
		tag,
	)
}

func (s *onvifServer) profileXML(tag string, token string, m *streamMetadata) string {
	var snap streamMetadataSnapshot
	var cameraName string
	if m != nil {
		snap = m.snapshot().normalized()
		cameraName = m.cameraName
	} else {
		snap = streamMetadataSnapshot{}.normalized()
		cameraName = "0"
	}

	videoSourceToken := xmlEscape("VideoSource_" + cameraName)
	audioSourceToken := xmlEscape("AudioSource_" + cameraName)
	profileToken := xmlEscape(token)
	name := xmlEscape(s.cfg.DeviceName + "_" + token)

	// In ONVIF Profile XML, we must link the Video Encoder, Audio Encoder, Video Source, and Audio Source.
	// Many strict VMS systems (Frigate/Synology) require the encoder configuration to be explicitly linked with <tt:VideoEncoderConfiguration> rather than just the raw XML blob without the wrapper.
	// Actually, the videoEncoderConfigXML returns the element itself.

	var b strings.Builder
	fmt.Fprintf(&b, `<%s token="%s" fixed="true">`, tag, profileToken)
	fmt.Fprintf(&b, `<tt:Name>%s</tt:Name>`, name)

	// VideoSource
	fmt.Fprintf(&b, `<tt:VideoSourceConfiguration token="VideoSourceConfig_%s"><tt:Name>VideoSourceConfig_%s</tt:Name><tt:UseCount>1</tt:UseCount><tt:SourceToken>%s</tt:SourceToken><tt:Bounds x="0" y="0" width="%d" height="%d"/></tt:VideoSourceConfiguration>`, profileToken, profileToken, videoSourceToken, snap.Width, snap.Height)

	// AudioSource
	if snap.AudioCodec != "" {
		fmt.Fprintf(&b, `<tt:AudioSourceConfiguration token="AudioSourceConfig_%s"><tt:Name>AudioSourceConfig_%s</tt:Name><tt:UseCount>1</tt:UseCount><tt:SourceToken>%s</tt:SourceToken></tt:AudioSourceConfiguration>`, profileToken, profileToken, audioSourceToken)
	}

	// VideoEncoder (empty for H265 — not representable in ver10)
	b.WriteString(s.videoEncoderConfigXML("tt:VideoEncoderConfiguration", token, snap))

	// AudioEncoder
	if snap.AudioCodec != "" {
		b.WriteString(s.audioEncoderConfigXML("tt:AudioEncoderConfiguration", token, snap))
	}

	// Analytics precedes PTZ in the tt:Profile sequence
	b.WriteString(analyticsConfigurationXML("tt:VideoAnalyticsConfiguration", cameraName))

	// PTZ (clients like Frigate/HA require the profile to carry a PTZConfiguration)
	b.WriteString(ptzConfigurationXML("tt:PTZConfiguration", cameraName))

	// Per onvif.xsd, AudioOutput/AudioDecoder configurations live inside
	// tt:Extension, after PTZConfiguration (required for 2-way audio).
	audioOutputToken := xmlEscape("AudioOutput_" + cameraName)
	b.WriteString(`<tt:Extension>`)
	fmt.Fprintf(&b, `<tt:AudioOutputConfiguration token="AudioOutputConfig_%s"><tt:Name>AudioOutputConfig_%s</tt:Name><tt:UseCount>1</tt:UseCount><tt:OutputToken>%s</tt:OutputToken></tt:AudioOutputConfiguration>`, profileToken, profileToken, audioOutputToken)
	b.WriteString(s.audioDecoderConfigXML("tt:AudioDecoderConfiguration", cameraName, snap))
	b.WriteString(`</tt:Extension>`)

	fmt.Fprintf(&b, `</%s>`, tag)
	return b.String()
}

func onvifProfileToken(cameraName string, streamName string) string {
	replacer := strings.NewReplacer(" ", "_", "/", "_", "\\", "_")
	cameraName = replacer.Replace(strings.TrimSpace(cameraName))
	streamName = replacer.Replace(strings.TrimSpace(streamName))

	if cameraName == "" {
		cameraName = "camera"
	}
	if streamName == "" {
		streamName = "main"
	}

	return cameraName + "_" + streamName
}

// videoEncoderConfigXML emits a ver10 tt:VideoEncoderConfiguration. The ver10
// schema only knows JPEG/MPEG4/H264, so H265 streams return "" — they are
// described by the media2 service instead. Sequence per onvif.xsd: Encoding,
// Resolution, Quality, RateControl?, H264?, Multicast, SessionTimeout.
func (s *onvifServer) videoEncoderConfigXML(tag string, token string, snap streamMetadataSnapshot) string {
	if snap.VideoCodec != "H264" {
		return ""
	}

	return fmt.Sprintf(
		`<%s token="VideoEncoder_%s"><tt:Name>VideoEncoder_%s</tt:Name><tt:UseCount>1</tt:UseCount><tt:Encoding>H264</tt:Encoding><tt:Resolution><tt:Width>%d</tt:Width><tt:Height>%d</tt:Height></tt:Resolution><tt:Quality>5</tt:Quality><tt:RateControl><tt:FrameRateLimit>%d</tt:FrameRateLimit><tt:EncodingInterval>1</tt:EncodingInterval><tt:BitrateLimit>4096</tt:BitrateLimit></tt:RateControl><tt:H264><tt:GovLength>50</tt:GovLength><tt:H264Profile>Main</tt:H264Profile></tt:H264><tt:Multicast><tt:Address><tt:Type>IPv4</tt:Type><tt:IPv4Address>0.0.0.0</tt:IPv4Address></tt:Address><tt:Port>0</tt:Port><tt:TTL>0</tt:TTL><tt:AutoStart>false</tt:AutoStart></tt:Multicast><tt:SessionTimeout>PT60S</tt:SessionTimeout></%s>`,
		tag,
		xmlEscape(token),
		xmlEscape(token),
		snap.Width,
		snap.Height,
		snap.FPS,
		tag,
	)
}

func (s *onvifServer) audioEncoderConfigXML(tag string, token string, snap streamMetadataSnapshot) string {
	if snap.AudioSampleRate == 0 {
		snap.AudioSampleRate = 16000
	}
	if snap.AudioChannels == 0 {
		snap.AudioChannels = 1
	}

	encoding := snap.AudioCodec
	if encoding == "" {
		encoding = "AAC"
	}
	// G711 must be G711 according to ONVIF
	if encoding == "PCMA" || encoding == "PCMU" {
		encoding = "G711"
	}

	return fmt.Sprintf(
		`<%s token="AudioEncoder_%s"><tt:Name>%s</tt:Name><tt:UseCount>1</tt:UseCount><tt:Encoding>%s</tt:Encoding><tt:Bitrate>128</tt:Bitrate><tt:SampleRate>%d</tt:SampleRate><tt:Multicast><tt:Address><tt:Type>IPv4</tt:Type><tt:IPv4Address>0.0.0.0</tt:IPv4Address></tt:Address><tt:Port>0</tt:Port><tt:TTL>0</tt:TTL><tt:AutoStart>false</tt:AutoStart></tt:Multicast><tt:SessionTimeout>PT60S</tt:SessionTimeout></%s>`,
		tag,
		xmlEscape(token),
		snap.AudioCodec,
		encoding,
		snap.AudioSampleRate,
		tag,
	)
}

func (s *onvifServer) deviceServiceURL(r *http.Request) string {
	return buildURL("http", s.authorityForRequest(r, s.cfg.Address), s.cfg.DevicePath)
}

func (s *onvifServer) mediaServiceURL(r *http.Request) string {
	return buildURL("http", s.authorityForRequest(r, s.cfg.Address), s.cfg.MediaPath)
}

func (s *onvifServer) media2ServiceURL(r *http.Request) string {
	path := s.cfg.Media2Path
	if path == "" {
		path = "/onvif/media2_service"
	}
	return buildURL("http", s.authorityForRequest(r, s.cfg.Address), path)
}

func (s *onvifServer) authorityForRequest(r *http.Request, listenAddr string) string {
	if s.cfg.AdvertiseHost != "" {
		return advertisedAuthority(listenAddr, s.cfg.AdvertiseHost)
	}

	if r != nil && r.Host != "" {
		host := r.Host
		if parsedHost, _, err := net.SplitHostPort(r.Host); err == nil {
			host = parsedHost
		}
		return advertisedAuthority(listenAddr, host)
	}

	return advertisedAuthority(listenAddr, "")
}

func soapAction(r *http.Request, body string, known []string) string {
	if raw := strings.Trim(strings.TrimSpace(r.Header.Get("SOAPAction")), `"`); raw != "" {
		if idx := strings.LastIndexAny(raw, "/#"); idx >= 0 && idx < len(raw)-1 {
			// Some clients send a quoted SOAPAction like "http://www.onvif.org/ver10/media/wsdl/GetStreamUri"
			// Extracting the final part of the URL path as the action name
			action := raw[idx+1:]

			// Some NVRs prefix it with trt: or tr2: like "trt:GetStreamUri" in the header!
			if colonIdx := strings.IndexByte(action, ':'); colonIdx >= 0 {
				action = action[colonIdx+1:]
			}
			return action
		}

		// Un-namespaced raw action
		if colonIdx := strings.IndexByte(raw, ':'); colonIdx >= 0 {
			raw = raw[colonIdx+1:]
		}
		return raw
	}

	// Sort known actions by length descending so longer matching strings win
	// e.g. "GetProfiles" matched before "GetProfile"
	for i := 0; i < len(known); i++ {
		for j := i + 1; j < len(known); j++ {
			if len(known[i]) < len(known[j]) {
				known[i], known[j] = known[j], known[i]
			}
		}
	}

	for _, action := range known {
		if hasSOAPActionBody(body, action) {
			return action
		}
	}
	return ""
}

func hasSOAPActionBody(body string, action string) bool {
	patterns := []string{
		":" + action + ">",
		":" + action + " ",
		":" + action + "/",
		"<" + action + ">",
		"<" + action + " ",
		"<" + action + "/",
		action, // fallback just in case namespace is completely omitted or weird
	}

	for _, pattern := range patterns {
		if strings.Contains(body, pattern) {
			return true
		}
	}

	return false
}

// writeProfileScopedResponse writes the response for an action that resolves
// an explicit ProfileToken, faulting ter:NoProfile when the token is unknown.
func writeProfileScopedResponse(w http.ResponseWriter, xmlBody string, ok bool) {
	if !ok {
		writeSOAPFault(w, http.StatusBadRequest, "ter:NoProfile", "the requested profile token does not exist")
		return
	}
	writeSOAPResponse(w, xmlBody)
}

func writeSOAPResponse(w http.ResponseWriter, inner string) {
	w.Header().Set("Content-Type", `application/soap+xml; charset=utf-8`)
	w.WriteHeader(http.StatusOK)
	_, _ = io.WriteString(w, soapEnvelope(inner))
}

func writeSOAPFault(w http.ResponseWriter, statusCode int, subcode string, reason string) {
	writeSOAPFaultCode(w, statusCode, "soap:Sender", subcode, reason)
}

// writeSOAPServerFault reports a server-side failure (SOAP 1.2 Receiver code)
// — use for errors the client's request did not cause, e.g. camera I/O.
func writeSOAPServerFault(w http.ResponseWriter, subcode string, reason string) {
	writeSOAPFaultCode(w, http.StatusInternalServerError, "soap:Receiver", subcode, reason)
}

func writeSOAPFaultCode(w http.ResponseWriter, statusCode int, code string, subcode string, reason string) {
	w.Header().Set("Content-Type", `application/soap+xml; charset=utf-8`)
	w.WriteHeader(statusCode)
	_, _ = io.WriteString(w, soapEnvelope(
		fmt.Sprintf(
			`<soap:Fault><soap:Code><soap:Value>%s</soap:Value><soap:Subcode><soap:Value>%s</soap:Value></soap:Subcode></soap:Code><soap:Reason><soap:Text xml:lang="en">%s</soap:Text></soap:Reason></soap:Fault>`,
			code,
			xmlEscape(subcode),
			xmlEscape(reason),
		),
	))
}

func soapEnvelope(inner string) string {
	return `<?xml version="1.0" encoding="UTF-8"?>` +
		`<soap:Envelope xmlns:soap="http://www.w3.org/2003/05/soap-envelope" xmlns:tds="http://www.onvif.org/ver10/device/wsdl" xmlns:trt="http://www.onvif.org/ver10/media/wsdl" xmlns:tr2="http://www.onvif.org/ver20/media/wsdl" xmlns:tptz="http://www.onvif.org/ver20/ptz/wsdl" xmlns:tev="http://www.onvif.org/ver10/events/wsdl" xmlns:timg="http://www.onvif.org/ver20/imaging/wsdl" xmlns:tan="http://www.onvif.org/ver20/analytics/wsdl" xmlns:trc="http://www.onvif.org/ver10/recording/wsdl" xmlns:tse="http://www.onvif.org/ver10/search/wsdl" xmlns:wsnt="http://docs.oasis-open.org/wsn/b-2" xmlns:wstop="http://docs.oasis-open.org/wsn/t-1" xmlns:tns1="http://www.onvif.org/ver10/topics" xmlns:wsa="http://www.w3.org/2005/08/addressing" xmlns:xs="http://www.w3.org/2001/XMLSchema" xmlns:tt="http://www.onvif.org/ver10/schema" xmlns:ter="http://www.onvif.org/ver10/error">` +
		`<soap:Body>` + inner + `</soap:Body></soap:Envelope>`
}

func xmlEscape(v string) string {
	var buf bytes.Buffer
	_ = xml.EscapeText(&buf, []byte(v))
	return buf.String()
}
