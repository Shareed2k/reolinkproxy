package main

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"time"

	"github.com/bluenviron/gortsplib/v5/pkg/description"
	gformat "github.com/bluenviron/gortsplib/v5/pkg/format"
	"github.com/pion/rtp"
	"github.com/shareed2k/reolinkproxy/pkg/baichuan"
	"github.com/shareed2k/reolinkproxy/pkg/codec"
)

type rtspTalkInput struct {
	media      *description.Media
	g711       *gformat.G711
	lpcm       *gformat.LPCM
	codecName  string
	sampleRate int
}

func selectTalkInput(desc *description.Session) (*rtspTalkInput, error) {
	if desc == nil {
		return nil, fmt.Errorf("missing announced session description")
	}

	for _, media := range desc.Medias {
		if media.Type != description.MediaTypeAudio {
			continue
		}

		for _, forma := range media.Formats {
			g711, ok := forma.(*gformat.G711)
			if ok {
				if g711.ChannelCount != 1 {
					return nil, fmt.Errorf("talkback only supports mono G711, got %d channels", g711.ChannelCount)
				}

				codecName := "PCMA"
				if g711.MULaw {
					codecName = "PCMU"
				}

				return &rtspTalkInput{
					media:      media,
					g711:       g711,
					codecName:  codecName,
					sampleRate: g711.SampleRate,
				}, nil
			}

			lpcm, ok := forma.(*gformat.LPCM)
			if !ok {
				continue
			}
			if lpcm.BitDepth != 16 {
				return nil, fmt.Errorf("talkback only supports 16-bit LPCM, got %d-bit", lpcm.BitDepth)
			}
			if lpcm.ChannelCount != 1 {
				return nil, fmt.Errorf("talkback only supports mono LPCM, got %d channels", lpcm.ChannelCount)
			}

			return &rtspTalkInput{
				media:      media,
				lpcm:       lpcm,
				codecName:  "L16",
				sampleRate: lpcm.SampleRate,
			}, nil
		}
	}

	return nil, fmt.Errorf("talkback requires mono G711 or 16-bit mono LPCM audio")
}

func selectBackChannelInputs(medias []*description.Media) ([]*rtspTalkInput, error) {
	var inputs []*rtspTalkInput

	for _, media := range medias {
		if media == nil || media.Type != description.MediaTypeAudio || !media.IsBackChannel {
			continue
		}

		for _, forma := range media.Formats {
			g711, ok := forma.(*gformat.G711)
			if !ok {
				continue
			}
			if g711.ChannelCount != 1 {
				return nil, fmt.Errorf("talkback only supports mono G711, got %d channels", g711.ChannelCount)
			}

			codecName := "PCMA"
			if g711.MULaw {
				codecName = "PCMU"
			}

			inputs = append(inputs, &rtspTalkInput{
				media:      media,
				g711:       g711,
				codecName:  codecName,
				sampleRate: g711.SampleRate,
			})
		}
	}

	if len(inputs) == 0 {
		return nil, fmt.Errorf("backchannel requires a sendonly mono G711 audio media")
	}

	return inputs, nil
}

func (i *rtspTalkInput) decode(pkt *rtp.Packet) ([]int16, error) {
	if pkt == nil {
		return nil, nil
	}
	if i == nil || (i.g711 == nil && i.lpcm == nil) {
		return nil, fmt.Errorf("talkback input is not configured")
	}

	if i.g711 != nil && i.g711.MULaw {
		return codec.DecodePCMU(pkt.Payload), nil
	}
	if i.g711 != nil {
		return codec.DecodePCMA(pkt.Payload), nil
	}

	if len(pkt.Payload)%2 != 0 {
		return nil, fmt.Errorf("invalid lpcm payload size %d", len(pkt.Payload))
	}

	out := make([]int16, len(pkt.Payload)/2)
	for j := 0; j < len(out); j++ {
		out[j] = int16(binary.BigEndian.Uint16(pkt.Payload[j*2 : j*2+2])) //#nosec G115
	}
	return out, nil
}

func resamplePCM(in []int16, fromRate int, toRate int) []int16 {
	if len(in) == 0 || fromRate <= 0 || toRate <= 0 {
		return nil
	}
	if fromRate == toRate {
		return append([]int16(nil), in...)
	}
	if len(in) == 1 {
		outLen := int((int64(len(in))*int64(toRate) + int64(fromRate) - 1) / int64(fromRate))
		if outLen < 1 {
			outLen = 1
		}
		out := make([]int16, outLen)
		for i := range out {
			out[i] = in[0]
		}
		return out
	}

	outLen := int((int64(len(in))*int64(toRate) + int64(fromRate) - 1) / int64(fromRate))
	if outLen < 1 {
		outLen = 1
	}

	out := make([]int16, outLen)
	for i := 0; i < outLen; i++ {
		positionNum := int64(i) * int64(fromRate)
		baseIndex := int(positionNum / int64(toRate))
		if baseIndex >= len(in)-1 {
			out[i] = in[len(in)-1]
			continue
		}

		fraction := positionNum % int64(toRate)
		a := int64(in[baseIndex])
		b := int64(in[baseIndex+1])
		out[i] = int16(a + ((b-a)*fraction)/int64(toRate)) //#nosec G115
	}
	return out
}

func applyTalkVolume(pcm []int16, percent int) {
	if percent == 100 {
		return
	}
	if percent < 0 {
		percent = 0
	}

	for i, sample := range pcm {
		scaled := int64(sample) * int64(percent) / 100
		if scaled > 32767 {
			scaled = 32767
		}
		if scaled < -32768 {
			scaled = -32768
		}
		pcm[i] = int16(scaled)
	}
}

func isSilence(pcm []int16) bool {
	for _, sample := range pcm {
		if sample > 25 || sample < -25 {
			return false
		}
	}
	return true
}

func enqueueTalkPCM(ctx context.Context, pcmCh chan []int16, pcm []int16) {
	for {
		select {
		case <-ctx.Done():
			return
		case pcmCh <- pcm:
			return
		default:
		}

		select {
		case <-ctx.Done():
			return
		case <-pcmCh:
			// Drop the oldest buffered audio to keep latency bounded for live talk.
		default:
		}
	}
}

type talkbackPipeline struct {
	cameraName     string
	channel        uint8
	clientManager  *cameraClientManager
	talkVolume     int
	talkEncoder    string
	talkEncoderCmd string
}

func newTalkbackPipeline(
	cameraName string,
	channel uint8,
	clientManager *cameraClientManager,
	talkVolume int,
	talkEncoder string,
	talkEncoderCmd string,
) *talkbackPipeline {
	return &talkbackPipeline{
		cameraName:     cameraName,
		channel:        channel,
		clientManager:  clientManager,
		talkVolume:     talkVolume,
		talkEncoder:    talkEncoder,
		talkEncoderCmd: talkEncoderCmd,
	}
}

func (p *talkbackPipeline) run(ctx context.Context, pcmCh <-chan []int16, input *rtspTalkInput, path string) {
	for {
		select {
		case <-ctx.Done():
			return
		case firstPcm := <-pcmCh:
			if len(firstPcm) == 0 || isSilence(firstPcm) {
				continue
			}

			connectCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
			talkClient, err := p.clientManager.Ensure(connectCtx)
			if err != nil {
				cancel()
				log.Printf("talk %s dial error: %v", p.cameraName, err)
				continue
			}
			talkSession, err := talkClient.StartTalk(connectCtx, p.channel)
			cancel()
			if err != nil {
				p.clientManager.ResetIfCurrent(talkClient, fmt.Sprintf("talk session start error: %v", err))
				log.Printf("talk %s start error: %v", p.cameraName, err)
				continue
			}

			log.Printf(
				"talk session activated camera=%s path=%s input=%s/%d target=ADPCM/%d volume=%d%%",
				p.cameraName,
				path,
				input.codecName,
				input.sampleRate,
				talkSession.SampleRate(),
				p.talkVolume,
			)

			p.runBridge(ctx, path, input, talkClient, talkSession, firstPcm, pcmCh)

			closeCtx, cancelClose := context.WithTimeout(context.Background(), 5*time.Second)
			if err := talkSession.Close(closeCtx); err != nil {
				log.Printf("talk %s close error: %v", p.cameraName, err)
			}
			cancelClose()
		}
	}
}

func (p *talkbackPipeline) runBridge(
	ctx context.Context,
	path string,
	input *rtspTalkInput,
	_ *baichuan.Client,
	talkSession *baichuan.TalkSession,
	firstPcm []int16,
	pcmCh <-chan []int16,
) {
	startedAt := time.Now()
	encoderMode := normalizeTalkEncoderMode(p.talkEncoder)
	result := "completed (idle)"
	bridgeCtx, bridgeCancel := context.WithCancel(ctx)
	defer bridgeCancel()
	defer func() {
		if ctx.Err() != nil {
			result = ctx.Err().Error()
		}
		log.Printf("talk %s bridge stopped path=%s mode=%s duration=%v result=%s", p.cameraName, path, encoderMode, time.Since(startedAt).Round(time.Millisecond), result)
	}()

	if encoderMode != talkEncoderInternal {
		err := p.runBridgeGStreamer(bridgeCtx, path, input, talkSession, firstPcm, pcmCh)
		if err != nil && !errors.Is(err, context.Canceled) {
			result = err.Error()
			log.Printf("talk %s gstreamer encoder error: %v", p.cameraName, err)
			if encoderMode == talkEncoderGStreamer {
				return
			}
			log.Printf("talk %s falling back to internal adpcm encoder", p.cameraName)
		} else {
			return
		}
	}

	if err := p.runBridgeInternal(bridgeCtx, path, input, talkSession, firstPcm, pcmCh); err != nil {
		result = err.Error()
	}
}

func (p *talkbackPipeline) runBridgeInternal(
	ctx context.Context,
	path string,
	input *rtspTalkInput,
	talkSession *baichuan.TalkSession,
	firstPcm []int16,
	pcmCh <-chan []int16,
) error {
	encoder := &codec.ADPCMEncoder{}
	targetSampleRate := talkSession.SampleRate()
	blockSamples := talkSession.SamplesPerBlock()
	pcmBuffer := make([]int16, 0, blockSamples*2)
	startedAt := time.Now()
	pcmPackets := 0
	pcmSamples := 0
	blocksWritten := 0
	defer func() {
		log.Debugf("talk %s internal bridge stopped path=%s duration=%v pcm_packets=%d pcm_samples=%d blocks=%d", p.cameraName, path, time.Since(startedAt).Round(time.Millisecond), pcmPackets, pcmSamples, blocksWritten)
	}()

	idleTimer := time.NewTimer(5 * time.Second)
	defer idleTimer.Stop()

	processPCM := func(pcm []int16) error {
		pcmPackets++
		pcmSamples += len(pcm)
		if input.sampleRate != targetSampleRate {
			pcm = resamplePCM(pcm, input.sampleRate, targetSampleRate)
		}
		if len(pcm) == 0 {
			return nil
		}

		pcmBuffer = append(pcmBuffer, pcm...)
		for len(pcmBuffer) >= blockSamples {
			block, err := encoder.EncodeBlock(pcmBuffer[:blockSamples])
			if err != nil {
				log.Printf("talk %s adpcm encode error: %v", p.cameraName, err)
				return err
			}

			writeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
			err = talkSession.WriteADPCMBlock(writeCtx, block)
			cancel()
			if err != nil {
				log.Printf("talk %s write error: %v", p.cameraName, err)
				return err
			}
			blocksWritten++

			pcmBuffer = pcmBuffer[blockSamples:]
		}
		return nil
	}

	if err := processPCM(firstPcm); err != nil {
		return err
	}

	for {
		select {
		case <-ctx.Done():
			log.Debugf("talk %s internal bridge context done path=%s err=%v", p.cameraName, path, ctx.Err())
			return nil

		case <-idleTimer.C:
			return nil

		case pcm := <-pcmCh:
			if !isSilence(pcm) {
				idleTimer.Reset(5 * time.Second)
			}
			if err := processPCM(pcm); err != nil {
				return err
			}
		}
	}
}
