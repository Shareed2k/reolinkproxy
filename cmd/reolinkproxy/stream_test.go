package main

import (
	"bytes"
	"encoding/binary"
	"net/http/httptest"
	"testing"

	"github.com/bluenviron/mediacommon/v2/pkg/codecs/mpeg4audio"
	"github.com/pion/rtp"

	"github.com/shareed2k/reolinkproxy/pkg/baichuan"
	"github.com/shareed2k/reolinkproxy/pkg/media"
)

func TestReorderH265NALsForAccessUnit(t *testing.T) {
	t.Parallel()

	vps := []byte{0x40, 0x01, 0xaa}
	sei := []byte{0x4E, 0x01, 0xbb}
	slice := []byte{0x26, 0x01, 0xcc}

	// Camera order: slice first, then VPS/SEI — reorder should move non-VCL ahead of VCL.
	got := media.ReorderH265NALsForAccessUnit([][]byte{slice, vps, sei})
	if len(got) != 3 {
		t.Fatalf("len = %d, want 3", len(got))
	}
	if !bytes.Equal(got[0], vps) || !bytes.Equal(got[1], sei) || !bytes.Equal(got[2], slice) {
		t.Fatalf("order = %x %x %x, want vps sei slice", got[0], got[1], got[2])
	}

	// Already-correct order is unchanged.
	unchanged := media.ReorderH265NALsForAccessUnit([][]byte{vps, sei, slice})
	want := [][]byte{vps, sei, slice}
	for i := range want {
		if i >= len(unchanged) || !bytes.Equal(unchanged[i], want[i]) {
			t.Fatalf("identity reorder at %d: got %d slices", i, len(unchanged))
		}
	}
}

func TestFixH265AggregationTemporalID(t *testing.T) {
	t.Parallel()

	firstNALU := []byte{0x40, 0x01, 0xaa, 0xbb}
	payload := make([]byte, 2+2+len(firstNALU))
	payload[0] = 48 << 1
	payload[1] = 0x00
	binary.BigEndian.PutUint16(payload[2:4], uint16(len(firstNALU)))
	copy(payload[4:], firstNALU)

	pkt := &rtp.Packet{Payload: payload}
	media.FixH265AggregationTemporalID([]*rtp.Packet{pkt})

	if got, want := pkt.Payload[0], (firstNALU[0]&0x81)|(48<<1); got != want {
		t.Fatalf("payload[0] = %#x, want %#x", got, want)
	}
	if got, want := pkt.Payload[1], firstNALU[1]; got != want {
		t.Fatalf("payload[1] = %#x, want %#x", got, want)
	}
}

func TestParseAACAccessUnits(t *testing.T) {
	t.Parallel()

	raw, err := mpeg4audio.ADTSPackets{
		&mpeg4audio.ADTSPacket{
			Type:         mpeg4audio.ObjectTypeAACLC,
			SampleRate:   16000,
			ChannelCount: 1,
			AU:           []byte{0x11, 0x22, 0x33},
		},
		&mpeg4audio.ADTSPacket{
			Type:         mpeg4audio.ObjectTypeAACLC,
			SampleRate:   16000,
			ChannelCount: 1,
			AU:           []byte{0x44, 0x55},
		},
	}.Marshal()
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}

	aus, cfg, err := media.ParseAACAccessUnits(raw)
	if err != nil {
		t.Fatalf("media.ParseAACAccessUnits() error = %v", err)
	}
	if got, want := len(aus), 2; got != want {
		t.Fatalf("len(aus) = %d, want %d", got, want)
	}
	if got, want := cfg.SampleRate, 16000; got != want {
		t.Fatalf("cfg.SampleRate = %d, want %d", got, want)
	}
	if got, want := int(cfg.ChannelConfig), 1; got != want {
		t.Fatalf("cfg.ChannelCount = %d, want %d", got, want)
	}
}

func TestTimestampUnwrapperWrapsForward(t *testing.T) {
	t.Parallel()

	var timestamps timestampUnwrapper
	timestamps.nowUnixMicro = func() int64 { return 0xfffffff0 }
	if got, want := timestamps.unwrap(0xfffffff0), uint64(0xfffffff0); got != want {
		t.Fatalf("first unwrap = %d, want %d", got, want)
	}
	if got, want := timestamps.unwrap(20), uint64(0x100000014); got != want {
		t.Fatalf("wrapped unwrap = %d, want %d", got, want)
	}
}

func TestRTPTimestampGuardClampsBackwardJitter(t *testing.T) {
	t.Parallel()

	var timestamps rtpTimestampGuard
	if got, want := timestamps.next(1_492_479), uint32(1_492_479); got != want {
		t.Fatalf("first next = %d, want %d", got, want)
	}
	if got, want := timestamps.next(1_487_732), uint32(1_492_480); got != want {
		t.Fatalf("backward next = %d, want %d", got, want)
	}
	if got, want := timestamps.next(1_492_480), uint32(1_492_481); got != want {
		t.Fatalf("equal next = %d, want %d", got, want)
	}
}

func TestRTPTimestampGuardAllowsForwardWrap(t *testing.T) {
	t.Parallel()

	var timestamps rtpTimestampGuard
	if got, want := timestamps.next(0xfffffff0), uint32(0xfffffff0); got != want {
		t.Fatalf("first next = %d, want %d", got, want)
	}
	if got, want := timestamps.next(20), uint32(20); got != want {
		t.Fatalf("wrapped next = %d, want %d", got, want)
	}
}

func TestRTPTimestampGuardClampsAudioRangeStart(t *testing.T) {
	t.Parallel()

	var timestamps rtpTimestampGuard
	pkts := []*rtp.Packet{{Header: rtp.Header{Timestamp: 0}}}

	if got, want := timestamps.applyBaseToPackets(pkts, 190_464, 1024), uint32(190_464); got != want {
		t.Fatalf("first base = %d, want %d", got, want)
	}
	if got, want := timestamps.applyBaseToPackets(pkts, 191_475, 1024), uint32(191_488); got != want {
		t.Fatalf("backward base = %d, want %d", got, want)
	}
	if got, want := timestamps.applyBaseToPackets(pkts, 192_512, 1024), uint32(192_512); got != want {
		t.Fatalf("equal-to-end base = %d, want %d", got, want)
	}
}

func TestRTPTimestampGuardShiftsAudioPacketBatch(t *testing.T) {
	t.Parallel()

	var timestamps rtpTimestampGuard
	pkts := []*rtp.Packet{
		{Header: rtp.Header{Timestamp: 0}},
		{Header: rtp.Header{Timestamp: 1024}},
	}

	if got, want := timestamps.applyBaseToPackets(pkts, 1000, 2048), uint32(1000); got != want {
		t.Fatalf("first base = %d, want %d", got, want)
	}
	if got, want := timestamps.applyBaseToPackets(pkts, 2000, 2048), uint32(3048); got != want {
		t.Fatalf("shifted base = %d, want %d", got, want)
	}
}

func TestAudioTimestampForPacketIgnoresFallbackWhenPacketHasNoTimestamp(t *testing.T) {
	t.Parallel()

	var streamTimestamps timestampUnwrapper

	got := audioTimestampForPacket(baichuan.MediaPacket{Kind: baichuan.MediaPacketAAC}, &streamTimestamps)
	want := mediaTimestamp{}
	if got != want {
		t.Fatalf("audioTimestampForPacket() = %+v, want %+v", got, want)
	}
	if streamTimestamps.highest != 0 {
		t.Fatalf("streamTimestamps.highest = %d, want 0", streamTimestamps.highest)
	}
}

func TestAudioTimestampForPacketUsesAuthoritativePacketTimestamp(t *testing.T) {
	t.Parallel()

	var streamTimestamps timestampUnwrapper
	streamTimestamps.nowUnixMicro = func() int64 { return 1234 }
	packet := baichuan.MediaPacket{
		Kind:               baichuan.MediaPacketAAC,
		TimestampMicrosecs: 1234,
		HasTimestamp:       true,
	}

	got := audioTimestampForPacket(packet, &streamTimestamps)
	want := mediaTimestamp{
		Microseconds:  1234,
		Valid:         true,
		Authoritative: true,
	}
	if got != want {
		t.Fatalf("audioTimestampForPacket() = %+v, want %+v", got, want)
	}
	if streamTimestamps.highest != 1234 {
		t.Fatalf("streamTimestamps.highest = %d, want 1234", streamTimestamps.highest)
	}
}

func TestSOAPActionPrefersExactElement(t *testing.T) {
	t.Parallel()

	req := httptest.NewRequest("POST", "http://example.test/onvif/media_service", nil)
	body := `<soap:Envelope xmlns:soap="http://www.w3.org/2003/05/soap-envelope" xmlns:trt="http://www.onvif.org/ver10/media/wsdl"><soap:Body><trt:GetProfiles/></soap:Body></soap:Envelope>`

	got := soapAction(req, body, []string{"GetProfile", "GetProfiles"})
	if got != "GetProfiles" {
		t.Fatalf("soapAction() = %q, want %q", got, "GetProfiles")
	}
}

func TestCoalesceRejectsTruncatedNALUs(t *testing.T) {
	t.Parallel()

	fallback := []byte{0x67, 0x42, 0x00, 0x1f}
	
	// Test nil
	if got := coalesce(nil, fallback); !bytes.Equal(got, fallback) {
		t.Errorf("coalesce(nil) = %x, want %x", got, fallback)
	}

	// Test 1-byte truncated NALU
	truncated := []byte{0x67}
	if got := coalesce(truncated, fallback); !bytes.Equal(got, fallback) {
		t.Errorf("coalesce(truncated) = %x, want %x", got, fallback)
	}

	// Test valid NALU
	valid := []byte{0x67, 0x42, 0x00, 0x20}
	if got := coalesce(valid, fallback); !bytes.Equal(got, valid) {
		t.Errorf("coalesce(valid) = %x, want %x", got, valid)
	}
}
