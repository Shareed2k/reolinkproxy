package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/shareed2k/reolinkproxy/pkg/baichuan"
)

const defaultTalkGStreamerCommand = "gst-launch-1.0"

type talkEncoderMode string

const (
	talkEncoderAuto      talkEncoderMode = "auto"
	talkEncoderInternal  talkEncoderMode = "internal"
	talkEncoderGStreamer talkEncoderMode = "gstreamer"
)

type gstreamerTalkEncoder struct {
	ctx           context.Context
	cmd           *exec.Cmd
	stdin         io.WriteCloser
	stdout        io.ReadCloser
	bytesPerBlock int
	blocks        chan []byte
	errs          chan error
	stderr        bytes.Buffer
	closeOnce     sync.Once
	errOnce       sync.Once
}

func normalizeTalkEncoderMode(raw string) talkEncoderMode {
	switch strings.TrimSpace(strings.ToLower(raw)) {
	case "", "auto":
		return talkEncoderAuto
	case "internal", "builtin", "built-in":
		return talkEncoderInternal
	case "gstreamer", "gst":
		return talkEncoderGStreamer
	default:
		return talkEncoderAuto
	}
}

func buildGStreamerTalkArgs(inputRate int, targetRate int, blockAlign int) []string {
	return []string{
		"-q",
		"fdsrc", "fd=0",
		"!",
		"rawaudioparse",
		"use-sink-caps=false",
		"format=pcm",
		"pcm-format=s16le",
		fmt.Sprintf("sample-rate=%d", inputRate),
		"num-channels=1",
		"!",
		"audioconvert",
		"!",
		"audioresample",
		"!",
		fmt.Sprintf("audio/x-raw,format=S16LE,rate=%d,channels=1,layout=interleaved", targetRate),
		"!",
		"adpcmenc",
		fmt.Sprintf("blockalign=%d", blockAlign),
		"layout=dvi",
		"!",
		"capsfilter",
		fmt.Sprintf("caps=audio/x-adpcm,layout=dvi,block_align=%d,channels=1,rate=%d", blockAlign, targetRate),
		"!",
		"fdsink", "fd=1", "sync=false",
	}
}

func newGStreamerTalkEncoder(
	ctx context.Context,
	command string,
	inputRate int,
	targetRate int,
	blockAlign int,
) (*gstreamerTalkEncoder, error) {
	if command == "" {
		command = defaultTalkGStreamerCommand
	}
	if _, err := exec.LookPath(command); err != nil {
		return nil, fmt.Errorf("find %s: %w", command, err)
	}

	cmd := exec.CommandContext(ctx, command, buildGStreamerTalkArgs(inputRate, targetRate, blockAlign)...)

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("open gstreamer stdin: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		_ = stdin.Close()
		return nil, fmt.Errorf("open gstreamer stdout: %w", err)
	}

	enc := &gstreamerTalkEncoder{
		ctx:           ctx,
		cmd:           cmd,
		stdin:         stdin,
		stdout:        stdout,
		bytesPerBlock: blockAlign,
		blocks:        make(chan []byte, 8),
		errs:          make(chan error, 1),
	}
	cmd.Stderr = &enc.stderr

	if err := cmd.Start(); err != nil {
		_ = stdin.Close()
		_ = stdout.Close()
		return nil, fmt.Errorf("start %s: %w", command, err)
	}

	go enc.readBlocks()
	go enc.wait()
	return enc, nil
}

func (e *gstreamerTalkEncoder) Blocks() <-chan []byte {
	return e.blocks
}

func (e *gstreamerTalkEncoder) Errors() <-chan error {
	return e.errs
}

func (e *gstreamerTalkEncoder) WritePCM(pcm []int16) error {
	if len(pcm) == 0 {
		return nil
	}

	buf := make([]byte, len(pcm)*2)
	for i, sample := range pcm {
		binary.LittleEndian.PutUint16(buf[i*2:i*2+2], uint16(sample))
	}

	for len(buf) > 0 {
		n, err := e.stdin.Write(buf)
		if err != nil {
			if e.ctx.Err() != nil {
				return e.ctx.Err()
			}
			return fmt.Errorf("write pcm to gstreamer: %w", err)
		}
		buf = buf[n:]
	}

	return nil
}

func (e *gstreamerTalkEncoder) Close() {
	e.closeOnce.Do(func() {
		if e.stdin != nil {
			_ = e.stdin.Close()
		}
		if e.cmd != nil && e.cmd.Process != nil && e.ctx.Err() == nil {
			_ = e.cmd.Process.Kill()
		}
	})
}

func (e *gstreamerTalkEncoder) readBlocks() {
	defer close(e.blocks)

	for {
		block := make([]byte, e.bytesPerBlock)
		_, err := io.ReadFull(e.stdout, block)
		if err != nil {
			if e.ctx.Err() == nil && !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) {
				e.reportError(fmt.Errorf("read adpcm block: %w", err))
			}
			return
		}

		// GStreamer's adpcmenc layout=dvi outputs WAV DVI ADPCM (first sample in low nibble).
		// Reolink expects DVI4 (first sample in high nibble). Swap the payload nibbles.
		for i := 4; i < len(block); i++ {
			block[i] = (block[i] << 4) | (block[i] >> 4)
		}

		select {
		case <-e.ctx.Done():
			return
		case e.blocks <- block:
		}
	}
}

func (e *gstreamerTalkEncoder) wait() {
	err := e.cmd.Wait()
	if err == nil || e.ctx.Err() != nil {
		return
	}

	stderr := strings.TrimSpace(e.stderr.String())
	if stderr != "" {
		e.reportError(fmt.Errorf("gstreamer exited: %w: %s", err, stderr))
		return
	}
	e.reportError(fmt.Errorf("gstreamer exited: %w", err))
}

func (e *gstreamerTalkEncoder) reportError(err error) {
	if err == nil {
		return
	}
	e.errOnce.Do(func() {
		select {
		case e.errs <- err:
		default:
		}
	})
}

func (p *rtspTalkPublisher) runBridgeGStreamer(
	state *rtspTalkSessionState,
	input *rtspTalkInput,
	talkSession *baichuan.TalkSession,
) error {
	encoder, err := newGStreamerTalkEncoder(
		state.ctx,
		p.talkEncoderCmd,
		input.sampleRate,
		talkSession.SampleRate(),
		talkSession.BytesPerBlock(),
	)
	if err != nil {
		return err
	}
	defer encoder.Close()

	log.Printf(
		"talk %s using gstreamer encoder input=%s/%d target=ADPCM/%d block_align=%d",
		p.cameraName,
		input.codecName,
		input.sampleRate,
		talkSession.SampleRate(),
		talkSession.BytesPerBlock(),
	)
	startedAt := time.Now()
	blocksWritten := 0
	defer func() {
		log.Debugf("talk %s gstreamer bridge stopped path=%s duration=%v blocks=%d", p.cameraName, state.path, time.Since(startedAt).Round(time.Millisecond), blocksWritten)
	}()

	writeBlock := func(block []byte) error {
		writeCtx, cancel := context.WithTimeout(state.ctx, 5*time.Second)
		err := talkSession.WriteADPCMBlock(writeCtx, block)
		cancel()
		if err != nil {
			return fmt.Errorf("write talk block: %w", err)
		}
		return nil
	}

	pcmWriteErrCh := make(chan error, 1)
	go func() {
		defer close(pcmWriteErrCh)

		for {
			select {
			case <-state.ctx.Done():
				return

			case pcm := <-state.pcmCh:
				if len(pcm) == 0 {
					continue
				}
				if err := encoder.WritePCM(pcm); err != nil {
					select {
					case pcmWriteErrCh <- err:
					default:
					}
					return
				}
			}
		}
	}()

	for {
		select {
		case <-state.ctx.Done():
			log.Debugf("talk %s gstreamer bridge context done path=%s err=%v", p.cameraName, state.path, state.ctx.Err())
			return nil

		case err, ok := <-pcmWriteErrCh:
			if !ok || err == nil {
				continue
			}
			log.Debugf("talk %s gstreamer pcm writer stopped path=%s err=%v", p.cameraName, state.path, err)
			return err

		case err := <-encoder.Errors():
			if err == nil {
				continue
			}
			log.Debugf("talk %s gstreamer encoder stopped path=%s err=%v", p.cameraName, state.path, err)
			return err

		case block, ok := <-encoder.Blocks():
			if !ok {
				if state.ctx.Err() != nil {
					return nil
				}
				return fmt.Errorf("gstreamer talk encoder stopped")
			}
			if err := writeBlock(block); err != nil {
				return err
			}
			blocksWritten++
		}
	}
}
