package main

import (
	"math"
	"testing"
	"time"
)

func TestNTPFromMicros(t *testing.T) {
	t.Parallel()

	now := time.Now()
	us := uint64(now.UnixMicro())
	got := ntpFromMicros(us)
	if got.UnixMicro() != now.UnixMicro() {
		t.Fatalf("ntpFromMicros(%d) = %v, want %v", us, got, now)
	}

	if !ntpFromMicros(0).IsZero() {
		t.Fatal("zero micros must map to zero time (no NTP)")
	}
	if !ntpFromMicros(math.MaxInt64 + 1).IsZero() {
		t.Fatal("overflowing micros must map to zero time")
	}
}

// Video and audio share one timestampUnwrapper per stream, so NTP values
// derived from the same camera timestamp must agree across both paths.
func TestNTPSharedClockAgreement(t *testing.T) {
	t.Parallel()

	var unwrapper timestampUnwrapper
	unwrapper.nowUnixMicro = func() int64 { return 1_000_000_000 }

	videoUS := unwrapper.unwrap(5_000_000)
	audioUS := unwrapper.unwrap(5_000_000)
	if ntpFromMicros(videoUS) != ntpFromMicros(audioUS) {
		t.Fatalf("video ntp %v != audio ntp %v for the same camera timestamp", ntpFromMicros(videoUS), ntpFromMicros(audioUS))
	}
}
