package main

import (
	"testing"
	"time"

	"github.com/bluenviron/gortsplib/v5"
)

func TestStreamPauseConfigShouldPauseOnClient(t *testing.T) {
	t.Parallel()

	handler := newRTSPStreamHandler("front")
	paused, reason := (streamPauseConfig{OnClient: true}).shouldPause(time.Now(), handler)
	if !paused {
		t.Fatal("expected stream to pause without clients")
	}
	if reason != "no rtsp client" {
		t.Fatalf("unexpected pause reason: %q", reason)
	}
}

func TestStreamPauseConfigShouldPauseOnMotionAfterTimeout(t *testing.T) {
	t.Parallel()

	motion := newCameraMotionState()
	motion.setActive(false)

	motion.mu.Lock()
	motion.snapshot.ChangedAt = time.Now().Add(-2 * time.Second)
	motion.mu.Unlock()

	paused, reason := (streamPauseConfig{
		OnMotion: true,
		Timeout:  time.Second,
		Motion:   motion,
	}).shouldPause(time.Now(), nil)
	if !paused {
		t.Fatal("expected stream to pause without motion after timeout")
	}
	if reason != "no motion" {
		t.Fatalf("unexpected pause reason: %q", reason)
	}
}

func TestStreamPauseConfigDoesNotPauseOnUnknownMotion(t *testing.T) {
	t.Parallel()

	motion := newCameraMotionState()
	paused, _ := (streamPauseConfig{
		OnMotion: true,
		Timeout:  time.Second,
		Motion:   motion,
	}).shouldPause(time.Now(), nil)
	if paused {
		t.Fatal("expected stream to remain active until motion state is known")
	}
}

func TestStreamPauseConfigPreviewWanted(t *testing.T) {
	t.Parallel()

	if (streamPauseConfig{}).previewWanted(newRTSPStreamHandler("front")) != nil {
		t.Fatal("expected nil predicate without idle disconnect")
	}

	handler := newRTSPStreamHandler("front")
	want := (streamPauseConfig{IdleDisconnect: true}).previewWanted(handler)
	if !want() {
		t.Fatal("expected preview until the rtsp stream is ready")
	}

	handler.stream = &gortsplib.ServerStream{}
	if want() {
		t.Fatal("expected preview to stop once ready and idle for IdleTimeout")
	}

	handler.clients[&gortsplib.ServerSession{}] = struct{}{}
	if !want() {
		t.Fatal("expected preview to resume when a client attaches")
	}

	wantLong := (streamPauseConfig{IdleDisconnect: true, IdleTimeout: time.Hour}).previewWanted(newRTSPStreamHandler("side"))
	if !wantLong() {
		t.Fatal("expected preview to keep running within IdleTimeout")
	}
}
