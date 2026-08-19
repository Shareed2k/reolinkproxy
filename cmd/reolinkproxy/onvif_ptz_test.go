package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestPTZDirection(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		x, y      float64
		wantDir   string
		wantSpeed int
	}{
		{name: "right full speed", x: 1, y: 0, wantDir: "right", wantSpeed: 64},
		{name: "left half speed", x: -0.5, y: 0, wantDir: "left", wantSpeed: 32},
		{name: "up", x: 0, y: 0.25, wantDir: "up", wantSpeed: 16},
		{name: "down", x: 0, y: -1, wantDir: "down", wantSpeed: 64},
		{name: "dominant axis wins", x: 0.2, y: -0.9, wantDir: "down", wantSpeed: 58},
		{name: "zero velocity is stop", x: 0, y: 0, wantDir: "stop", wantSpeed: 32},
		{name: "tiny magnitude clamps to 1", x: 0.001, y: 0, wantDir: "right", wantSpeed: 1},
		{name: "overrange clamps to 64", x: 2, y: 0, wantDir: "right", wantSpeed: 64},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			dir, speed := ptzDirection(tt.x, tt.y)
			if dir != tt.wantDir || speed != tt.wantSpeed {
				t.Fatalf("ptzDirection(%v, %v) = (%q, %d), want (%q, %d)", tt.x, tt.y, dir, speed, tt.wantDir, tt.wantSpeed)
			}
		})
	}
}

func TestExtractPanTiltVelocity(t *testing.T) {
	t.Parallel()

	// zeep/python-onvif style body as sent by Frigate.
	body := `<soap-env:Envelope xmlns:soap-env="http://www.w3.org/2003/05/soap-envelope"><soap-env:Body>` +
		`<ns0:ContinuousMove xmlns:ns0="http://www.onvif.org/ver20/ptz/wsdl"><ns0:ProfileToken>cam_main</ns0:ProfileToken>` +
		`<ns0:Velocity><ns1:PanTilt xmlns:ns1="http://www.onvif.org/ver10/schema" x="0.5" y="-0.25"/><ns1:Zoom xmlns:ns1="http://www.onvif.org/ver10/schema" x="0.0"/></ns0:Velocity>` +
		`</ns0:ContinuousMove></soap-env:Body></soap-env:Envelope>`

	x, y, ok := extractPanTiltVelocity(body)
	if !ok {
		t.Fatal("extractPanTiltVelocity() ok = false, want true")
	}
	if x != 0.5 || y != -0.25 {
		t.Fatalf("extractPanTiltVelocity() = (%v, %v), want (0.5, -0.25)", x, y)
	}

	if _, _, ok := extractPanTiltVelocity("<Stop/>"); ok {
		t.Fatal("extractPanTiltVelocity() on body without PanTilt: ok = true, want false")
	}
}

func newPTZTestServer() *onvifServer {
	return &onvifServer{
		cfg: onvifConfig{PTZPath: "/onvif/ptz_service"},
		metas: []*streamMetadata{
			{cameraName: "front", name: "main", token: "front_main"},
			{cameraName: "front", name: "sub", token: "front_sub"},
		},
	}
}

func doPTZ(t *testing.T, s *onvifServer, action, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest("POST", "/onvif/ptz_service", strings.NewReader(body))
	req.Header.Set("SOAPAction", `"http://www.onvif.org/ver20/ptz/wsdl/`+action+`"`)
	rec := httptest.NewRecorder()
	s.handlePTZ(rec, req)
	return rec
}

func TestHandlePTZStaticResponses(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		action     string
		body       string
		wantStatus int
		wantSubstr []string
	}{
		{
			name:       "GetConfigurations lists one config per camera",
			action:     "GetConfigurations",
			body:       "<GetConfigurations/>",
			wantStatus: http.StatusOK,
			wantSubstr: []string{`<tptz:PTZConfiguration token="PTZConfig_front">`, `<tt:NodeToken>PTZNode_front</tt:NodeToken>`, `<tt:DefaultPTZTimeout>PT5S</tt:DefaultPTZTimeout>`},
		},
		{
			name:       "GetNodes lists node with spaces",
			action:     "GetNodes",
			body:       "<GetNodes/>",
			wantStatus: http.StatusOK,
			wantSubstr: []string{`<tptz:PTZNode token="PTZNode_front"`, `VelocityGenericSpace`, `<tt:HomeSupported>false</tt:HomeSupported>`},
		},
		{
			name:       "GetConfigurationOptions carries required Spaces and PTZTimeout",
			action:     "GetConfigurationOptions",
			body:       "<GetConfigurationOptions><ConfigurationToken>PTZConfig_front</ConfigurationToken></GetConfigurationOptions>",
			wantStatus: http.StatusOK,
			wantSubstr: []string{`<tt:Spaces>`, `<tt:PTZTimeout>`, `<tt:XRange><tt:Min>-1.0</tt:Min><tt:Max>1.0</tt:Max></tt:XRange>`},
		},
		{
			name:       "GetServiceCapabilities",
			action:     "GetServiceCapabilities",
			body:       "<GetServiceCapabilities/>",
			wantStatus: http.StatusOK,
			wantSubstr: []string{`<tptz:Capabilities`},
		},
		{
			name:       "GetStatus reports idle with UtcTime",
			action:     "GetStatus",
			body:       "<GetStatus><ProfileToken>front_main</ProfileToken></GetStatus>",
			wantStatus: http.StatusOK,
			wantSubstr: []string{`<tt:PanTilt>IDLE</tt:PanTilt>`, `<tt:UtcTime>`},
		},
		{
			name:       "ContinuousMove without device faults",
			action:     "ContinuousMove",
			body:       `<ContinuousMove><ProfileToken>front_main</ProfileToken><Velocity><PanTilt x="0.5" y="0"/></Velocity></ContinuousMove>`,
			wantStatus: http.StatusBadRequest,
			wantSubstr: []string{`ter:NoPTZProfile`},
		},
		{
			name:       "GotoPreset with bad token faults",
			action:     "GotoPreset",
			body:       `<GotoPreset><ProfileToken>front_main</ProfileToken><PresetToken>abc</PresetToken></GotoPreset>`,
			wantStatus: http.StatusBadRequest,
			wantSubstr: []string{`ter:InvalidArgVal`},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			rec := doPTZ(t, newPTZTestServer(), tt.action, tt.body)
			if rec.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d; body:\n%s", rec.Code, tt.wantStatus, rec.Body.String())
			}
			for _, substr := range tt.wantSubstr {
				if !strings.Contains(rec.Body.String(), substr) {
					t.Fatalf("response does not contain %q; body:\n%s", substr, rec.Body.String())
				}
			}
		})
	}
}

func TestDeviceAdvertisesPTZService(t *testing.T) {
	t.Parallel()

	s := newPTZTestServer()
	req := httptest.NewRequest("POST", "/onvif/device_service", nil)

	services := s.deviceServicesResponse(req)
	if !strings.Contains(services, "http://www.onvif.org/ver20/ptz/wsdl") {
		t.Fatalf("GetServices response does not advertise the PTZ namespace:\n%s", services)
	}
	if !strings.Contains(services, "/onvif/ptz_service") {
		t.Fatalf("GetServices response does not carry the PTZ XAddr:\n%s", services)
	}

	capabilities := s.deviceCapabilitiesResponse(req)
	if !strings.Contains(capabilities, "<tt:PTZ><tt:XAddr>") {
		t.Fatalf("GetCapabilities response does not carry the PTZ section:\n%s", capabilities)
	}
}

func TestProfileCarriesPTZConfiguration(t *testing.T) {
	t.Parallel()

	s := newPTZTestServer()
	profile := s.profileXML("trt:Profiles", "front_main", s.metas[0])
	if !strings.Contains(profile, `<tt:PTZConfiguration token="PTZConfig_front">`) {
		t.Fatalf("media profile does not carry PTZConfiguration:\n%s", profile)
	}
}

func TestExtractZoomPosition(t *testing.T) {
	t.Parallel()

	body := `<AbsoluteMove><ProfileToken>front_main</ProfileToken><Position><ns1:Zoom xmlns:ns1="http://www.onvif.org/ver10/schema" x="0.75"/></Position></AbsoluteMove>`
	zoom, ok := extractZoomPosition(body)
	if !ok || zoom != 0.75 {
		t.Fatalf("extractZoomPosition() = (%v, %v), want (0.75, true)", zoom, ok)
	}

	if _, ok := extractZoomPosition("<AbsoluteMove/>"); ok {
		t.Fatal("extractZoomPosition without Zoom element must not match")
	}
}

func TestPTZMoveTracker(t *testing.T) {
	t.Parallel()

	var tracker ptzMoveTracker
	stopped := make(chan struct{})
	tracker.start("cam", 30*time.Millisecond, func() { close(stopped) })

	if !tracker.moving("cam") {
		t.Fatal("tracker must report MOVING during a burst")
	}
	select {
	case <-stopped:
	case <-time.After(2 * time.Second):
		t.Fatal("stop function was not invoked after the burst duration")
	}
	// The stop callback runs before the tracker clears the entry; give it a moment.
	deadline := time.Now().Add(time.Second)
	for tracker.moving("cam") {
		if time.Now().After(deadline) {
			t.Fatal("tracker must report IDLE after the burst")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestPTZMoveTrackerReplacement(t *testing.T) {
	t.Parallel()

	var tracker ptzMoveTracker
	firstStopped := false
	tracker.start("cam", time.Hour, func() { firstStopped = true })
	tracker.start("cam", 20*time.Millisecond, func() {})

	time.Sleep(200 * time.Millisecond)
	if firstStopped {
		t.Fatal("replaced move's stop function must not fire")
	}
	if tracker.moving("cam") {
		t.Fatal("tracker must be idle after the replacing burst finished")
	}
}

func TestPTZRelativeMoveWithoutDeviceFaults(t *testing.T) {
	t.Parallel()

	rec := doPTZ(t, newPTZTestServer(), "RelativeMove",
		`<RelativeMove><ProfileToken>front_main</ProfileToken><Translation><PanTilt x="0.3" y="0"/></Translation></RelativeMove>`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (no device wired)", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "ter:NoPTZProfile") {
		t.Fatalf("expected ter:NoPTZProfile, got:\n%s", rec.Body.String())
	}
}

func TestPTZSpacesAdvertiseRelativeAndZoom(t *testing.T) {
	t.Parallel()

	rec := doPTZ(t, newPTZTestServer(), "GetConfigurationOptions", "<GetConfigurationOptions/>")
	body := rec.Body.String()
	for _, want := range []string{"RelativePanTiltTranslationSpace", "AbsoluteZoomPositionSpace", "TranslationGenericSpace"} {
		if !strings.Contains(body, want) {
			t.Fatalf("options missing %q:\n%s", want, body)
		}
	}

	rec = doPTZ(t, newPTZTestServer(), "GetServiceCapabilities", "<GetServiceCapabilities/>")
	if !strings.Contains(rec.Body.String(), `MoveStatus="true"`) {
		t.Fatalf("MoveStatus must be advertised:\n%s", rec.Body.String())
	}
}
