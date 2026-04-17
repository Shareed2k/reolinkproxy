package baichuan

import (
	"testing"
)

func TestADPCMDecoder(t *testing.T) {
	// A small dummy stream of ADPCM data
	data := []byte{0x00, 0x11, 0x22, 0x33, 0x44, 0x55, 0x66, 0x77}
	decoder := &ADPCMDecoder{}
	pcm := decoder.Decode(data)

	if len(pcm) != len(data)*2 {
		t.Fatalf("expected pcm length %d, got %d", len(data)*2, len(pcm))
	}

	// Just verify it doesn't panic and state is updated
	if decoder.index == 0 && decoder.predicted == 0 {
		t.Errorf("decoder state should have updated after decoding")
	}
}
