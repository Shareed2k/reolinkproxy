package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func doImaging(t *testing.T, s *onvifServer, action, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest("POST", "/onvif/imaging_service", strings.NewReader(body))
	req.Header.Set("SOAPAction", `"http://www.onvif.org/ver20/imaging/wsdl/`+action+`"`)
	rec := httptest.NewRecorder()
	s.handleImaging(rec, req)
	return rec
}

func TestImagingService(t *testing.T) {
	t.Parallel()

	server := &onvifServer{
		cfg:   onvifConfig{ImagingPath: "/onvif/imaging_service"},
		metas: []*streamMetadata{{cameraName: "front", name: "main", token: "front_main"}},
	}

	t.Run("GetOptions carries camera-native ranges", func(t *testing.T) {
		t.Parallel()
		rec := doImaging(t, server, "GetOptions", `<GetOptions><VideoSourceToken>VideoSource_front</VideoSourceToken></GetOptions>`)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d", rec.Code)
		}
		body := rec.Body.String()
		for _, want := range []string{"<tt:Brightness>", "<tt:ColorSaturation>", "<tt:Contrast>", "<tt:Sharpness>", "<tt:Max>255.0</tt:Max>"} {
			if !strings.Contains(body, want) {
				t.Fatalf("GetOptions missing %q:\n%s", want, body)
			}
		}
	})

	t.Run("unknown video source faults", func(t *testing.T) {
		t.Parallel()
		rec := doImaging(t, server, "GetImagingSettings", `<GetImagingSettings><VideoSourceToken>VideoSource_other</VideoSourceToken></GetImagingSettings>`)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400", rec.Code)
		}
	})

	t.Run("known source without device faults as server error path", func(t *testing.T) {
		t.Parallel()
		rec := doImaging(t, server, "GetImagingSettings", `<GetImagingSettings><VideoSourceToken>VideoSource_front</VideoSourceToken></GetImagingSettings>`)
		// device is nil in this test server, so resolution succeeds but the guard rejects
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400 (no device wired)", rec.Code)
		}
	})

	t.Run("device advertises imaging", func(t *testing.T) {
		t.Parallel()
		req := httptest.NewRequest("POST", "/onvif/device_service", nil)
		services := server.deviceServicesResponse(req)
		if !strings.Contains(services, "http://www.onvif.org/ver20/imaging/wsdl") {
			t.Fatalf("GetServices does not advertise imaging:\n%s", services)
		}
		capabilities := server.deviceCapabilitiesResponse(req)
		if !strings.Contains(capabilities, "<tt:Imaging><tt:XAddr>") {
			t.Fatalf("GetCapabilities does not carry imaging:\n%s", capabilities)
		}
	})
}
