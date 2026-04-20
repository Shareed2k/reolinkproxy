package main

import (
	"strings"
	"testing"
)

func TestNormalizeTalkEncoderMode(t *testing.T) {
	t.Parallel()

	cases := map[string]talkEncoderMode{
		"":          talkEncoderAuto,
		"auto":      talkEncoderAuto,
		"internal":  talkEncoderInternal,
		"built-in":  talkEncoderInternal,
		"gstreamer": talkEncoderGStreamer,
		"gst":       talkEncoderGStreamer,
		"something": talkEncoderAuto,
	}

	for input, want := range cases {
		if got := normalizeTalkEncoderMode(input); got != want {
			t.Fatalf("normalizeTalkEncoderMode(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestBuildGStreamerTalkArgs(t *testing.T) {
	t.Parallel()

	args := buildGStreamerTalkArgs(8000, 16000, 512)
	joined := strings.Join(args, " ")

	for _, want := range []string{
		"rawaudioparse",
		"sample-rate=8000",
		"audio/x-raw,format=S16LE,rate=16000,channels=1,layout=interleaved",
		"adpcmenc",
		"blockalign=512",
		"layout=dvi",
		"caps=audio/x-adpcm,layout=dvi,block_align=512,channels=1,rate=16000",
		"fdsink fd=1 sync=false",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("buildGStreamerTalkArgs() missing %q in %q", want, joined)
		}
	}
}
