package main

import (
	"testing"

	gortsplib "github.com/bluenviron/gortsplib/v4"
	"github.com/bluenviron/gortsplib/v4/pkg/base"
	"github.com/bluenviron/gortsplib/v4/pkg/description"
	gformat "github.com/bluenviron/gortsplib/v4/pkg/format"
	"github.com/pion/rtp"
)

func TestTalkPathForCamera(t *testing.T) {
	t.Parallel()

	if got, want := talkPathForCamera("/front/stream"), "front/stream_talk"; got != want {
		t.Fatalf("talkPathForCamera() = %q, want %q", got, want)
	}
}

func TestOnDescribeTalkPath(t *testing.T) {
	t.Parallel()

	server := &gortsplib.Server{}
	handler := newRTSPServerHandler()
	handler.server = server
	handler.addTalk("office/stream_talk", &rtspTalkPublisher{})

	res, stream, err := handler.OnDescribe(&gortsplib.ServerHandlerOnDescribeCtx{Path: "office/stream_talk"})
	if err != nil {
		t.Fatalf("OnDescribe() error = %v, want nil", err)
	}
	if got, want := res.StatusCode, base.StatusOK; got != want {
		t.Fatalf("status = %v, want %v", got, want)
	}
	if stream == nil {
		t.Fatal("stream = nil, want non-nil")
	}
}

func TestOnDescribePrefersStreamOverTalkAlias(t *testing.T) {
	t.Parallel()

	handler := newRTSPServerHandler()
	playback := newRTSPStreamHandler("office/stream")
	playback.stream = &gortsplib.ServerStream{}
	handler.addStream("office/stream", playback)
	handler.addTalkAlias("office/stream", &rtspTalkPublisher{})

	res, stream, err := handler.OnDescribe(&gortsplib.ServerHandlerOnDescribeCtx{Path: "office/stream"})
	if err != nil {
		t.Fatalf("OnDescribe() error = %v, want nil", err)
	}
	if got, want := res.StatusCode, base.StatusOK; got != want {
		t.Fatalf("status = %v, want %v", got, want)
	}
	if stream != playback.stream {
		t.Fatalf("stream = %p, want %p", stream, playback.stream)
	}
}

func TestOnPlayUsesStreamWhenTalkAliasExists(t *testing.T) {
	t.Parallel()

	handler := newRTSPServerHandler()
	playback := newRTSPStreamHandler("office/stream")
	handler.addStream("office/stream", playback)
	handler.addTalkAlias("office/stream", &rtspTalkPublisher{})

	res, err := handler.OnPlay(&gortsplib.ServerHandlerOnPlayCtx{
		Session: &gortsplib.ServerSession{},
		Path:    "office/stream",
	})
	if err != nil {
		t.Fatalf("OnPlay() error = %v, want nil", err)
	}
	if got, want := res.StatusCode, base.StatusOK; got != want {
		t.Fatalf("status = %v, want %v", got, want)
	}
	if got := playback.hasClients(); !got {
		t.Fatal("expected playback handler to track an active client")
	}
}

func TestNewBackChannelMedia(t *testing.T) {
	t.Parallel()

	media := newBackChannelMedia()
	if !media.IsBackChannel {
		t.Fatal("expected backchannel media to be marked sendonly")
	}
	if got, want := media.Control, "trackID=2"; got != want {
		t.Fatalf("control = %q, want %q", got, want)
	}
	if got, want := len(media.Formats), 2; got != want {
		t.Fatalf("formats = %d, want %d", got, want)
	}
}

func TestSelectBackChannelInputs(t *testing.T) {
	t.Parallel()

	medias := []*description.Media{
		{
			Type:    description.MediaTypeAudio,
			Control: "trackID=1",
			Formats: []gformat.Format{&gformat.G711{
				PayloadTyp:   8,
				MULaw:        false,
				SampleRate:   8000,
				ChannelCount: 1,
			}},
		},
		newBackChannelMedia(),
	}

	inputs, err := selectBackChannelInputs(medias)
	if err != nil {
		t.Fatalf("selectBackChannelInputs() error = %v", err)
	}
	if got, want := len(inputs), 2; got != want {
		t.Fatalf("len(inputs) = %d, want %d", got, want)
	}
	for _, input := range inputs {
		if input.media == nil || !input.media.IsBackChannel {
			t.Fatal("expected only backchannel inputs")
		}
	}
}

func TestSelectTalkInputAcceptsG711(t *testing.T) {
	t.Parallel()

	desc := &description.Session{
		Medias: []*description.Media{
			{
				Type:    description.MediaTypeVideo,
				Control: "trackID=0",
				Formats: []gformat.Format{&gformat.H264{PayloadTyp: 96}},
			},
			{
				Type:    description.MediaTypeAudio,
				Control: "trackID=1",
				Formats: []gformat.Format{&gformat.G711{
					PayloadTyp:   0,
					MULaw:        true,
					SampleRate:   8000,
					ChannelCount: 1,
				}},
			},
		},
	}

	input, err := selectTalkInput(desc)
	if err != nil {
		t.Fatalf("selectTalkInput() error = %v", err)
	}
	if got, want := input.codecName, "PCMU"; got != want {
		t.Fatalf("codecName = %q, want %q", got, want)
	}
	if got, want := input.sampleRate, 8000; got != want {
		t.Fatalf("sampleRate = %d, want %d", got, want)
	}
}

func TestSelectTalkInputAcceptsLPCM(t *testing.T) {
	t.Parallel()

	desc := &description.Session{
		Medias: []*description.Media{
			{
				Type:    description.MediaTypeAudio,
				Control: "trackID=0",
				Formats: []gformat.Format{&gformat.LPCM{
					PayloadTyp:   96,
					BitDepth:     16,
					SampleRate:   16000,
					ChannelCount: 1,
				}},
			},
		},
	}

	input, err := selectTalkInput(desc)
	if err != nil {
		t.Fatalf("selectTalkInput() error = %v", err)
	}
	if got, want := input.codecName, "L16"; got != want {
		t.Fatalf("codecName = %q, want %q", got, want)
	}
	if got, want := input.sampleRate, 16000; got != want {
		t.Fatalf("sampleRate = %d, want %d", got, want)
	}
}

func TestRTSPTalkInputDecodePCMU(t *testing.T) {
	t.Parallel()

	input := &rtspTalkInput{
		g711: &gformat.G711{
			PayloadTyp:   0,
			MULaw:        true,
			SampleRate:   8000,
			ChannelCount: 1,
		},
	}

	pcm, err := input.decode(&rtp.Packet{Payload: []byte{0xff, 0x7f}})
	if err != nil {
		t.Fatalf("decode() error = %v", err)
	}
	if got, want := len(pcm), 2; got != want {
		t.Fatalf("len(pcm) = %d, want %d", got, want)
	}
}

func TestRTSPTalkInputDecodeLPCM(t *testing.T) {
	t.Parallel()

	input := &rtspTalkInput{
		lpcm: &gformat.LPCM{
			PayloadTyp:   96,
			BitDepth:     16,
			SampleRate:   16000,
			ChannelCount: 1,
		},
	}

	pcm, err := input.decode(&rtp.Packet{Payload: []byte{0x03, 0xE8, 0xFC, 0x18}})
	if err != nil {
		t.Fatalf("decode() error = %v", err)
	}
	want := []int16{1000, -1000}
	for i := range want {
		if pcm[i] != want[i] {
			t.Fatalf("pcm[%d] = %d, want %d", i, pcm[i], want[i])
		}
	}
}

func TestResamplePCM(t *testing.T) {
	t.Parallel()

	in := []int16{0, 1000, 2000, 3000}
	out := resamplePCM(in, 8000, 16000)

	if got, want := len(out), 8; got != want {
		t.Fatalf("len(out) = %d, want %d", got, want)
	}
	if out[0] != in[0] {
		t.Fatalf("out[0] = %d, want %d", out[0], in[0])
	}
	if out[len(out)-1] != in[len(in)-1] {
		t.Fatalf("out[last] = %d, want %d", out[len(out)-1], in[len(in)-1])
	}
}

func TestApplyTalkVolume(t *testing.T) {
	t.Parallel()

	pcm := []int16{1000, -2000, 20000, -20000}
	applyTalkVolume(pcm, 200)

	want := []int16{2000, -4000, 32767, -32768}
	for i := range want {
		if pcm[i] != want[i] {
			t.Fatalf("pcm[%d] = %d, want %d", i, pcm[i], want[i])
		}
	}
}
