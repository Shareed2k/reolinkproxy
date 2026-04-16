package main

import (
	"strings"
	"testing"
)

func TestBuildProbeMatch(t *testing.T) {
	cfg := onvifConfig{
		DeviceName:    "Test Camera",
		Model:         "Model X",
		Address:       "127.0.0.1:8002",
		DevicePath:    "/onvif/device_service",
		AdvertiseHost: "127.0.0.1",
	}
	server := &wsDiscoveryServer{cfg: cfg}

	response := server.buildProbeMatch("urn:uuid:test-relates-to")

	expectedSubstrings := []string{
		"<env:Envelope",
		"<wsa:Action wsa:mustUnderstand=\"true\">http://schemas.xmlsoap.org/ws/2005/04/discovery/ProbeMatches</wsa:Action>",
		"<wsa:RelatesTo>urn:uuid:test-relates-to</wsa:RelatesTo>",
		"<d:Types>dn:NetworkVideoTransmitter</d:Types>",
		"onvif://www.onvif.org/hardware/Model_X",
		"onvif://www.onvif.org/name/Test_Camera",
		"http://127.0.0.1:8002/onvif/device_service",
	}

	for _, sub := range expectedSubstrings {
		if !strings.Contains(response, sub) {
			t.Errorf("expected response to contain %q, but it didn't.\nResponse: %s", sub, response)
		}
	}
}
