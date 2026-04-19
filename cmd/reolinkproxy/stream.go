package main

import (
	"encoding/binary"
	"fmt"
	"log"
	"net"
	"strings"
	"sync"
	"time"

	gortsplib "github.com/bluenviron/gortsplib/v4"
	"github.com/bluenviron/gortsplib/v4/pkg/base"
	"github.com/bluenviron/gortsplib/v4/pkg/description"
	"github.com/bluenviron/gortsplib/v4/pkg/format"
	"github.com/bluenviron/gortsplib/v4/pkg/format/rtplpcm"
	"github.com/bluenviron/gortsplib/v4/pkg/format/rtpmpeg4audio"
	"github.com/bluenviron/mediacommon/pkg/codecs/mpeg4audio"
	"github.com/pion/rtp"

	"github.com/shareed2k/reolinkproxy/pkg/baichuan"
)

type rtspServerHandler struct {
	mu      sync.RWMutex
	streams map[string]*rtspStreamHandler
}

func newRTSPServerHandler() *rtspServerHandler {
	return &rtspServerHandler{
		streams: make(map[string]*rtspStreamHandler),
	}
}

func (h *rtspServerHandler) addStream(path string, stream *rtspStreamHandler) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.streams[strings.TrimPrefix(path, "/")] = stream
}

func (h *rtspServerHandler) getStream(path string) *rtspStreamHandler {
	h.mu.RLock()
	defer h.mu.RUnlock()
	for p, s := range h.streams {
		if samePath(path, p) {
			return s
		}
	}
	return nil
}

func (h *rtspServerHandler) OnDescribe(ctx *gortsplib.ServerHandlerOnDescribeCtx) (*base.Response, *gortsplib.ServerStream, error) {
	stream := h.getStream(ctx.Path)
	if stream == nil {
		return &base.Response{StatusCode: base.StatusNotFound}, nil, nil
	}

	stream.mu.RLock()
	defer stream.mu.RUnlock()
	if stream.stream == nil {
		return &base.Response{StatusCode: base.StatusNotFound}, nil, nil
	}

	return &base.Response{StatusCode: base.StatusOK}, stream.stream, nil
}

func (h *rtspServerHandler) OnSetup(ctx *gortsplib.ServerHandlerOnSetupCtx) (*base.Response, *gortsplib.ServerStream, error) {
	stream := h.getStream(ctx.Path)
	if stream == nil {
		return &base.Response{StatusCode: base.StatusNotFound}, nil, nil
	}

	stream.mu.RLock()
	defer stream.mu.RUnlock()
	if stream.stream == nil {
		return &base.Response{StatusCode: base.StatusNotFound}, nil, nil
	}

	return &base.Response{StatusCode: base.StatusOK}, stream.stream, nil
}

//nolint:unparam
func (h *rtspServerHandler) OnPlay(_ *gortsplib.ServerHandlerOnPlayCtx) (*base.Response, error) {
	return &base.Response{StatusCode: base.StatusOK}, nil
}

type rtspStreamHandler struct {
	server *gortsplib.Server
	path   string

	mu     sync.RWMutex
	stream *gortsplib.ServerStream
}

func newRTSPStreamHandler(path string) *rtspStreamHandler {
	return &rtspStreamHandler{path: strings.TrimPrefix(path, "/")}
}

func (h *rtspStreamHandler) attachServer(server *gortsplib.Server) {
	h.server = server
}

func (h *rtspStreamHandler) ready() bool {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.stream != nil
}

func (h *rtspStreamHandler) setReady(medias ...*description.Media) error {
	h.mu.Lock()
	defer h.mu.Unlock()

	if h.stream != nil {
		return nil
	}
	if h.server == nil {
		return fmt.Errorf("rtsp server is not attached")
	}

	filtered := make([]*description.Media, 0, len(medias))
	for _, media := range medias {
		if media != nil {
			filtered = append(filtered, media)
		}
	}
	if len(filtered) == 0 {
		return fmt.Errorf("rtsp session requires at least one media")
	}

	desc := &description.Session{Medias: filtered}
	h.stream = gortsplib.NewServerStream(h.server, desc)
	return nil
}

func (h *rtspStreamHandler) writePacket(media *description.Media, pkt *rtp.Packet) {
	h.mu.RLock()
	stream := h.stream
	h.mu.RUnlock()
	if stream != nil {
		_ = stream.WritePacketRTP(media, pkt)
	}
}

type audioPublisher struct {
	media         *description.Media
	aacEncoder    *rtpmpeg4audio.Encoder
	g711Encoder   *rtplpcm.Encoder
	adpcmDecoder  *baichuan.ADPCMDecoder
	nextTimestamp uint32
	unsupported   bool
	lateIgnored   bool
}

func (p *audioPublisher) ready() bool {
	return p.media != nil && (p.aacEncoder != nil || p.g711Encoder != nil)
}

func (p *audioPublisher) mediaDescription() *description.Media {
	return p.media
}

func (p *audioPublisher) awaitingStartupDecision(deadline time.Time) bool {
	return !p.ready() && !p.unsupported && time.Now().Before(deadline)
}

func (p *audioPublisher) markUnsupported(reason string) {
	if p.unsupported {
		return
	}
	p.unsupported = true
	log.Printf("audio passthrough disabled: %s", reason)
}

func (p *audioPublisher) processAAC(data []byte, baseTimeMicroseconds uint32, handler *rtspStreamHandler, meta *streamMetadata) error {
	aus, cfg, err := parseAACAccessUnits(data)
	if err != nil {
		p.markUnsupported(fmt.Sprintf("invalid AAC/ADTS payload: %v", err))
		return nil
	}

	if !p.ready() {
		if handler.ready() {
			if !p.lateIgnored {
				p.lateIgnored = true
				log.Printf("audio arrived after RTSP session creation; keeping stream video-only")
			}
			return nil
		}

		audioFormat := &format.MPEG4Audio{
			PayloadTyp:       97,
			Config:           cfg,
			SizeLength:       13,
			IndexLength:      3,
			IndexDeltaLength: 3,
		}
		encoder, err := audioFormat.CreateEncoder()
		if err != nil {
			return fmt.Errorf("create AAC RTP encoder: %w", err)
		}

		p.media = &description.Media{
			Type:    description.MediaTypeAudio,
			Control: "trackID=1",
			Formats: []format.Format{audioFormat},
		}
		p.aacEncoder = encoder
		p.nextTimestamp = rtpTimestampForClock(baseTimeMicroseconds, cfg.SampleRate)
		meta.setAudioAAC(cfg.SampleRate, cfg.ChannelCount)

		log.Printf("audio configured codec=AAC sample_rate=%d channels=%d", cfg.SampleRate, cfg.ChannelCount)
	}

	if !handler.ready() {
		return nil
	}

	pkts, err := p.aacEncoder.Encode(aus)
	if err != nil {
		return fmt.Errorf("encode AAC RTP: %w", err)
	}

	for _, pkt := range pkts {
		pkt.Timestamp = p.nextTimestamp
		handler.writePacket(p.media, pkt)
	}

	p.nextTimestamp += uint32(len(aus)) * mpeg4audio.SamplesPerAccessUnit
	return nil
}

func (p *audioPublisher) processADPCM(data []byte, baseTimeMicroseconds uint32, handler *rtspStreamHandler, meta *streamMetadata) error {
	if p.adpcmDecoder == nil {
		p.adpcmDecoder = &baichuan.ADPCMDecoder{}
	}

	pcm := p.adpcmDecoder.Decode(data)
	pcma := baichuan.EncodePCMA(pcm)

	sampleRate := 8000 // Reolink usually sends ADPCM at 8kHz
	channelCount := 1

	if !p.ready() {
		if handler.ready() {
			if !p.lateIgnored {
				p.lateIgnored = true
				log.Printf("audio arrived after RTSP session creation; keeping stream video-only")
			}
			return nil
		}

		audioFormat := &format.G711{
			PayloadTyp:   8, // PCMA
			MULaw:        false,
			SampleRate:   sampleRate,
			ChannelCount: channelCount,
		}
		encoder, err := audioFormat.CreateEncoder()
		if err != nil {
			return fmt.Errorf("create G711 RTP encoder: %w", err)
		}

		p.media = &description.Media{
			Type:    description.MediaTypeAudio,
			Control: "trackID=1",
			Formats: []format.Format{audioFormat},
		}
		p.g711Encoder = encoder
		p.nextTimestamp = rtpTimestampForClock(baseTimeMicroseconds, sampleRate)
		meta.setAudioG711(sampleRate, channelCount)

		log.Printf("audio configured codec=PCMA sample_rate=%d channels=%d", sampleRate, channelCount)
	}

	if !handler.ready() {
		return nil
	}

	pkts, err := p.g711Encoder.Encode(pcma)
	if err != nil {
		return fmt.Errorf("encode G711 RTP: %w", err)
	}

	for _, pkt := range pkts {
		pkt.Timestamp = p.nextTimestamp
		handler.writePacket(p.media, pkt)
	}

	p.nextTimestamp += uint32(len(pcm))
	return nil
}

type streamMetadata struct {
	mu sync.RWMutex

	cameraName      string
	name            string
	token           string
	path            string
	width           uint32
	height          uint32
	fps             uint8
	audioCodec      string
	audioSampleRate int
	audioChannels   int
	videoCodec      string
}

type streamMetadataSnapshot struct {
	Name            string
	Token           string
	Path            string
	Width           uint32
	Height          uint32
	FPS             uint8
	AudioCodec      string
	AudioSampleRate int
	AudioChannels   int
	VideoCodec      string
}

func (m *streamMetadata) setVideoInfo(width uint32, height uint32, fps uint8, codec string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.width = width
	m.height = height
	m.fps = fps
	if codec != "" {
		m.videoCodec = codec
	}
}

func (m *streamMetadata) setVideoCodec(codec string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.videoCodec = codec
}

func (m *streamMetadata) setAudioAAC(sampleRate int, channels int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.audioCodec = "AAC"
	m.audioSampleRate = sampleRate
	m.audioChannels = channels
}

func (m *streamMetadata) setAudioG711(sampleRate int, channels int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.audioCodec = "PCMA"
	m.audioSampleRate = sampleRate
	m.audioChannels = channels
}

func (m *streamMetadata) snapshot() streamMetadataSnapshot {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return streamMetadataSnapshot{
		Name:            m.name,
		Token:           m.token,
		Path:            m.path,
		Width:           m.width,
		Height:          m.height,
		FPS:             m.fps,
		AudioCodec:      m.audioCodec,
		AudioSampleRate: m.audioSampleRate,
		AudioChannels:   m.audioChannels,
		VideoCodec:      m.videoCodec,
	}
}

func (s streamMetadataSnapshot) normalized() streamMetadataSnapshot {
	if s.Width == 0 {
		s.Width = 3840
	}
	if s.Height == 0 {
		s.Height = 2160
	}
	if s.FPS == 0 {
		s.FPS = 15
	}
	if s.VideoCodec == "" {
		if strings.Contains(strings.ToLower(s.Name), "sub") || strings.Contains(strings.ToLower(s.Name), "extern") {
			s.VideoCodec = "H264"
		} else {
			s.VideoCodec = "H265" // default fallback for main
		}
	}
	return s
}

func parseAACAccessUnits(data []byte) ([][]byte, *mpeg4audio.Config, error) {
	var packets mpeg4audio.ADTSPackets
	if err := packets.Unmarshal(data); err != nil {
		return nil, nil, err
	}
	if len(packets) == 0 {
		return nil, nil, fmt.Errorf("empty ADTS packet set")
	}

	first := packets[0]
	cfg := &mpeg4audio.Config{
		Type:         first.Type,
		SampleRate:   first.SampleRate,
		ChannelCount: first.ChannelCount,
	}

	aus := make([][]byte, 0, len(packets))
	for _, pkt := range packets {
		if pkt.Type != cfg.Type || pkt.SampleRate != cfg.SampleRate || pkt.ChannelCount != cfg.ChannelCount {
			return nil, nil, fmt.Errorf("mixed AAC configuration inside one payload")
		}
		aus = append(aus, cloneBytes(pkt.AU))
	}

	return aus, cfg, nil
}

func samePath(got string, want string) bool {
	got = strings.Trim(strings.TrimSpace(got), "/")
	want = strings.Trim(strings.TrimSpace(want), "/")
	return got == want
}

func splitAnnexB(buf []byte) [][]byte {
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

func extractH264Params(nalus [][]byte) ([]byte, []byte) {
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

func extractH265Params(nalus [][]byte) ([]byte, []byte, []byte) {
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

func fixH265AggregationTemporalID(pkts []*rtp.Packet) {
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

func coalesce(next []byte, fallback []byte) []byte {
	if next != nil {
		return next
	}
	return fallback
}

func rtpTimestampForClock(microseconds uint32, clockRate int) uint32 {
	return uint32((uint64(microseconds) * uint64(clockRate)) / 1_000_000)
}

func getOutboundIP() string {
	conn, err := net.Dial("udp", "8.8.8.8:80")
	if err != nil {
		return ""
	}
	defer conn.Close()
	localAddr := conn.LocalAddr().(*net.UDPAddr)
	return localAddr.IP.String()
}

func advertisedAuthority(address string, overrideHost string) string {
	host := ""
	port := ""

	if parsedHost, parsedPort, err := net.SplitHostPort(address); err == nil {
		host = parsedHost
		port = parsedPort
	} else if strings.HasPrefix(address, ":") {
		port = strings.TrimPrefix(address, ":")
	} else {
		host = address
	}

	if overrideHost != "" {
		host = overrideHost
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		if outbound := getOutboundIP(); outbound != "" {
			host = outbound
		} else {
			host = "127.0.0.1"
		}
	}
	if port == "" {
		return host
	}
	return net.JoinHostPort(host, port)
}

func buildURL(scheme string, authority string, path string) string {
	path = "/" + strings.TrimPrefix(path, "/")
	return fmt.Sprintf("%s://%s%s", scheme, authority, path)
}
