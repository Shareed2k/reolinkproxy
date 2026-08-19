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
		"<wsa:Action env:mustUnderstand=\"true\">http://schemas.xmlsoap.org/ws/2005/04/discovery/ProbeMatches</wsa:Action>",
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

func TestMatchesProbeScopes(t *testing.T) {
	t.Parallel()

	advertised := onvifScopes(onvifConfig{Model: "Test Model", DeviceName: "Front Door"})

	tests := []struct {
		name      string
		requested string
		want      bool
	}{
		{name: "empty scopes match everything", requested: "", want: true},
		{name: "profile scope matches", requested: "onvif://www.onvif.org/Profile/S", want: true},
		{name: "prefix scope matches", requested: "onvif://www.onvif.org/Profile", want: true},
		{name: "name scope matches", requested: "onvif://www.onvif.org/name/Front_Door", want: true},
		{name: "unknown scope rejected", requested: "onvif://www.onvif.org/location/warehouse", want: false},
		{name: "one bad scope rejects all", requested: "onvif://www.onvif.org/Profile/S onvif://www.onvif.org/location/x", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := matchesProbeScopes(tt.requested, advertised); got != tt.want {
				t.Fatalf("matchesProbeScopes(%q) = %v, want %v", tt.requested, got, tt.want)
			}
		})
	}
}

func TestDeviceUUIDStable(t *testing.T) {
	t.Parallel()

	cfg := onvifConfig{DeviceName: "cam", SerialNumber: "123"}
	if deviceUUID(cfg) != deviceUUID(cfg) {
		t.Fatal("deviceUUID must be stable for the same identity")
	}
	other := onvifConfig{DeviceName: "cam2", SerialNumber: "123"}
	if deviceUUID(cfg) == deviceUUID(other) {
		t.Fatal("deviceUUID must differ for different identities")
	}

	match := (&wsDiscoveryServer{cfg: cfg}).buildProbeMatch("urn:uuid:x")
	if !strings.Contains(match, "urn:uuid:"+deviceUUID(cfg)) {
		t.Fatalf("ProbeMatch must carry the stable device UUID:\n%s", match)
	}
	if !strings.Contains(match, `env:mustUnderstand="true"`) {
		t.Fatalf("mustUnderstand must be in the SOAP envelope namespace:\n%s", match)
	}
}
