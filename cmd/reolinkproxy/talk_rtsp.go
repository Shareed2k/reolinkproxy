package main

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/bluenviron/gortsplib/v4"
	"github.com/bluenviron/gortsplib/v4/pkg/base"
	"github.com/bluenviron/gortsplib/v4/pkg/description"
	gformat "github.com/bluenviron/gortsplib/v4/pkg/format"
	"github.com/pion/rtp"
	"github.com/shareed2k/reolinkproxy/pkg/baichuan"
)

type rtspTalkPublisher struct {
	path           string
	cameraName     string
	channel        uint8
	clientConfig   baichuan.Config
	talkVolume     int
	talkEncoder    string
	talkEncoderCmd string

	mu     sync.Mutex
	active *rtspTalkSessionState
	stream *gortsplib.ServerStream
}

type rtspTalkSessionState struct {
	publisher *rtspTalkPublisher
	session   *gortsplib.ServerSession
	path      string
	ctx       context.Context
	cancel    context.CancelFunc
	pcmCh     chan []int16
	done      chan struct{}
	doneOnce  sync.Once
	stopOnce  sync.Once
}

const rtspTalkPCMQueueSize = 16

type rtspTalkInput struct {
	media      *description.Media
	g711       *gformat.G711
	lpcm       *gformat.LPCM
	codecName  string
	sampleRate int
}

func newDedicatedTalkMedia() *description.Media {
	return &description.Media{
		Type:    description.MediaTypeAudio,
		Control: "trackID=0",
		Formats: []gformat.Format{
			&gformat.G711{
				PayloadTyp:   0,
				MULaw:        true,
				SampleRate:   8000,
				ChannelCount: 1,
			},
			&gformat.G711{
				PayloadTyp:   8,
				MULaw:        false,
				SampleRate:   8000,
				ChannelCount: 1,
			},
		},
	}
}

func newBackChannelMedia() *description.Media {
	media := newDedicatedTalkMedia()
	media.Control = "trackID=2"
	media.IsBackChannel = true
	return media
}

func newRTSPTalkPublisher(
	path string,
	cameraName string,
	channel uint8,
	clientConfig baichuan.Config,
	talkVolume int,
	talkEncoder string,
	talkEncoderCmd string,
) *rtspTalkPublisher {
	return &rtspTalkPublisher{
		path:           strings.TrimPrefix(path, "/"),
		cameraName:     cameraName,
		channel:        channel,
		clientConfig:   clientConfig,
		talkVolume:     talkVolume,
		talkEncoder:    talkEncoder,
		talkEncoderCmd: talkEncoderCmd,
	}
}

func talkPathForCamera(rtspPath string) string {
	rtspPath = strings.Trim(strings.TrimSpace(rtspPath), "/")
	if rtspPath == "" {
		return "talk"
	}
	return rtspPath + "_talk"
}

func twoWayPathForStream(rtspPath string) string {
	rtspPath = strings.Trim(strings.TrimSpace(rtspPath), "/")
	if rtspPath == "" {
		return "twoway"
	}
	return rtspPath + "_twoway"
}

func (s *rtspTalkSessionState) close() {
	if s == nil {
		return
	}
	s.stopOnce.Do(func() {
		if s.publisher != nil {
			s.publisher.finish(s)
		}
		if s.cancel != nil {
			s.cancel()
		}
	})
}

func (s *rtspTalkSessionState) markDone() {
	if s == nil {
		return
	}
	s.doneOnce.Do(func() {
		close(s.done)
	})
}

func (p *rtspTalkPublisher) finish(state *rtspTalkSessionState) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.active == state {
		p.active = nil
	}
}

func (p *rtspTalkPublisher) describe(server *gortsplib.Server) (*gortsplib.ServerStream, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.stream != nil {
		return p.stream, nil
	}

	desc := &description.Session{
		Medias: []*description.Media{newDedicatedTalkMedia()},
	}

	stream := gortsplib.NewServerStream(server, desc)
	p.stream = stream
	return stream, nil
}

func (p *rtspTalkPublisher) announce(ctx *gortsplib.ServerHandlerOnAnnounceCtx) (*base.Response, error) {
	if _, err := selectTalkInput(ctx.Description); err != nil {
		return &base.Response{StatusCode: base.StatusBadRequest}, err
	}
	return &base.Response{StatusCode: base.StatusOK}, nil
}

func (p *rtspTalkPublisher) ensureSessionState(session *gortsplib.ServerSession) (*rtspSessionState, *rtspTalkSessionState) {
	state := attachSessionState(session)
	if state != nil && state.talk != nil && state.talk.publisher == p && state.talk.session == session {
		return state, state.talk
	}

	var active *rtspTalkSessionState
	for {
		p.mu.Lock()
		if p.active != nil {
			select {
			case <-p.active.ctx.Done():
				p.active = nil
			default:
			}
		}
		if p.active == nil {
			bridgeCtx, cancel := context.WithCancel(context.Background())
			state.talk = &rtspTalkSessionState{
				publisher: p,
				session:   session,
				ctx:       bridgeCtx,
				cancel:    cancel,
				pcmCh:     make(chan []int16, rtspTalkPCMQueueSize),
				done:      make(chan struct{}),
			}
			p.active = state.talk
			active = state.talk
			p.mu.Unlock()
			break
		}
		if p.active.session == session {
			state.talk = p.active
			active = state.talk
			p.mu.Unlock()
			break
		}
		prev := p.active
		p.mu.Unlock()

		log.Debugf("talk %s replacing previous rtsp session", p.cameraName)
		prev.close()
		if prev.session != nil {
			if prevState, ok := prev.session.UserData().(*rtspSessionState); ok && prevState != nil && prevState.talk == prev {
				prevState.talk = nil
			}
			closeTalkRTSPSession(prev)
		}
		select {
		case <-prev.done:
		case <-time.After(2 * time.Second):
		}
	}

	return state, active
}

func (p *rtspTalkPublisher) bindInputs(session *gortsplib.ServerSession, inputs []*rtspTalkInput, active *rtspTalkSessionState) *rtspTalkInput {
	var primary *rtspTalkInput

	for _, input := range inputs {
		if input == nil {
			continue
		}
		if primary == nil {
			primary = input
		}

		current := input
		var inputFormat gformat.Format
		if current.g711 != nil {
			inputFormat = current.g711
		} else {
			inputFormat = current.lpcm
		}

		session.OnPacketRTP(current.media, inputFormat, func(pkt *rtp.Packet) {
			pcm, err := current.decode(pkt)
			if err != nil {
				log.Printf("talk %s decode error: %v", p.cameraName, err)
				return
			}
			if len(pcm) == 0 {
				return
			}
			if p.talkVolume != 100 {
				applyTalkVolume(pcm, p.talkVolume)
			}

			enqueueTalkPCM(active, pcm)
		})
	}

	return primary
}

func (p *rtspTalkPublisher) startBridge(session *gortsplib.ServerSession, path string, inputs []*rtspTalkInput) error {
	state := attachSessionState(session)
	if state != nil && state.talk != nil && state.talk.publisher == p && state.talk.session == session {
		return nil
	}

	state, active := p.ensureSessionState(session)
	if active == nil {
		return fmt.Errorf("failed to initialize talk session state")
	}
	active.path = strings.TrimPrefix(path, "/")

	primary := p.bindInputs(session, inputs, active)
	if primary == nil {
		return fmt.Errorf("talkback input is not configured")
	}

	connectCtx, cancel := context.WithTimeout(active.ctx, 10*time.Second)
	talkClient, err := baichuan.Dial(connectCtx, p.clientConfig)
	if err != nil {
		cancel()
		if errors.Is(err, context.Canceled) {
			log.Debugf("talk %s dial canceled", p.cameraName)
		} else {
			log.Printf("talk %s dial error: %v", p.cameraName, err)
		}
		p.finish(active)
		active.markDone()
		state.talk = nil
		return err
	}
	if err := talkClient.Login(connectCtx); err != nil {
		cancel()
		_ = talkClient.Close()
		if errors.Is(err, context.Canceled) {
			log.Debugf("talk %s login canceled", p.cameraName)
		} else {
			log.Printf("talk %s login error: %v", p.cameraName, err)
		}
		p.finish(active)
		active.markDone()
		state.talk = nil
		return err
	}
	talkSession, err := talkClient.StartTalk(connectCtx, p.channel)
	cancel()
	if err != nil {
		if errors.Is(err, context.Canceled) {
			log.Debugf("talk %s start canceled", p.cameraName)
		} else {
			log.Printf("talk %s start error: %v", p.cameraName, err)
		}
		_ = talkClient.Close()
		p.finish(active)
		active.markDone()
		state.talk = nil
		return err
	}
	go p.runBridge(active, primary, talkClient, talkSession)

	log.Printf(
		"talk registered camera=%s path=%s input=%s/%d target=ADPCM/%d volume=%d%%",
		p.cameraName,
		strings.TrimPrefix(path, "/"),
		primary.codecName,
		primary.sampleRate,
		talkSession.SampleRate(),
		p.talkVolume,
	)

	return nil
}

func (p *rtspTalkPublisher) record(ctx *gortsplib.ServerHandlerOnRecordCtx) (*base.Response, error) {
	desc := ctx.Session.AnnouncedDescription()
	if desc == nil && p.stream != nil {
		desc = p.stream.Description()
	}
	input, err := selectTalkInput(desc)
	if err != nil {
		return &base.Response{StatusCode: base.StatusBadRequest}, err
	}

	if err := p.startBridge(ctx.Session, ctx.Path, []*rtspTalkInput{input}); err != nil {
		return &base.Response{StatusCode: base.StatusBadRequest}, err
	}

	return &base.Response{StatusCode: base.StatusOK}, nil
}

func (p *rtspTalkPublisher) startBackChannel(session *gortsplib.ServerSession, path string) error {
	inputs, err := selectBackChannelInputs(session.SetuppedMedias())
	if err != nil {
		return err
	}
	return p.startBridge(session, path, inputs)
}

func enqueueTalkPCM(state *rtspTalkSessionState, pcm []int16) {
	for {
		select {
		case <-state.ctx.Done():
			return
		case state.pcmCh <- pcm:
			return
		default:
		}

		select {
		case <-state.ctx.Done():
			return
		case <-state.pcmCh:
			// Drop the oldest buffered audio to keep latency bounded for live talk.
		default:
		}
	}
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

func (p *rtspTalkPublisher) runBridge(
	state *rtspTalkSessionState,
	input *rtspTalkInput,
	talkClient *baichuan.Client,
	talkSession *baichuan.TalkSession,
) {
	startedAt := time.Now()
	encoderMode := normalizeTalkEncoderMode(p.talkEncoder)
	result := "completed"
	defer p.finish(state)
	defer state.close()
	defer state.markDone()
	defer func() {
		if state.ctx.Err() != nil {
			result = state.ctx.Err().Error()
		}
		log.Printf("talk %s bridge stopped path=%s mode=%s duration=%v result=%s", p.cameraName, state.path, encoderMode, time.Since(startedAt).Round(time.Millisecond), result)
	}()
	defer func() {
		closeCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := talkSession.Close(closeCtx); err != nil {
			log.Printf("talk %s close error: %v", p.cameraName, err)
		}
		if err := talkClient.Close(); err != nil {
			log.Printf("talk %s client close error: %v", p.cameraName, err)
		}
	}()

	if encoderMode != talkEncoderInternal {
		err := p.runBridgeGStreamer(state, input, talkSession)
		if err != nil && !errors.Is(err, context.Canceled) {
			result = err.Error()
			log.Printf("talk %s gstreamer encoder error: %v", p.cameraName, err)
			if encoderMode == talkEncoderGStreamer {
				closeTalkRTSPSession(state)
				return
			}
			log.Printf("talk %s falling back to internal adpcm encoder", p.cameraName)
		} else {
			return
		}
	}

	if err := p.runBridgeInternal(state, input, talkSession); err != nil {
		result = err.Error()
	}
}

func (p *rtspTalkPublisher) runBridgeInternal(state *rtspTalkSessionState, input *rtspTalkInput, talkSession *baichuan.TalkSession) error {
	encoder := &baichuan.ADPCMEncoder{}
	targetSampleRate := talkSession.SampleRate()
	blockSamples := talkSession.SamplesPerBlock()
	pcmBuffer := make([]int16, 0, blockSamples*2)
	startedAt := time.Now()
	pcmPackets := 0
	pcmSamples := 0
	blocksWritten := 0
	defer func() {
		log.Debugf("talk %s internal bridge stopped path=%s duration=%v pcm_packets=%d pcm_samples=%d blocks=%d", p.cameraName, state.path, time.Since(startedAt).Round(time.Millisecond), pcmPackets, pcmSamples, blocksWritten)
	}()

	for {
		select {
		case <-state.ctx.Done():
			log.Debugf("talk %s internal bridge context done path=%s err=%v", p.cameraName, state.path, state.ctx.Err())
			return nil

		case pcm := <-state.pcmCh:
			pcmPackets++
			pcmSamples += len(pcm)
			if input.sampleRate != targetSampleRate {
				pcm = resamplePCM(pcm, input.sampleRate, targetSampleRate)
			}
			if len(pcm) == 0 {
				continue
			}

			pcmBuffer = append(pcmBuffer, pcm...)
			for len(pcmBuffer) >= blockSamples {
				block, err := encoder.EncodeBlock(pcmBuffer[:blockSamples])
				if err != nil {
					log.Printf("talk %s adpcm encode error: %v", p.cameraName, err)
					closeTalkRTSPSession(state)
					return err
				}

				writeCtx, cancel := context.WithTimeout(state.ctx, 5*time.Second)
				err = talkSession.WriteADPCMBlock(writeCtx, block)
				cancel()
				if err != nil {
					log.Printf("talk %s write error: %v", p.cameraName, err)
					closeTalkRTSPSession(state)
					return err
				}
				blocksWritten++

				pcmBuffer = pcmBuffer[blockSamples:]
			}
		}
	}
}

func closeTalkRTSPSession(state *rtspTalkSessionState) {
	if state == nil || state.session == nil {
		return
	}
	sessionState, ok := state.session.UserData().(*rtspSessionState)
	if ok && sessionState != nil && sessionState.stream != nil {
		return
	}
	state.session.Close()
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
		return baichuan.DecodePCMU(pkt.Payload), nil
	}
	if i.g711 != nil {
		return baichuan.DecodePCMA(pkt.Payload), nil
	}

	if len(pkt.Payload)%2 != 0 {
		return nil, fmt.Errorf("invalid lpcm payload size %d", len(pkt.Payload))
	}

	out := make([]int16, len(pkt.Payload)/2)
	for j := 0; j < len(out); j++ {
		out[j] = int16(binary.BigEndian.Uint16(pkt.Payload[j*2 : j*2+2]))
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
		out[i] = int16(a + ((b-a)*fraction)/int64(toRate))
	}
	return out
}

func (h *rtspServerHandler) OnAnnounce(ctx *gortsplib.ServerHandlerOnAnnounceCtx) (*base.Response, error) {
	talk := h.getTalk(ctx.Path)
	if talk == nil {
		return &base.Response{StatusCode: base.StatusNotFound}, fmt.Errorf("rtsp announce: talk path not found for %q", ctx.Path)
	}
	return talk.announce(ctx)
}

func (h *rtspServerHandler) OnRecord(ctx *gortsplib.ServerHandlerOnRecordCtx) (*base.Response, error) {
	talk := h.getTalk(ctx.Path)
	if talk == nil {
		return &base.Response{StatusCode: base.StatusNotFound}, fmt.Errorf("rtsp record: talk path not found for %q", ctx.Path)
	}
	return talk.record(ctx)
}
