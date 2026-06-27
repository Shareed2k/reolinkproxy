package main

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/bluenviron/gortsplib/v5"
	"github.com/bluenviron/gortsplib/v5/pkg/base"
	"github.com/bluenviron/gortsplib/v5/pkg/description"
	gformat "github.com/bluenviron/gortsplib/v5/pkg/format"
	"github.com/pion/rtp"
)

type rtspTalkPublisher struct {
	path           string
	cameraName     string
	channel        uint8
	device         *CameraDevice
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
	stopOnce  sync.Once
}

const rtspTalkPCMQueueSize = 256

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
	device *CameraDevice,
	talkVolume int,
	talkEncoder string,
	talkEncoderCmd string,
) *rtspTalkPublisher {
	return &rtspTalkPublisher{
		path:           strings.TrimPrefix(path, "/"),
		cameraName:     cameraName,
		channel:        channel,
		device:         device,
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

	stream := &gortsplib.ServerStream{Desc: desc, Server: server}
	if err := stream.Initialize(); err != nil {
		return nil, err
	}
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

			enqueueTalkPCM(active.ctx, active.pcmCh, pcm)
		})
	}

	return primary
}

func (p *rtspTalkPublisher) startBridge(session *gortsplib.ServerSession, path string, inputs []*rtspTalkInput) error {
	state := attachSessionState(session)
	if state != nil && state.talk != nil && state.talk.publisher == p && state.talk.session == session {
		return nil
	}

	_, active := p.ensureSessionState(session)
	if active == nil {
		return fmt.Errorf("failed to initialize talk session state")
	}
	active.path = strings.TrimPrefix(path, "/")

	primary := p.bindInputs(session, inputs, active)
	if primary == nil {
		return fmt.Errorf("talkback input is not configured")
	}

	go func() {
		defer p.finish(active)
		defer active.close()
		defer close(active.done)
		pipeline := newTalkbackPipeline(p.cameraName, p.channel, p.device, p.talkVolume, p.talkEncoder, p.talkEncoderCmd)
		pipeline.run(active.ctx, active.pcmCh, primary, active.path)
	}()

	return nil
}

func (p *rtspTalkPublisher) record(ctx *gortsplib.ServerHandlerOnRecordCtx) (*base.Response, error) {
	desc := ctx.Session.AnnouncedDescription()
	if desc == nil && p.stream != nil {
		desc = p.stream.Desc
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
	log.Printf("talk %s starting backchannel for path %s", p.cameraName, path)
	inputs, err := selectBackChannelInputs(session.Medias())
	if err != nil {
		log.Printf("talk %s failed to select backchannel inputs: %v", p.cameraName, err)
		return err
	}
	return p.startBridge(session, path, inputs)
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
