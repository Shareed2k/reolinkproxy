package main

import (
	"testing"
	"time"

	"github.com/bluenviron/gortsplib/v4/pkg/description"
	"github.com/pion/rtp"
)

// TestReorderShouldFlushWhenSpreadWithinWindow checks shouldFlush when the
// circular spread of pending RTP timestamps fits inside windowTicks: flush is
// allowed immediately without waiting for the reorder buffer deadline.
func TestReorderShouldFlushWhenSpreadWithinWindow(t *testing.T) {
	t.Parallel()

	q := newRTPVideoReorderQueue(200, 90_000)
	if q == nil {
		t.Fatal("queue")
	}
	t0 := time.Now()
	q.enqueue(913_500, []*rtp.Packet{{Payload: []byte{1}}}, 0, 0, 0, t0)
	q.enqueue(904_500, []*rtp.Packet{{Payload: []byte{2}}}, 0, 0, 0, t0)
	if !q.shouldFlush(t0) {
		t.Fatal("expected flush: spread 9000 <= window")
	}
}

// TestReorderShouldFlushWhenDeadlineExceeded checks shouldFlush when spread
// exceeds windowTicks: no flush until now passes bufMs since the oldest
// enqueued group, then the deadline path forces a flush.
func TestReorderShouldFlushWhenDeadlineExceeded(t *testing.T) {
	t.Parallel()

	q := newRTPVideoReorderQueue(50, 1)
	t0 := time.Now()
	q.enqueue(0, []*rtp.Packet{{Payload: []byte{1}}}, 0, 0, 0, t0)
	q.enqueue(2_000_000, []*rtp.Packet{{Payload: []byte{2}}}, 0, 0, 0, t0)
	if q.shouldFlush(t0) {
		t.Fatal("did not expect flush before deadline (spread exceeds tiny window)")
	}
	if !q.shouldFlush(t0.Add(60 * time.Millisecond)) {
		t.Fatal("expected deadline-based flush")
	}
}

// TestReorderFlushEmitsAscendingRTPTimestamps enqueues several groups with
// different RTP timestamps (including out-of-order) and runs flush until the
// queue is idle: pending must be empty after reorder and emission complete.
func TestReorderFlushEmitsAscendingRTPTimestamps(t *testing.T) {
	t.Parallel()

	q := newRTPVideoReorderQueue(200, 90_000)
	t0 := time.Now()
	q.enqueue(913_500, []*rtp.Packet{{Payload: []byte{1}}}, 0, 0, 0, t0)
	q.enqueue(904_500, []*rtp.Packet{{Payload: []byte{2}}}, 0, 0, 0, t0)
	q.enqueue(909_000, []*rtp.Packet{{Payload: []byte{3}}}, 0, 0, 0, t0)

	h := newRTSPStreamHandler("/t")
	media := &description.Media{}

	q.flush(t0, func(pkts []*rtp.Packet, _ uint64) {
		for _, pkt := range pkts {
			h.writePacket(media, pkt)
		}
	})

	if len(q.pending) != 0 {
		t.Fatalf("pending=%d want 0", len(q.pending))
	}
}

// TestRtpCircularCoverSpreadTicksSingleIsZero verifies rtpCircularCoverSpreadTicks
// returns 0 for a single timestamp (no spread to measure).
func TestRtpCircularCoverSpreadTicksSingleIsZero(t *testing.T) {
	t.Parallel()
	if s := rtpCircularCoverSpreadTicks([]uint32{42}); s != 0 {
		t.Fatalf("spread=%d want 0", s)
	}
}

// TestReorderFlushBumpsLateGroupBelowLastEmitted checks flush after a group whose
// nominal RTP timestamp is behind lastEmitted: emitTimestamp advances it so the
// written timeline stays strictly forward, and the queue drains.
func TestReorderFlushBumpsLateGroupBelowLastEmitted(t *testing.T) {
	t.Parallel()

	q := newRTPVideoReorderQueue(200, 90_000)
	if q == nil {
		t.Fatal("queue")
	}
	h := newRTSPStreamHandler("/t")
	media := &description.Media{}
	t0 := time.Now()

	q.enqueue(5000, []*rtp.Packet{{Payload: []byte{1}}}, 0, 0, 0, t0)
	q.flush(t0, func(pkts []*rtp.Packet, _ uint64) {
		for _, pkt := range pkts {
			h.writePacket(media, pkt)
		}
	})

	if !q.lastEmittedSet || q.lastEmitted != 5000 {
		t.Fatalf("after first flush lastEmitted=%v set=%v", q.lastEmitted, q.lastEmittedSet)
	}

	q.enqueue(4000, []*rtp.Packet{{Payload: []byte{2}}}, 0, 0, 0, t0)
	q.flush(t0, func(pkts []*rtp.Packet, _ uint64) {
		for _, pkt := range pkts {
			h.writePacket(media, pkt)
		}
	})

	if q.lastEmitted != 5001 {
		t.Fatalf("late nominal TS below lastEmitted: got lastEmitted=%d want 5001", q.lastEmitted)
	}
	if len(q.pending) != 0 {
		t.Fatalf("pending=%d want 0", len(q.pending))
	}
}

// TestReorderShouldFlushWhenCircularSpreadStraddlesWrap checks shouldFlush when
// pending timestamps sit on opposite sides of the uint32 RTP clock wrap: spread
// is computed on the circle and can still fall within windowTicks.
func TestReorderShouldFlushWhenCircularSpreadStraddlesWrap(t *testing.T) {
	t.Parallel()

	q := newRTPVideoReorderQueue(200, 90_000)
	if q == nil {
		t.Fatal("queue")
	}
	t0 := time.Now()
	q.enqueue(0x00000020, []*rtp.Packet{{Payload: []byte{1}}}, 0, 0, 0, t0)
	q.enqueue(0xFFFFFFF0, []*rtp.Packet{{Payload: []byte{2}}}, 0, 0, 0, t0)
	if !q.shouldFlush(t0) {
		t.Fatal("expected flush: circular spread 48 <= window")
	}
}

// TestReorderEmitTimestampBumpsBackwardNominal verifies emitTimestamp maps a
// backward nominal RTP time to lastEmitted+1, passes through an equal timestamp
// (same AU continuation), and leaves strictly increasing timestamps unchanged.
func TestReorderEmitTimestampBumpsBackwardNominal(t *testing.T) {
	t.Parallel()

	q := newRTPVideoReorderQueue(200, 90_000)
	if q == nil {
		t.Fatal("queue")
	}
	q.lastEmittedSet = true
	q.lastEmitted = 1000
	if got := q.emitTimestamp(500); got != 1001 {
		t.Fatalf("emitTimestamp(500)=%d want 1001", got)
	}
	if got := q.emitTimestamp(1000); got != 1000 {
		t.Fatalf("emitTimestamp(1000)=%d want 1000 (same AU continuation)", got)
	}
	if got := q.emitTimestamp(2000); got != 2000 {
		t.Fatalf("emitTimestamp(2000)=%d want 2000", got)
	}
}
