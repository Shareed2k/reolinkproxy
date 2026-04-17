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

	"github.com/bluenviron/gortsplib/v4/pkg/format/rtph264"
	"github.com/bluenviron/gortsplib/v4/pkg/format/rtph265"
	"github.com/pion/rtp"

	"github.com/shareed2k/reolinkproxy/pkg/baichuan"
)

var (
	Version = "dev"
	Commit  = "none"
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
	rtspPath := envString("RTSP_PATH", "Camera01/stream")
	onvifAddress := envString("ONVIF_ADDRESS", ":8002")
	onvifDevicePath := envString("ONVIF_DEVICE_PATH", "/onvif/device_service")
	onvifMediaPath := envString("ONVIF_MEDIA_PATH", "/onvif/media_service")
	advertiseHost := envString("ADVERTISE_HOST", "")

	onvifUsername := envString("ONVIF_USERNAME", "admin")
	onvifPassword := envString("ONVIF_PASSWORD", "")

	mqttBroker := envString("MQTT_BROKER", "")
	mqttUsername := envString("MQTT_USERNAME", "")
	mqttPassword := envString("MQTT_PASSWORD", "")
	mqttTopic := envString("MQTT_TOPIC", "reolinkproxy") // Default to reolinkproxy

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
	flag.StringVar(&mqttBroker, "mqtt-broker", mqttBroker, "MQTT broker URL (tcp://192.168.1.10:1883)")
	flag.StringVar(&mqttUsername, "mqtt-username", mqttUsername, "MQTT username")
	flag.StringVar(&mqttPassword, "mqtt-password", mqttPassword, "MQTT password")
	flag.StringVar(&mqttTopic, "mqtt-topic", mqttTopic, "MQTT root topic (defaults to reolinkproxy)")
	flag.BoolVar(&logPackets, "log-packets", false, "log every parsed video packet")
	showVersion := flag.Bool("version", false, "print version and exit")
	flag.Parse()

	if *showVersion {
		log.Printf("reolinkproxy version=%s commit=%s", Version, Commit)
		os.Exit(0)
	}

	if cameraCfg.Host == "" && cameraCfg.UID == "" {
		log.Fatal("set -host or -uid")
	}
	if cameraCfg.Username == "" || cameraCfg.Password == "" {
		log.Fatal("set -username and -password")
	}

	streamsList := strings.Split(stream, ",")
	var streamsToStart []string
	for _, s := range streamsList {
		if s := strings.TrimSpace(s); s != "" {
			streamsToStart = append(streamsToStart, s)
		}
	}
	if len(streamsToStart) == 0 {
		streamsToStart = []string{"main"}
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

	rtspPath = strings.TrimPrefix(rtspPath, "/")
	serverHandler := newRTSPServerHandler()

	server := &gortsplib.Server{
		Handler:        serverHandler,
		RTSPAddress:    rtspAddress,
		UDPRTPAddress:  rtpAddress,
		UDPRTCPAddress: rtcpAddress,
		WriteQueueSize: 2048,
	}

	if err := server.Start(); err != nil {
		log.Fatalf("start rtsp server: %v", err)
	}
	defer server.Close()

	var metas []*streamMetadata

	for _, stName := range streamsToStart {
		reader, err := client.StartPreview(ctx, uint8(channel), parseStream(stName))
		if err != nil {
			log.Fatalf("start preview for %s: %v", stName, err)
		}

		path := rtspPath
		if len(streamsToStart) > 1 {
			path = rtspPath + "_" + stName
		}

		meta := &streamMetadata{name: stName, path: path}
		metas = append(metas, meta)

		streamHandler := newRTSPStreamHandler(path)
		streamHandler.attachServer(server)
		serverHandler.addStream(path, streamHandler)

		log.Printf("preview started transport=%s channel=%d stream=%s path=%s", transportName(cameraCfg), channel, stName, path)

		go runStream(ctx, reader, client, streamHandler, meta, logPackets)
	}

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
		FirmwareVersion: envString("DEVICE_FIRMWARE_VERSION", Version),
		SerialNumber:    envString("DEVICE_SERIAL_NUMBER", firstNonEmpty(cameraCfg.UID, cameraCfg.Host, "unknown")),
		HardwareID:      envString("DEVICE_HARDWARE_ID", "reolinkproxy"),
		ProfileToken:    envString("ONVIF_PROFILE_TOKEN", profileTokenFromPath(rtspPath)),
		Username:        onvifUsername,
		Password:        onvifPassword,
	}

	startWSDiscovery(onvifCfg)

	onvifServer := &http.Server{
		Addr:              onvifAddress,
		Handler:           newONVIFHandler(onvifCfg, metas),
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

	if mqttBroker != "" {
		mqttCfg := mqttConfig{
			Broker:   mqttBroker,
			Username: mqttUsername,
			Password: mqttPassword,
			Topic:    mqttTopic,
		}
		if err := startMQTT(ctx, mqttCfg, client, onvifCfg.DeviceName, uint8(channel)); err != nil {
			log.Printf("mqtt start error: %v", err)
		}
	}

	<-ctx.Done()
}

func runStream(ctx context.Context, reader *baichuan.MediaReader, client *baichuan.Client, handler *rtspStreamHandler, meta *streamMetadata, logPackets bool) {
	var (
		infoPackets          uint64
		videoPackets         uint64
		audioPackets         uint64
		videoBytes           uint64
		firstVideo           bool
		lastVideoTimestampUS uint32
		videoFormat          format.Format
		videoEncoder         interface{}

		lastVideoPackets uint64
		stalledDuration  time.Duration
	)

	videoMedia := &description.Media{
		Type:    description.MediaTypeVideo,
		Control: "trackID=0",
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
				log.Fatalf("stream %s preview closed: %v", meta.name, client.Err())
			}

			switch packet.Kind {
			case baichuan.MediaPacketInfoV1, baichuan.MediaPacketInfoV2:
				infoPackets++
				meta.setVideoInfo(packet.Width, packet.Height, packet.FPS, "")
				log.Printf("stream %s info size=%dx%d fps=%d", meta.name, packet.Width, packet.Height, packet.FPS)

			case baichuan.MediaPacketIFrame, baichuan.MediaPacketPFrame:
				if packet.Codec != "H265" && packet.Codec != "H264" {
					if !firstVideo {
						log.Printf("stream %s skipping unsupported codec %q", meta.name, packet.Codec)
					}
					continue
				}

				nalus := splitAnnexB(packet.Data)
				if len(nalus) == 0 {
					continue
				}
				lastVideoTimestampUS = packet.TimestampMicrosecs

				if videoFormat == nil {
					meta.setVideoCodec(packet.Codec)
					if packet.Codec == "H265" {
						h265Format := &format.H265{PayloadTyp: 96}
						videoFormat = h265Format
						enc, err := h265Format.CreateEncoder()
						if err != nil {
							log.Fatalf("stream %s create h265 encoder: %v", meta.name, err)
						}
						videoEncoder = enc
					} else {
						h264Format := &format.H264{PayloadTyp: 96}
						videoFormat = h264Format
						enc, err := h264Format.CreateEncoder()
						if err != nil {
							log.Fatalf("stream %s create h264 encoder: %v", meta.name, err)
						}
						videoEncoder = enc
					}
					videoMedia.Formats = []format.Format{videoFormat}
				}

				var readyToExpose bool
				var clockRate int

				if packet.Codec == "H265" {
					h265Format := videoFormat.(*format.H265)
					clockRate = h265Format.ClockRate()
					vps, sps, pps := extractH265Params(nalus)
					if vps != nil || sps != nil || pps != nil {
						h265Format.SafeSetParams(coalesce(vps, h265Format.VPS), coalesce(sps, h265Format.SPS), coalesce(pps, h265Format.PPS))
					}
					curVPS, curSPS, curPPS := h265Format.SafeParams()
					readyToExpose = curVPS != nil && curSPS != nil && curPPS != nil
				} else {
					h264Format := videoFormat.(*format.H264)
					clockRate = h264Format.ClockRate()
					sps, pps := extractH264Params(nalus)
					if sps != nil || pps != nil {
						h264Format.SafeSetParams(coalesce(sps, h264Format.SPS), coalesce(pps, h264Format.PPS))
					}
					curSPS, curPPS := h264Format.SafeParams()
					readyToExpose = curSPS != nil && curPPS != nil
				}

				if !handler.ready() {
					if !readyToExpose {
						if packet.Kind == baichuan.MediaPacketIFrame && logPackets {
							log.Printf("stream %s waiting for parameter sets before exposing RTSP path", meta.name)
						}
						continue
					}
					if audio.awaitingStartupDecision(startupDeadline) {
						continue
					}

					if err := handler.setReady(videoMedia, audio.mediaDescription()); err != nil {
						log.Fatalf("stream %s prepare rtsp stream: %v", meta.name, err)
					}
				}

				var pkts []*rtp.Packet
				var err error
				if packet.Codec == "H265" {
					pkts, err = videoEncoder.(*rtph265.Encoder).Encode(nalus)
					if err == nil {
						fixH265AggregationTemporalID(pkts)
					}
				} else {
					pkts, err = videoEncoder.(*rtph264.Encoder).Encode(nalus)
				}

				if err != nil {
					log.Fatalf("stream %s encode rtp: %v", meta.name, err)
				}

				ts := rtpTimestampForClock(packet.TimestampMicrosecs, clockRate)
				for _, pkt := range pkts {
					pkt.Timestamp = ts
					handler.writePacket(videoMedia, pkt)
				}

				videoPackets++
				videoBytes += uint64(len(packet.Data))
				if !firstVideo || logPackets {
					firstVideo = true
					log.Printf("stream %s video packet kind=%s codec=%s nalus=%d bytes=%d ts_us=%d", meta.name, packet.Kind, packet.Codec, len(nalus), len(packet.Data), packet.TimestampMicrosecs)
				}

			case baichuan.MediaPacketAAC:
				audioPackets++
				if err := audio.processAAC(packet.Data, lastVideoTimestampUS, handler, meta); err != nil {
					log.Printf("stream %s audio publish error: %v", meta.name, err)
				}

			case baichuan.MediaPacketADPCM:
				audioPackets++
				if err := audio.processADPCM(packet.Data, lastVideoTimestampUS, handler, meta); err != nil {
					log.Printf("stream %s audio adpcm publish error: %v", meta.name, err)
				}
			}

		case <-statsTicker.C:
			log.Printf("stream %s stats info=%d video=%d audio=%d video_bytes=%d rtsp_ready=%t audio_ready=%t", meta.name, infoPackets, videoPackets, audioPackets, videoBytes, handler.ready(), audio.ready())

			if videoPackets == lastVideoPackets {
				stalledDuration += 5 * time.Second
				if stalledDuration >= 15*time.Second {
					log.Fatalf("stream %s stalled for %v, restarting proxy to recover", meta.name, stalledDuration)
				}
			} else {
				stalledDuration = 0
			}
			lastVideoPackets = videoPackets
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
