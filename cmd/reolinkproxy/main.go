package main

import (
	"context"
	"flag"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/bluenviron/gortsplib/v4"
	"github.com/bluenviron/gortsplib/v4/pkg/description"
	"github.com/bluenviron/gortsplib/v4/pkg/format"

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
	onvifAddress := envString("ONVIF_ADDRESS", ":8002")
	onvifDevicePath := envString("ONVIF_DEVICE_PATH", "/onvif/device_service")
	onvifMediaPath := envString("ONVIF_MEDIA_PATH", "/onvif/media_service")
	advertiseHost := envString("ADVERTISE_HOST", "")

	onvifUsername := envString("ONVIF_USERNAME", "admin")
	onvifPassword := envString("ONVIF_PASSWORD", "")

	flag.StringVar(&cameraCfg.Host, "host", cameraCfg.Host, "camera host or IP")
	flag.IntVar(&cameraCfg.Port, "port", cameraCfg.Port, "Baichuan TCP port")
	flag.StringVar(&cameraCfg.UID, "uid", cameraCfg.UID, "camera UID for local UDP discovery")
	flag.StringVar(&cameraCfg.Username, "username", cameraCfg.Username, "camera username")
	flag.StringVar(&cameraCfg.Password, "password", cameraCfg.Password, "camera password")
	flag.StringVar(&onvifUsername, "onvif-username", onvifUsername, "ONVIF username (defaults to admin)")
	flag.StringVar(&onvifPassword, "onvif-password", onvifPassword, "ONVIF password (required for ONVIF auth)")
	flag.DurationVar(&cameraCfg.Timeout, "timeout", cameraCfg.Timeout, "connection timeout")
	flag.StringVar(&stream, "stream", stream, "stream to request: main|sub|extern")
	flag.IntVar(&channel, "channel", channel, "camera channel id")
	flag.StringVar(&rtspAddress, "rtsp-address", rtspAddress, "RTSP listen address")
	flag.StringVar(&rtpAddress, "rtp-address", rtpAddress, "RTP UDP listen address")
	flag.StringVar(&rtcpAddress, "rtcp-address", rtcpAddress, "RTCP UDP listen address")
	flag.StringVar(&rtspPath, "rtsp-path", rtspPath, "RTSP path to publish")
	flag.StringVar(&onvifAddress, "onvif-address", onvifAddress, "ONVIF HTTP listen address")
	flag.StringVar(&advertiseHost, "advertise-host", advertiseHost, "host or IP advertised in RTSP and ONVIF URLs")
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
	meta := &streamMetadata{}
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

	onvifCfg := onvifConfig{
		Address:         onvifAddress,
		DevicePath:      onvifDevicePath,
		MediaPath:       onvifMediaPath,
		AdvertiseHost:   advertiseHost,
		RTSPAddress:     rtspAddress,
		RTSPPath:        rtspPath,
		DeviceName:      envString("DEVICE_NAME", deviceNameFromPath(rtspPath)),
		Manufacturer:    envString("DEVICE_MANUFACTURER", "Reolink"),
		Model:           envString("DEVICE_MODEL", "Argus 3 Ultra"),
		FirmwareVersion: envString("DEVICE_FIRMWARE_VERSION", "reolinkproxy"),
		SerialNumber:    envString("DEVICE_SERIAL_NUMBER", firstNonEmpty(cameraCfg.UID, cameraCfg.Host, "unknown")),
		HardwareID:      envString("DEVICE_HARDWARE_ID", "reolinkproxy"),
		ProfileToken:    envString("ONVIF_PROFILE_TOKEN", profileTokenFromPath(rtspPath)),
		Username:        onvifUsername,
		Password:        onvifPassword,
	}

	startWSDiscovery(onvifCfg)

	onvifServer := &http.Server{
		Addr:              onvifAddress,
		Handler:           newONVIFHandler(onvifCfg, meta),
		ReadHeaderTimeout: 5 * time.Second,
	}
	go func() {
		if err := onvifServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("start onvif server: %v", err)
		}
	}()
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = onvifServer.Shutdown(shutdownCtx)
	}()

	log.Printf("rtsp server listening at %s", buildURL("rtsp", advertisedAuthority(rtspAddress, advertiseHost), rtspPath))
	log.Printf("onvif device service listening at %s", buildURL("http", advertisedAuthority(onvifAddress, advertiseHost), onvifDevicePath))

	var (
		infoPackets          uint64
		videoPackets         uint64
		audioPackets         uint64
		videoBytes           uint64
		firstVideo           bool
		lastVideoTimestampUS uint32
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
	audio := &audioPublisher{}
	startupDeadline := time.Now().Add(2 * time.Second)

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
				meta.setVideoInfo(packet.Width, packet.Height, packet.FPS)
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
				lastVideoTimestampUS = packet.TimestampMicrosecs

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
					if audio.awaitingStartupDecision(startupDeadline) {
						continue
					}

					if err := handler.setReady(videoMedia, audio.mediaDescription()); err != nil {
						log.Fatalf("prepare rtsp stream: %v", err)
					}
				}

				pkts, err := encoder.Encode(nalus)
				if err != nil {
					log.Fatalf("encode rtp: %v", err)
				}
				fixH265AggregationTemporalID(pkts)

				ts := rtpTimestampForClock(packet.TimestampMicrosecs, h265Format.ClockRate())
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

			case baichuan.MediaPacketAAC:
				audioPackets++
				if err := audio.processAAC(packet.Data, lastVideoTimestampUS, handler, meta); err != nil {
					log.Printf("audio publish error: %v", err)
				}

			case baichuan.MediaPacketADPCM:
				audioPackets++
				if err := audio.processADPCM(packet.Data, lastVideoTimestampUS, handler, meta); err != nil {
					log.Printf("audio adpcm publish error: %v", err)
				}
			}

		case <-statsTicker.C:
			log.Printf("stats info=%d video=%d audio=%d video_bytes=%d rtsp_ready=%t audio_ready=%t", infoPackets, videoPackets, audioPackets, videoBytes, handler.ready(), audio.ready())
		}
	}
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

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
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
