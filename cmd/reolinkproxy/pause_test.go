package main

import (
	"testing"
	"time"
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

func TestStreamLifecycleConfigMaxReconnectDelayBattery(t *testing.T) {
	t.Parallel()

	if got, want := (streamLifecycleConfig{BatteryCamera: true}).maxReconnectDelay(), time.Hour; got != want {
		t.Fatalf("maxReconnectDelay() = %v, want %v", got, want)
	}
}
