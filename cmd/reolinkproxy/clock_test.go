package main

import (
	"testing"
	"time"
)

func TestTimestampUnwrapperReanchorsDiscontinuities(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name string
		next uint32
	}{
		{"counter jumps forward", 1_800_000_000},
		{"counter resets on reconnect", 1_000},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			now := int64(1_700_000_000_000_000)
			clock := timestampUnwrapper{nowUnixMicro: func() int64 { return now }}
			clock.unwrap(1_000_000_000)
			now += 40_000
			videoNTP := ntpFromMicros(clock.unwrap(tc.next))
			if !videoNTP.Equal(time.UnixMicro(now)) {
				t.Fatalf("video NTP = %v, want current wall time %v", videoNTP, time.UnixMicro(now))
			}
			now += 40_000
			if got := clock.unwrap(tc.next + 40_000); got != uint64(now) {
				t.Fatalf("next frame time = %d, want %d", got, now)
			}
		})
	}
}

func TestTimestampUnwrapperBoundsSustainedDrift(t *testing.T) {
	t.Parallel()

	for _, step := range []uint32{39_800, 40_200} {
		t.Run((time.Duration(step) * time.Microsecond).String(), func(t *testing.T) {
			t.Parallel()
			now := int64(1_700_000_000_000_000)
			clock := timestampUnwrapper{nowUnixMicro: func() int64 { return now }}
			// Cross multiple 32-bit camera-counter wraps while accumulating
			// eighty seconds of source-clock drift in either direction.
			source := uint32(0xffff0000)
			clock.unwrap(source)
			for frame := range 400_000 {
				now += 40_000
				source += step
				videoNTP := ntpFromMicros(clock.unwrap(source))
				drift := videoNTP.Sub(time.UnixMicro(now))
				// A slow clock may lag up to a second before the late-hold
				// timer starts, plus drift accumulated during the five-second
				// hold; a fast clock re-anchors as soon as it runs ahead.
				if drift < -1100*time.Millisecond || drift > time.Second {
					t.Fatalf("frame %d: video clock drift = %v", frame, drift)
				}
			}
		})
	}
}

func TestTimestampUnwrapperToleratesDeliveryBurst(t *testing.T) {
	t.Parallel()

	now := int64(1_700_000_000_000_000)
	clock := timestampUnwrapper{nowUnixMicro: func() int64 { return now }}
	src := uint32(1_000_000)
	prev := clock.unwrap(src)
	step := func(arrivalUS int64) {
		now += arrivalUS
		src += 40_000
		got := clock.unwrap(src)
		if got != prev+40_000 {
			t.Fatalf("frame at src %d = %d, want camera-paced %d", src, got, prev+40_000)
		}
		prev = got
	}
	for range 100 {
		step(40_000)
	}
	now += 37 * 40_000 // 1.5 s delivery stall
	for range 37 {
		step(1_000) // backlog drains as a burst
	}
	for range 100 {
		step(40_000)
	}
}

func TestTimestampUnwrapperPreservesSmallJitter(t *testing.T) {
	t.Parallel()

	now := int64(1_700_000_000_000_000)
	clock := timestampUnwrapper{nowUnixMicro: func() int64 { return now }}
	clock.unwrap(1_000_000)
	now += 40_000
	if got, want := clock.unwrap(1_030_000), uint64(now-10_000); got != want {
		t.Fatalf("jittered frame time = %d, want source time %d", got, want)
	}
	now += 40_000
	if got, want := clock.unwrap(1_080_000), uint64(now); got != want {
		t.Fatalf("recovered frame time = %d, want %d", got, want)
	}
}
