package main

import (
	"strings"
	"testing"
	"time"
)

func TestLoadCamerasFromEntries(t *testing.T) {
	t.Parallel()

	cameras, err := loadCamerasFromEntries([]string{
		"REOLINK_CAMERA_1_NAME=garage",
		"REOLINK_CAMERA_1_UID=9527000000000000",
		"REOLINK_CAMERA_1_USERNAME=admin",
		"REOLINK_CAMERA_1_PASSWORD=secret",
		"REOLINK_CAMERA_0_NAME=front",
		"REOLINK_CAMERA_0_HOST=192.168.1.10",
		"REOLINK_CAMERA_0_TIMEOUT=15s",
		"REOLINK_CAMERA_0_RTSP_PATH=front/custom",
		"REOLINK_CAMERA_0_STREAM=main,sub",
		"REOLINK_CAMERA_0_TALK_PROFILE=sub",
		"REOLINK_CAMERA_0_CHANNEL=1",
		"REOLINK_CAMERA_0_PAUSE_ON_MOTION=true",
		"REOLINK_CAMERA_0_PAUSE_ON_CLIENT=true",
		"REOLINK_CAMERA_0_PAUSE_TIMEOUT=3s",
		"REOLINK_CAMERA_0_IDLE_DISCONNECT=true",
		"REOLINK_CAMERA_0_IDLE_TIMEOUT=45s",
		"UNRELATED_KEY=value",
	})
	if err != nil {
		t.Fatalf("loadCamerasFromEntries returned error: %v", err)
	}

	if len(cameras) != 2 {
		t.Fatalf("expected 2 cameras, got %d", len(cameras))
	}

	if cameras[0].Name != "front" {
		t.Fatalf("unexpected first camera name: %q", cameras[0].Name)
	}
	if cameras[0].Host != "192.168.1.10" {
		t.Fatalf("unexpected first camera host: %q", cameras[0].Host)
	}
	if cameras[0].Port != 9000 {
		t.Fatalf("unexpected first camera default port: %d", cameras[0].Port)
	}
	if cameras[0].Timeout != 15*time.Second {
		t.Fatalf("unexpected first camera timeout: %v", cameras[0].Timeout)
	}
	if cameras[0].RTSPPath != "front/custom" {
		t.Fatalf("unexpected first camera rtsp path: %q", cameras[0].RTSPPath)
	}
	if cameras[0].TalkProfile != "sub" {
		t.Fatalf("unexpected first camera talk profile: %q", cameras[0].TalkProfile)
	}
	if cameras[0].Channel != 1 {
		t.Fatalf("unexpected first camera channel: %d", cameras[0].Channel)
	}
	if !cameras[0].PauseOnMotion {
		t.Fatal("expected first camera pause_on_motion to be true")
	}
	if !cameras[0].PauseOnClient {
		t.Fatal("expected first camera pause_on_client to be true")
	}
	if cameras[0].PauseTimeout != 3*time.Second {
		t.Fatalf("unexpected first camera pause timeout: %v", cameras[0].PauseTimeout)
	}
	if !cameras[0].IdleDisconnect {
		t.Fatal("expected first camera idle_disconnect to be true")
	}
	if cameras[0].IdleTimeout != 45*time.Second {
		t.Fatalf("unexpected first camera idle timeout: %v", cameras[0].IdleTimeout)
	}

	if cameras[1].Name != "garage" {
		t.Fatalf("unexpected second camera name: %q", cameras[1].Name)
	}
	if cameras[1].UID != "9527000000000000" {
		t.Fatalf("unexpected second camera uid: %q", cameras[1].UID)
	}
	if cameras[1].Stream != "main" {
		t.Fatalf("unexpected second camera default stream: %q", cameras[1].Stream)
	}
	if cameras[1].RTSPPath != "garage/stream" {
		t.Fatalf("unexpected second camera default rtsp path: %q", cameras[1].RTSPPath)
	}
	if cameras[1].TalkEncoder != "internal" {
		t.Fatalf("unexpected second camera default talk encoder: %q", cameras[1].TalkEncoder)
	}
}

func TestApplyCameraDefaultsBatteryCameraDoesNotEnableIdleDisconnect(t *testing.T) {
	t.Parallel()

	camera := CameraConfig{
		Name:          "front",
		Host:          "192.168.1.10",
		BatteryCamera: true,
	}

	applyCameraDefaults(&camera)

	if camera.IdleDisconnect {
		t.Fatal("expected battery camera not to enable idle_disconnect automatically")
	}
	if camera.IdleTimeout != 30*time.Second {
		t.Fatalf("unexpected default idle timeout: %v", camera.IdleTimeout)
	}
}

func TestLoadCamerasFromEntriesReturnsValidationError(t *testing.T) {
	t.Parallel()

	_, err := loadCamerasFromEntries([]string{
		"REOLINK_CAMERA_2_NAME=front",
	})
	if err == nil {
		t.Fatal("expected validation error")
	}
	if !strings.Contains(err.Error(), "REOLINK_CAMERA_2_*") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestLoadCamerasFromEntriesRejectsInvalidTalkProfile(t *testing.T) {
	t.Parallel()

	_, err := loadCamerasFromEntries([]string{
		"REOLINK_CAMERA_0_NAME=front",
		"REOLINK_CAMERA_0_HOST=192.168.1.10",
		"REOLINK_CAMERA_0_STREAM=main,sub",
		"REOLINK_CAMERA_0_TALK_PROFILE=extern",
	})
	if err == nil {
		t.Fatal("expected validation error")
	}
	if !strings.Contains(err.Error(), "talk_profile") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestLoadCamerasFromEntriesReturnsParseError(t *testing.T) {
	t.Parallel()

	_, err := loadCamerasFromEntries([]string{
		"REOLINK_CAMERA_0_NAME=front",
		"REOLINK_CAMERA_0_HOST=192.168.1.10",
		"REOLINK_CAMERA_0_TIMEOUT=not-a-duration",
	})
	if err == nil {
		t.Fatal("expected parse error")
	}
	if !strings.Contains(err.Error(), "REOLINK_CAMERA_0_TIMEOUT") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestNormalizeHWAddress(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		in, want string
		wantErr  bool
	}{
		{in: "00:00:00:00:00:00", want: "00:00:00:00:00:00"},
		{in: "02:42:AC:11:00:02", want: "02:42:ac:11:00:02"},
		{in: "02-42-ac-11-00-02", want: "02:42:ac:11:00:02"},
		{in: "", wantErr: true},
		{in: "02:42:ac:11:00", wantErr: true},
		{in: "02:42:ac:11:00:02:03:04", wantErr: true},
		{in: "<tt:HwAddress>", wantErr: true},
	} {
		got, err := normalizeHWAddress(tc.in)
		if (err != nil) != tc.wantErr || got != tc.want {
			t.Errorf("normalizeHWAddress(%q) = %q, %v; want %q, err=%t", tc.in, got, err, tc.want, tc.wantErr)
		}
	}
}

func TestCameraPacerLatencyOverride(t *testing.T) {
	t.Parallel()

	camera := CameraConfig{Name: "front", Host: "cam", Stream: "main,sub", PacerLatencyMs: "300, sub:0"}
	if err := validateCameraConfig(&camera); err != nil {
		t.Fatalf("validate: %v", err)
	}
	if ms, ok := camera.pacerLatencyFor("SUB"); !ok || ms != 0 {
		t.Fatalf("sub override = %d, %t; want 0, true", ms, ok)
	}
	if ms, ok := camera.pacerLatencyFor("main"); !ok || ms != 300 {
		t.Fatalf("main override = %d, %t; want camera-wide 300, true", ms, ok)
	}
	if _, ok := (CameraConfig{Stream: "main"}).pacerLatencyFor("main"); ok {
		t.Fatal("expected no override when PACER_LATENCY_MS is unset")
	}

	for _, raw := range []string{"fast", "sub:-1", "sub:", "extern:200"} {
		bad := CameraConfig{Name: "front", Host: "cam", Stream: "main,sub", PacerLatencyMs: raw}
		if err := validateCameraConfig(&bad); err == nil {
			t.Errorf("PacerLatencyMs %q: expected validation error", raw)
		}
	}

	server := defaultConfig().Server.withPacerLatency(200)
	if server.VideoPacerInitialLatencyMs != 200 || server.AudioPacerInitialLatencyMs != 200 ||
		server.VideoPacerMaxLeadMs != 500 || server.AudioPacerMaxLeadMs != 500 {
		t.Fatalf("withPacerLatency(200) = %+v; want 200/200 latency, 500/500 lead", server)
	}
	if lead := defaultConfig().Server.withPacerLatency(2000).AudioPacerMaxLeadMs; lead <= 2000 {
		t.Fatalf("max lead %d must exceed the initial latency", lead)
	}
}
