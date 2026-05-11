package main

import (
	"slices"
	"time"

	"github.com/bluenviron/gortsplib/v4/pkg/description"
	"github.com/pion/rtp"
)

// queuedVideoGroup is one Baichuan video frame worth of RTP (same RTP timestamp).
type queuedVideoGroup struct {
	rtpTS        uint32
	arrival      uint64
	enqueuedAt   time.Time
	continuousUS uint64
	rawCameraUS  uint64
	unwrappedUS  uint64
	packets      []*rtp.Packet
}

// rtpVideoReorderQueue reorders H264/H265 RTP by camera RTP timestamp before writePacket,
// bounded by a spread window and a max hold time (see plan todo 5).
type rtpVideoReorderQueue struct {
	bufMs       time.Duration
	windowTicks uint32
	nextArrival uint64
	pending     []queuedVideoGroup
	// lastEmitted is the last RTP timestamp written for this queue (send order).
	lastEmitted    uint32
	lastEmittedSet bool
}

// newRTPVideoReorderQueue returns a reorder queue with max hold time bufMs and
// spread threshold windowTicks (RTP clock units), or nil if bufMs is non-positive.
func newRTPVideoReorderQueue(bufMs int, windowTicks uint32) *rtpVideoReorderQueue {
	if bufMs <= 0 {
		return nil
	}
	if windowTicks == 0 {
		windowTicks = 90_000
	}
	return &rtpVideoReorderQueue{
		bufMs:       time.Duration(bufMs) * time.Millisecond,
		windowTicks: windowTicks,
	}
}

// reset clears pending frames and emission state so a new stream can start clean.
func (q *rtpVideoReorderQueue) reset() {
	if q == nil {
		return
	}
	q.pending = q.pending[:0]
	q.nextArrival = 0
	q.lastEmitted = 0
	q.lastEmittedSet = false
}

// uniqueSortedRTPTicks returns sorted unique RTP timestamps from pending groups (numeric order).
func uniqueSortedRTPTicks(pending []queuedVideoGroup) []uint32 {
	seen := make(map[uint32]struct{}, len(pending))
	for _, g := range pending {
		seen[g.rtpTS] = struct{}{}
	}
	out := make([]uint32, 0, len(seen))
	for ts := range seen {
		out = append(out, ts)
	}
	slices.SortFunc(out, func(a, b uint32) int {
		if a < b {
			return -1
		}
		if a > b {
			return 1
		}
		return 0
	})
	return out
}

// rtpCircularCoverSpreadTicks is the minimum circular arc length (in RTP clock ticks)
// that contains every timestamp in sortedUnique (ascending uint32 sort).
func rtpCircularCoverSpreadTicks(sortedUnique []uint32) uint32 {
	n := len(sortedUnique)
	if n <= 1 {
		return 0
	}
	var maxGap uint32
	for i := 0; i < n-1; i++ {
		gap := sortedUnique[i+1] - sortedUnique[i]
		if gap > maxGap {
			maxGap = gap
		}
	}
	wrapGap := sortedUnique[0] - sortedUnique[n-1]
	if wrapGap > maxGap {
		maxGap = wrapGap
	}
	return 0 - maxGap //#nosec G115 — uint32 wrap is 2^32 - maxGap
}

// pendingCircularSpreadTicks is the circular spread of distinct RTP timestamps
// currently buffered (how scattered pending frames are on the RTP clock ring).
func (q *rtpVideoReorderQueue) pendingCircularSpreadTicks() uint32 {
	u := uniqueSortedRTPTicks(q.pending)
	return rtpCircularCoverSpreadTicks(u)
}

// emitTimestamp maps a group's nominal RTP time to a strictly forward timeline vs
// prior emissions so writePacket's final clamp rarely has to repair large backward jumps.
func (q *rtpVideoReorderQueue) emitTimestamp(rtpTS uint32) uint32 {
	if !q.lastEmittedSet {
		return rtpTS
	}
	if rtpTimestampAfter(rtpTS, q.lastEmitted) {
		return rtpTS
	}
	if rtpTS == q.lastEmitted {
		return rtpTS
	}
	return q.lastEmitted + 1 //#nosec G115
}

// cloneRTPPackets returns a shallow copy of the slice with deep-copied Payload
// bytes so queued packets remain stable if the original buffers are reused.
func cloneRTPPackets(pkts []*rtp.Packet) []*rtp.Packet {
	out := make([]*rtp.Packet, len(pkts))
	for i, p := range pkts {
		if p == nil {
			continue
		}
		cp := *p
		if len(p.Payload) > 0 {
			cp.Payload = append([]byte(nil), p.Payload...)
		}
		out[i] = &cp
	}
	return out
}

// enqueue adds one video access unit (same RTP timestamp) to the pending buffer,
// cloning packets and sorting pending groups by RTP time then arrival order.
func (q *rtpVideoReorderQueue) enqueue(
	rtpTS uint32,
	pkts []*rtp.Packet,
	continuousUS uint64,
	rawCameraUS uint64,
	unwrappedUS uint64,
	now time.Time,
) {
	if q == nil || len(pkts) == 0 {
		return
	}
	q.nextArrival++
	g := queuedVideoGroup{
		rtpTS:        rtpTS,
		arrival:      q.nextArrival,
		enqueuedAt:   now,
		continuousUS: continuousUS,
		rawCameraUS:  rawCameraUS,
		unwrappedUS:  unwrappedUS,
		packets:      cloneRTPPackets(pkts),
	}
	q.pending = append(q.pending, g)
	slices.SortFunc(q.pending, func(a, b queuedVideoGroup) int {
		if a.rtpTS == b.rtpTS {
			if a.arrival < b.arrival {
				return -1
			}
			if a.arrival > b.arrival {
				return 1
			}
			return 0
		}
		if rtpTimestampBefore(a.rtpTS, b.rtpTS) {
			return -1
		}
		if rtpTimestampAfter(a.rtpTS, b.rtpTS) {
			return 1
		}
		return 0
	})
}

// oldestEnqueueTime is the earliest enqueuedAt among pending groups (for max-hold timeout).
func oldestEnqueueTime(pending []queuedVideoGroup) time.Time {
	t := pending[0].enqueuedAt
	for i := 1; i < len(pending); i++ {
		if pending[i].enqueuedAt.Before(t) {
			t = pending[i].enqueuedAt
		}
	}
	return t
}

// shouldFlush is true when buffered timestamps are within the spread window, or
// when the oldest pending frame has been held at least bufMs (force emit).
func (q *rtpVideoReorderQueue) shouldFlush(now time.Time) bool {
	if len(q.pending) == 0 {
		return false
	}
	spread := q.pendingCircularSpreadTicks()
	if spread <= q.windowTicks {
		return true
	}
	return now.Sub(oldestEnqueueTime(q.pending)) >= q.bufMs
}

// minRTP is the pending RTP timestamp that is earliest on the circular clock
// (the next frame to emit when flushing in timestamp order).
func (q *rtpVideoReorderQueue) minRTP() uint32 {
	m := q.pending[0].rtpTS
	for _, g := range q.pending[1:] {
		if rtpTimestampBefore(g.rtpTS, m) {
			m = g.rtpTS
		}
	}
	return m
}

// removeAndReturnWithRTP removes all pending groups with RTP timestamp ts from the
// queue and returns them sorted by arrival order (fragments reassembled in receive order).
func (q *rtpVideoReorderQueue) removeAndReturnWithRTP(ts uint32) []queuedVideoGroup {
	var match []queuedVideoGroup
	rest := q.pending[:0]
	for _, g := range q.pending {
		if g.rtpTS == ts {
			match = append(match, g)
		} else {
			rest = append(rest, g)
		}
	}
	slices.SortFunc(match, func(a, b queuedVideoGroup) int {
		if a.arrival < b.arrival {
			return -1
		}
		if a.arrival > b.arrival {
			return 1
		}
		return 0
	})
	q.pending = rest
	return match
}

// flush repeatedly emits the earliest pending RTP timestamp while shouldFlush holds,
// rewriting timestamps via emitTimestamp and forwarding each packet to writePacket.
func (q *rtpVideoReorderQueue) flush(
	now time.Time,
	handler *rtspStreamHandler,
	videoMedia *description.Media,
) {
	if q == nil {
		return
	}
	for q.shouldFlush(now) {
		if len(q.pending) == 0 {
			return
		}
		Tmin := q.minRTP()
		batch := q.removeAndReturnWithRTP(Tmin)
		emitTS := q.emitTimestamp(Tmin)
		for i := range batch {
			g := &batch[i]
			for _, pkt := range g.packets {
				if pkt == nil {
					continue
				}
				pkt.Timestamp = emitTS
				handler.writePacket(videoMedia, pkt)
			}
		}
		q.lastEmitted = emitTS
		q.lastEmittedSet = true
	}
}
