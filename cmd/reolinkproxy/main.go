package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/bluenviron/gortsplib/v4"
	"github.com/bluenviron/gortsplib/v4/pkg/base"
	"github.com/bluenviron/gortsplib/v4/pkg/description"
	"github.com/bluenviron/gortsplib/v4/pkg/format"
	"github.com/pion/rtp"

	"github.com/shareed2k/reolinkproxy/pkg/baichuan"
)

func main() {
	cameraCfg := baichuan.Config{
		Host:     envString("REOLINK_HOST", ""),
		Port:     envInt("REOLINK_PORT", baichuan.DefaultPort),
		UID:      envString("REOLINK_UID", ""),
		Username: envString("REOLINK_USERNAME", ""),
		Password: envString("REOLINK_PASSWORD", ""),
		Timeout:  envDuration("REOLINK_TIMEOUT", baichuan.DefaultTimeout),
	}

	stream := string(envString("REOLINK_STREAM", string(baichuan.StreamMain)))
	channel := envInt("REOLINK_CHANNEL", 0)
	logPackets := false

	rtspAddress := envString("RTSP_ADDRESS", ":8554")
	rtpAddress := envString("RTSP_RTP_ADDRESS", ":8000")
	rtcpAddress := envString("RTSP_RTCP_ADDRESS", ":8001")
	rtspPath := envString("RTSP_PATH", "Camera01/main")

	flag.StringVar(&cameraCfg.Host, "host", cameraCfg.Host, "camera host or IP")
	flag.IntVar(&cameraCfg.Port, "port", cameraCfg.Port, "Baichuan TCP port")
	flag.StringVar(&cameraCfg.UID, "uid", cameraCfg.UID, "camera UID for local UDP discovery")
	flag.StringVar(&cameraCfg.Username, "username", cameraCfg.Username, "camera username")
	flag.StringVar(&cameraCfg.Password, "password", cameraCfg.Password, "camera password")
	flag.DurationVar(&cameraCfg.Timeout, "timeout", cameraCfg.Timeout, "connection timeout")
	flag.StringVar(&stream, "stream", stream, "stream to request: main|sub|extern")
	flag.IntVar(&channel, "channel", channel, "camera channel id")
	flag.StringVar(&rtspAddress, "rtsp-address", rtspAddress, "RTSP listen address")
	flag.StringVar(&rtpAddress, "rtp-address", rtpAddress, "RTP UDP listen address")
	flag.StringVar(&rtcpAddress, "rtcp-address", rtcpAddress, "RTCP UDP listen address")
	flag.StringVar(&rtspPath, "rtsp-path", rtspPath, "RTSP path to publish")
	flag.BoolVar(&logPackets, "log-packets", false, "log every parsed video packet")
	flag.Parse()

	if cameraCfg.Host == "" && cameraCfg.UID == "" {
		log.Fatal("set -host or -uid")
	}
	if cameraCfg.Username == "" || cameraCfg.Password == "" {
		log.Fatal("set -username and -password")
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	client, err := baichuan.Dial(ctx, cameraCfg)
	if err != nil {
		log.Fatalf("dial camera: %v", err)
	}
	defer client.Close()

	if err := client.Login(ctx); err != nil {
		log.Fatalf("login: %v", err)
	}

	reader, err := client.StartPreview(ctx, uint8(channel), parseStream(stream))
	if err != nil {
		log.Fatalf("start preview: %v", err)
	}
	defer reader.Close()

	log.Printf("preview started transport=%s channel=%d stream=%s", transportName(cameraCfg), channel, stream)

	rtspPath = strings.TrimPrefix(rtspPath, "/")
	handler := newRTSPHandler(rtspPath)
	server := &gortsplib.Server{
		Handler:        handler,
		RTSPAddress:    rtspAddress,
		UDPRTPAddress:  rtpAddress,
		UDPRTCPAddress: rtcpAddress,
	}

	if err := server.Start(); err != nil {
		log.Fatalf("start rtsp server: %v", err)
	}
	defer server.Close()
	handler.attachServer(server)

	log.Printf("rtsp server listening at rtsp://%s/%s", rtspAdvertiseHost(rtspAddress), rtspPath)

	var (
		infoPackets  uint64
		videoPackets uint64
		audioPackets uint64
		videoBytes   uint64
		firstVideo   bool
	)

	h265Format := &format.H265{
		PayloadTyp: 96,
	}
	encoder, err := h265Format.CreateEncoder()
	if err != nil {
		log.Fatalf("create h265 rtp encoder: %v", err)
	}

	videoMedia := &description.Media{
		Type:    description.MediaTypeVideo,
		Control: "trackID=0",
		Formats: []format.Format{h265Format},
	}

	statsTicker := time.NewTicker(5 * time.Second)
	defer statsTicker.Stop()

	for {
		select {
		case <-ctx.Done():
			return

		case packet, ok := <-reader.Packets:
			if !ok {
				log.Fatalf("preview stream closed: %v", client.Err())
			}

			switch packet.Kind {
			case baichuan.MediaPacketInfoV1, baichuan.MediaPacketInfoV2:
				infoPackets++
				log.Printf("stream info size=%dx%d fps=%d", packet.Width, packet.Height, packet.FPS)

			case baichuan.MediaPacketIFrame, baichuan.MediaPacketPFrame:
				if packet.Codec != "H265" {
					if !firstVideo {
						log.Printf("skipping unsupported codec %q", packet.Codec)
					}
					continue
				}

				nalus := splitAnnexB(packet.Data)
				if len(nalus) == 0 {
					continue
				}

				vps, sps, pps := extractH265Params(nalus)
				if vps != nil || sps != nil || pps != nil {
					h265Format.SafeSetParams(coalesce(vps, h265Format.VPS), coalesce(sps, h265Format.SPS), coalesce(pps, h265Format.PPS))
				}

				if !handler.ready() {
					curVPS, curSPS, curPPS := h265Format.SafeParams()
					if curVPS == nil || curSPS == nil || curPPS == nil {
						if packet.Kind == baichuan.MediaPacketIFrame && logPackets {
							log.Printf("waiting for VPS/SPS/PPS before exposing RTSP path")
						}
						continue
					}

					if err := handler.setReady(videoMedia); err != nil {
						log.Fatalf("prepare rtsp stream: %v", err)
					}
				}

				pkts, err := encoder.Encode(nalus)
				if err != nil {
					log.Fatalf("encode rtp: %v", err)
				}

				ts := rtpTimestamp(packet.TimestampMicrosecs)
				for _, pkt := range pkts {
					pkt.Timestamp = ts
					handler.writePacket(videoMedia, pkt)
				}

				videoPackets++
				videoBytes += uint64(len(packet.Data))
				if !firstVideo || logPackets {
					firstVideo = true
					log.Printf("video packet kind=%s codec=%s nalus=%d bytes=%d ts_us=%d", packet.Kind, packet.Codec, len(nalus), len(packet.Data), packet.TimestampMicrosecs)
				}

			case baichuan.MediaPacketAAC, baichuan.MediaPacketADPCM:
				audioPackets++
			}

		case <-statsTicker.C:
			log.Printf("stats info=%d video=%d audio=%d video_bytes=%d rtsp_ready=%t", infoPackets, videoPackets, audioPackets, videoBytes, handler.ready())
		}
	}
}

type rtspHandler struct {
	server *gortsplib.Server
	path   string

	mu     sync.RWMutex
	stream *gortsplib.ServerStream
}

func newRTSPHandler(path string) *rtspHandler {
	return &rtspHandler{path: strings.TrimPrefix(path, "/")}
}

func (h *rtspHandler) attachServer(server *gortsplib.Server) {
	h.server = server
}

func (h *rtspHandler) ready() bool {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.stream != nil
}

func (h *rtspHandler) setReady(videoMedia *description.Media) error {
	h.mu.Lock()
	defer h.mu.Unlock()

	if h.stream != nil {
		return nil
	}
	if h.server == nil {
		return fmt.Errorf("rtsp server is not attached")
	}

	desc := &description.Session{
		Medias: []*description.Media{videoMedia},
	}
	h.stream = gortsplib.NewServerStream(h.server, desc)
	return nil
}

func (h *rtspHandler) writePacket(media *description.Media, pkt *rtp.Packet) {
	h.mu.RLock()
	stream := h.stream
	h.mu.RUnlock()
	if stream != nil {
		stream.WritePacketRTP(media, pkt)
	}
}

func (h *rtspHandler) OnDescribe(ctx *gortsplib.ServerHandlerOnDescribeCtx) (*base.Response, *gortsplib.ServerStream, error) {
	if !samePath(ctx.Path, h.path) {
		return &base.Response{StatusCode: base.StatusNotFound}, nil, nil
	}

	h.mu.RLock()
	defer h.mu.RUnlock()
	if h.stream == nil {
		return &base.Response{StatusCode: base.StatusNotFound}, nil, nil
	}

	return &base.Response{StatusCode: base.StatusOK}, h.stream, nil
}

func (h *rtspHandler) OnSetup(ctx *gortsplib.ServerHandlerOnSetupCtx) (*base.Response, *gortsplib.ServerStream, error) {
	if !samePath(ctx.Path, h.path) {
		return &base.Response{StatusCode: base.StatusNotFound}, nil, nil
	}

	h.mu.RLock()
	defer h.mu.RUnlock()
	if h.stream == nil {
		return &base.Response{StatusCode: base.StatusNotFound}, nil, nil
	}

	return &base.Response{StatusCode: base.StatusOK}, h.stream, nil
}

func (h *rtspHandler) OnPlay(_ *gortsplib.ServerHandlerOnPlayCtx) (*base.Response, error) {
	return &base.Response{StatusCode: base.StatusOK}, nil
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

func cloneBytes(buf []byte) []byte {
	return append([]byte(nil), buf...)
}

func coalesce(next []byte, fallback []byte) []byte {
	if next != nil {
		return next
	}
	return fallback
}

func rtpTimestamp(microseconds uint32) uint32 {
	return uint32((uint64(microseconds) * 90000) / 1_000_000)
}

func parseStream(v string) baichuan.Stream {
	switch v {
	case "sub":
		return baichuan.StreamSub
	case "extern":
		return baichuan.StreamExtern
	default:
		return baichuan.StreamMain
	}
}

func transportName(cfg baichuan.Config) string {
	if cfg.UID != "" {
		return "uid-udp"
	}
	return "tcp"
}

func rtspAdvertiseHost(address string) string {
	if strings.HasPrefix(address, ":") {
		return "127.0.0.1" + address
	}

	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return address
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}
	return net.JoinHostPort(host, port)
}

func envString(key string, def string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return def
}

func envInt(key string, def int) int {
	value := os.Getenv(key)
	if value == "" {
		return def
	}
	parsed, err := strconv.Atoi(value)
	if err != nil {
		return def
	}
	return parsed
}

func envDuration(key string, def time.Duration) time.Duration {
	value := os.Getenv(key)
	if value == "" {
		return def
	}
	parsed, err := time.ParseDuration(value)
	if err != nil {
		return def
	}
	return parsed
}
