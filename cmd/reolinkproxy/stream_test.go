package main

import (
	"encoding/binary"
	"net/http/httptest"
	"testing"

	"github.com/bluenviron/mediacommon/pkg/codecs/mpeg4audio"
	"github.com/pion/rtp"
)

func TestFixH265AggregationTemporalID(t *testing.T) {
	t.Parallel()

	firstNALU := []byte{0x40, 0x01, 0xaa, 0xbb}
	payload := make([]byte, 2+2+len(firstNALU))
	payload[0] = 48 << 1
	payload[1] = 0x00
	binary.BigEndian.PutUint16(payload[2:4], uint16(len(firstNALU)))
	copy(payload[4:], firstNALU)

	pkt := &rtp.Packet{Payload: payload}
	fixH265AggregationTemporalID([]*rtp.Packet{pkt})

	if got, want := pkt.Payload[0], byte((firstNALU[0]&0x81)|(48<<1)); got != want {
		t.Fatalf("payload[0] = %#x, want %#x", got, want)
	}
	if got, want := pkt.Payload[1], firstNALU[1]; got != want {
		t.Fatalf("payload[1] = %#x, want %#x", got, want)
	}
}

func TestParseAACAccessUnits(t *testing.T) {
	t.Parallel()

	raw, err := mpeg4audio.ADTSPackets{
		{
			Type:         mpeg4audio.ObjectTypeAACLC,
			SampleRate:   16000,
			ChannelCount: 1,
			AU:           []byte{0x11, 0x22, 0x33},
		},
		{
			Type:         mpeg4audio.ObjectTypeAACLC,
			SampleRate:   16000,
			ChannelCount: 1,
			AU:           []byte{0x44, 0x55},
		},
	}.Marshal()
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}

	aus, cfg, err := parseAACAccessUnits(raw)
	if err != nil {
		t.Fatalf("parseAACAccessUnits() error = %v", err)
	}
	if got, want := len(aus), 2; got != want {
		t.Fatalf("len(aus) = %d, want %d", got, want)
	}
	if got, want := cfg.SampleRate, 16000; got != want {
		t.Fatalf("cfg.SampleRate = %d, want %d", got, want)
	}
	if got, want := cfg.ChannelCount, 1; got != want {
		t.Fatalf("cfg.ChannelCount = %d, want %d", got, want)
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
