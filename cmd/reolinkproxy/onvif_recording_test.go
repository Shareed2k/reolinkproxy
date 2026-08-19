package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/shareed2k/reolinkproxy/pkg/baichuan"
)

func doSearch(t *testing.T, s *onvifServer, path, action, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest("POST", path, strings.NewReader(body))
	ns := "http://www.onvif.org/ver10/search/wsdl"
	if strings.Contains(path, "recording") {
		ns = "http://www.onvif.org/ver10/recording/wsdl"
	}
	req.Header.Set("SOAPAction", `"`+ns+`/`+action+`"`)
	rec := httptest.NewRecorder()
	if strings.Contains(path, "recording") {
		s.handleRecording(rec, req)
	} else {
		s.handleSearch(rec, req)
	}
	return rec
}

func newRecordingTestServer() *onvifServer {
	return &onvifServer{
		cfg: onvifConfig{RecordingPath: "/onvif/recording_service", SearchPath: "/onvif/search_service"},
		metas: []*streamMetadata{
			{cameraName: "front", name: "main", token: "front_main"},
		},
	}
}

func TestGetRecordingsListsCameras(t *testing.T) {
	t.Parallel()

	rec := doSearch(t, newRecordingTestServer(), "/onvif/recording_service", "GetRecordings", "<GetRecordings/>")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "<trc:RecordingToken>Recording_front</trc:RecordingToken>") {
		t.Fatalf("GetRecordings missing per-camera recording:\n%s", rec.Body.String())
	}
}

func TestSearchSessionResults(t *testing.T) {
	t.Parallel()

	s := newRecordingTestServer()
	start := time.Date(2026, 8, 19, 10, 0, 0, 0, time.UTC)
	session := &onvifSearchSession{
		files: []recordingSearchHit{
			{
				recordingToken: "Recording_front",
				file: baichuan.RecordingFile{
					FileName:  "01_20260819100000.mp4",
					AlarmType: "people",
					StartTime: start,
					EndTime:   start.Add(30 * time.Second),
				},
			},
		},
	}
	token := s.searches.put(session)

	rec := doSearch(t, s, "/onvif/search_service", "GetRecordingSearchResults",
		"<GetRecordingSearchResults><SearchToken>"+token+"</SearchToken></GetRecordingSearchResults>")
	body := rec.Body.String()
	for _, want := range []string{"<tt:SearchState>Completed</tt:SearchState>", "Recording_front", "<tt:EarliestRecording>2026-08-19T10:00:00Z</tt:EarliestRecording>"} {
		if !strings.Contains(body, want) {
			t.Fatalf("recording results missing %q:\n%s", want, body)
		}
	}

	rec = doSearch(t, s, "/onvif/search_service", "GetEventSearchResults",
		"<GetEventSearchResults><SearchToken>"+token+"</SearchToken></GetEventSearchResults>")
	body = rec.Body.String()
	for _, want := range []string{"tns1:RecordingHistory/Recording", `Name="IsRecording" Value="true"`, `Name="IsRecording" Value="false"`, `Name="FileName" Value="01_20260819100000.mp4"`, `Name="AlarmType" Value="people"`} {
		if !strings.Contains(body, want) {
			t.Fatalf("event results missing %q:\n%s", want, body)
		}
	}

	rec = doSearch(t, s, "/onvif/search_service", "EndSearch",
		"<EndSearch><SearchToken>"+token+"</SearchToken></EndSearch>")
	if rec.Code != http.StatusOK {
		t.Fatalf("EndSearch status = %d", rec.Code)
	}
	rec = doSearch(t, s, "/onvif/search_service", "GetRecordingSearchResults",
		"<GetRecordingSearchResults><SearchToken>"+token+"</SearchToken></GetRecordingSearchResults>")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("results after EndSearch status = %d, want 400", rec.Code)
	}
}

func TestFindRecordingsRequiresStartPoint(t *testing.T) {
	t.Parallel()

	rec := doSearch(t, newRecordingTestServer(), "/onvif/search_service", "FindRecordings", "<FindRecordings/>")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}
