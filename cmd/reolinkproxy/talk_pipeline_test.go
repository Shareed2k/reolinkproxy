package main

import (
	"context"
	"sync"
	"testing"
)

type fakeTalkWriter struct {
	mu         sync.Mutex
	sampleRate int
	samplesPB  int
	blocks     [][]byte
}

func (f *fakeTalkWriter) SampleRate() int      { return f.sampleRate }
func (f *fakeTalkWriter) SamplesPerBlock() int { return f.samplesPB }
func (f *fakeTalkWriter) BytesPerBlock() int   { return 4 + f.samplesPB/2 }

func (f *fakeTalkWriter) WriteADPCMBlock(_ context.Context, block []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.blocks = append(f.blocks, append([]byte(nil), block...))
	return nil
}

func (f *fakeTalkWriter) writtenBlocks() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.blocks)
}

// Regression test for issue #27: audio still queued when the RTSP session is
// torn down must be flushed to the camera, including the final partial block,
// otherwise short clips are cut off.
func TestRunBridgeInternalFlushesOnCancel(t *testing.T) {
	t.Parallel()

	const samplesPB = 8
	writer := &fakeTalkWriter{sampleRate: 16000, samplesPB: samplesPB}
	input := &rtspTalkInput{sampleRate: 16000} // same rate: no resampling

	nonSilent := func(n int) []int16 {
		pcm := make([]int16, n)
		for i := range pcm {
			pcm[i] = 1000
		}
		return pcm
	}

	// 6 + 6 + 6 = 18 samples: 2 full blocks of 8, plus 2 samples that only a
	// zero-padded final block can deliver.
	firstPcm := nonSilent(6)
	pcmCh := make(chan []int16, 4)
	pcmCh <- nonSilent(6)
	pcmCh <- nonSilent(6)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	p := newTalkbackPipeline("cam", 0, nil, 100, string(talkEncoderInternal), "")
	if err := p.runBridgeInternal(ctx, "cam/stream_talk", input, writer, firstPcm, pcmCh); err != nil {
		t.Fatalf("runBridgeInternal() error = %v, want nil", err)
	}

	if got, want := writer.writtenBlocks(), 3; got != want {
		t.Fatalf("written blocks = %d, want %d (queued audio and padded final block must be flushed)", got, want)
	}
}
