// Package media provides bitstream parsing and translation for media payloads.
package media

import (
	"encoding/binary"
	"fmt"

	"github.com/bluenviron/mediacommon/v2/pkg/codecs/mpeg4audio"
	"github.com/pion/rtp"
)

// ParseAACAccessUnits unmarshals ADTS packets from the raw payload.
func ParseAACAccessUnits(data []byte) ([][]byte, *mpeg4audio.AudioSpecificConfig, error) {
	var packets mpeg4audio.ADTSPackets
	if err := packets.Unmarshal(data); err != nil {
		return nil, nil, err
	}
	if len(packets) == 0 {
		return nil, nil, fmt.Errorf("empty ADTS packet set")
	}

	first := packets[0]
	cfg := &mpeg4audio.AudioSpecificConfig{
		Type:          first.Type,
		SampleRate:    first.SampleRate,
		ChannelConfig: first.ChannelConfig,
	}

	aus := make([][]byte, 0, len(packets))
	for _, pkt := range packets {
		if pkt.Type != cfg.Type || pkt.SampleRate != cfg.SampleRate || pkt.ChannelConfig != cfg.ChannelConfig {
			return nil, nil, fmt.Errorf("mixed AAC configuration inside one payload")
		}
		aus = append(aus, cloneBytes(pkt.AU))
	}

	return aus, cfg, nil
}

// SplitAnnexB extracts NAL units from an Annex-B byte stream.
func SplitAnnexB(buf []byte) [][]byte {
	var out [][]byte
	var start int
	var found bool

	for i := 0; i < len(buf)-3; i++ {
		prefixLen := startCodeLen(buf[i:])
		if prefixLen == 0 {
			continue
		}

		if found && i > start {
			out = append(out, cloneBytes(buf[start:i]))
		}
		start = i + prefixLen
		found = true
		i += prefixLen - 1
	}

	if found && start < len(buf) {
		out = append(out, cloneBytes(buf[start:]))
	}

	if len(out) == 0 && len(buf) > 0 {
		out = append(out, cloneBytes(buf))
	}

	trimmed := out[:0]
	for _, nalu := range out {
		if len(nalu) > 0 {
			trimmed = append(trimmed, nalu)
		}
	}
	return trimmed
}

func startCodeLen(buf []byte) int {
	if len(buf) >= 4 && buf[0] == 0 && buf[1] == 0 && buf[2] == 0 && buf[3] == 1 {
		return 4
	}
	if len(buf) >= 3 && buf[0] == 0 && buf[1] == 0 && buf[2] == 1 {
		return 3
	}
	return 0
}

// FilterH265DecodableNALs drops HEVC NAL units that common RTP/FFmpeg stacks reject:
// types 48–63 (aggregation, unspecified-including Reolink UNSPEC62), and non-base layer NALs.
func FilterH265DecodableNALs(nalus [][]byte) [][]byte {
	out := nalus[:0]
	for _, n := range nalus {
		if len(n) < 2 {
			continue
		}
		t := (n[0] >> 1) & 0x3F
		if t >= 48 {
			continue
		}
		layerID := ((n[0] & 1) << 5) | (n[1] >> 3)
		if layerID != 0 {
			continue
		}
		out = append(out, n)
	}
	return out
}

// h265NALUnitType returns the HEVC nal_unit_type from the first header byte.
func h265NALUnitType(header0 byte) byte {
	return (header0 >> 1) & 0x3F
}

// h265IsSliceNAL reports whether typ is a VCL slice NAL type (HEVC types 0–9, 16–21).
func h265IsSliceNAL(typ byte) bool {
	return typ <= 9 || (typ >= 16 && typ <= 21)
}

// ReorderH265NALsForAccessUnit places non-VCL NALs (parameter sets, SEI, AUD, …) before
// VCL slice NALs so the RTP packetizer's marker bit lands on the last slice of the AU.
func ReorderH265NALsForAccessUnit(nalus [][]byte) [][]byte {
	var nonSlice, slice [][]byte
	for _, n := range nalus {
		if len(n) < 2 {
			continue
		}
		if h265IsSliceNAL(h265NALUnitType(n[0])) {
			slice = append(slice, n)
		} else {
			nonSlice = append(nonSlice, n)
		}
	}
	return append(nonSlice, slice...)
}

// ExtractH264Params extracts the sequence and picture parameter sets from H264 NAL units.
func ExtractH264Params(nalus [][]byte) ([]byte, []byte) {
	var sps []byte
	var pps []byte

	for _, nalu := range nalus {
		if len(nalu) < 1 {
			continue
		}
		switch nalu[0] & 0x1F {
		case 7:
			sps = cloneBytes(nalu)
		case 8:
			pps = cloneBytes(nalu)
		}
	}

	return sps, pps
}

// ExtractH265Params extracts the video, sequence, and picture parameter sets from H265 NAL units.
func ExtractH265Params(nalus [][]byte) ([]byte, []byte, []byte) {
	var vps []byte
	var sps []byte
	var pps []byte

	for _, nalu := range nalus {
		if len(nalu) < 2 {
			continue
		}
		switch (nalu[0] >> 1) & 0x3F {
		case 32:
			vps = cloneBytes(nalu)
		case 33:
			sps = cloneBytes(nalu)
		case 34:
			pps = cloneBytes(nalu)
		}
	}

	return vps, sps, pps
}

// FixH265AggregationTemporalID fixes the temporal ID in HEVC aggregation packets to conform to RFC requirements.
func FixH265AggregationTemporalID(pkts []*rtp.Packet) {
	for _, pkt := range pkts {
		if len(pkt.Payload) < 6 {
			continue
		}

		naluType := (pkt.Payload[0] >> 1) & 0x3F
		if naluType != 48 {
			continue
		}

		firstNALULen := int(binary.BigEndian.Uint16(pkt.Payload[2:4]))
		if firstNALULen < 2 || len(pkt.Payload) < 4+firstNALULen {
			continue
		}

		head0 := pkt.Payload[4]
		head1 := pkt.Payload[5]
		pkt.Payload[0] = (head0 & 0x81) | (48 << 1)
		pkt.Payload[1] = head1
	}
}

func cloneBytes(buf []byte) []byte {
	return append([]byte(nil), buf...)
}

// h264NALUnitType returns the H264 nal_unit_type from the first header byte.
func h264NALUnitType(header0 byte) byte {
	return header0 & 0x1F
}

// h264IsSliceNAL reports whether typ is a VCL slice NAL type (H264 types 1–5).
func h264IsSliceNAL(typ byte) bool {
	return typ >= 1 && typ <= 5
}

// ReorderH264NALsForAccessUnit places non-VCL NALs (parameter sets, SEI, AUD, …) before
// VCL slice NALs so the RTP packetizer's marker bit lands on the last slice of the AU.
func ReorderH264NALsForAccessUnit(nalus [][]byte) [][]byte {
	var nonSlice, slice [][]byte
	for _, n := range nalus {
		if len(n) < 1 {
			continue
		}
		if h264IsSliceNAL(h264NALUnitType(n[0])) {
			slice = append(slice, n)
		} else {
			nonSlice = append(nonSlice, n)
		}
	}
	return append(nonSlice, slice...)
}
