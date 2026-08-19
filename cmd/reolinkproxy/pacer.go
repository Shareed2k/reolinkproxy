package main

import (
	"context"
	"math"
	"sync"
	"time"

	"github.com/bluenviron/gortsplib/v5/pkg/description"
	"github.com/pion/rtp"
)

// pacedFrame is one logical emission: one or more RTP packets sharing the same
// schedule slot (e.g. one AAC AU or one HEVC AU split across RTP fragments).
type pacedFrame struct {
	pkts     []*rtp.Packet
	media    *description.Media
	duration time.Duration
	// ntp is the wall-clock time of the frame's media timestamp, used for
	// RTCP Sender Reports (RTP<->NTP mapping). Zero means unknown.
	ntp time.Time
}

// ntpFromMicros converts an unwrapped camera timestamp (already anchored to
// the unix epoch in microseconds by timestampUnwrapper) to a wall-clock time.
func ntpFromMicros(us uint64) time.Time {
	if us == 0 || us > math.MaxInt64 {
		return time.Time{}
	}
	return time.UnixMicro(int64(us))
}

// videoPaceState tracks the previous frame's continuous PTS (microseconds) for
// computing per-AU pacer durations (ΔPTS → wall spacing).
type videoPaceState struct {
	prevUS  uint64
	hasPrev bool
}

// durationForFrame returns the wall duration between this frame and the previous
// emitted video AU. First frame and anomalies (non-increasing or Δ ≥ 5s) yield 0.
func (s *videoPaceState) durationForFrame(continuousUS uint64) time.Duration {
	const maxDeltaUS = 5_000_000 // 5s of media time; larger deltas treated as discontinuities
	if !s.hasPrev {
		s.hasPrev = true
		s.prevUS = continuousUS
		return 0
	}
	if continuousUS <= s.prevUS {
		s.prevUS = continuousUS
		return 0
	}
	delta := continuousUS - s.prevUS
	s.prevUS = continuousUS
	if delta >= maxDeltaUS {
		return 0
	}
	return time.Duration(delta) * time.Microsecond
}

// mediaPacer smooths bursty upstream media onto the wire using an absolute
// next-emission cursor and wall-clock-relative scheduling.
type mediaPacer struct {
	ch             chan pacedFrame
	maxLead        time.Duration
	initialLatency time.Duration
	snapOnPast     bool
	handler        *rtspStreamHandler

	overflowMu      sync.Mutex
	lastOverflowLog time.Time
}

// enqueue sends a paced frame to the pacer goroutine. If the channel is full,
// the frame is dropped and overflow is logged at most once per minute.
func (p *mediaPacer) enqueue(item pacedFrame) {
	select {
	case p.ch <- item:
	default:
		p.warnOverflowOnce()
	}
}

// warnOverflowOnce logs a queue-overflow warning, rate-limited to once per
// minute to avoid log spam under sustained overload.
func (p *mediaPacer) warnOverflowOnce() {
	p.overflowMu.Lock()
	defer p.overflowMu.Unlock()
	now := time.Now()
	if now.Sub(p.lastOverflowLog) < 60*time.Second {
		return
	}
	p.lastOverflowLog = now
	log.Warnf("media pacer queue overflow (cap=%d); dropping frame", cap(p.ch))
}

// run drains pacedFrame values from the channel until ctx is cancelled or the
// channel closes. It waits on an absolute next-emission time, re-anchors the
// schedule per maxLead / snapOnPast / initialLatency, then writes each RTP
// packet to the handler and advances the cursor by item.duration.
func (p *mediaPacer) run(ctx context.Context) {
	var nextEmitAt time.Time

	for {
		var item pacedFrame
		select {
		case <-ctx.Done():
			return
		case it, ok := <-p.ch:
			if !ok {
				return
			}
			item = it
		}

		now := time.Now()

		// Re-anchor:
		// 1. Cursor too far in the future → snap to now (burst / startup).
		// 2. Cursor in the past → snap to now only when snapOnPast (audio).
		//    Otherwise keep the past target so we burst-drain (video slope).
		var target time.Time
		switch {
		case !nextEmitAt.IsZero() && nextEmitAt.After(now.Add(p.maxLead)):
			target = now
		case !nextEmitAt.IsZero() && p.snapOnPast && nextEmitAt.Before(now):
			target = now
		case !nextEmitAt.IsZero():
			target = nextEmitAt
		default:
			target = now.Add(p.initialLatency)
		}

		if target.After(now) {
			delay := time.Until(target)
			timer := time.NewTimer(delay)
			select {
			case <-ctx.Done():
				if !timer.Stop() {
					<-timer.C
				}
				return
			case <-timer.C:
			}
		}

		for _, pkt := range item.pkts {
			if p.handler != nil && item.media != nil && pkt != nil {
				p.handler.writePacket(item.media, pkt, item.ntp)
			}
		}

		// Schedule absolutely from target, not time.Now(), so scheduler
		// jitter does not drift the RTP↔wall slope.
		nextEmitAt = addDurationClampOverflow(target, item.duration)
	}
}

// addDurationClampOverflow returns t+d, or t if d is non-positive or adding d
// would overflow time.Time (in which case After would be false).
func addDurationClampOverflow(t time.Time, d time.Duration) time.Time {
	if d <= 0 {
		return t
	}
	out := t.Add(d)
	if !out.After(t) {
		return t
	}
	return out
}
