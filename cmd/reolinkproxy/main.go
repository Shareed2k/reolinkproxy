// Package main provides the entry point for the reolinkproxy application.
package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	gortsplib "github.com/bluenviron/gortsplib/v4"
	"github.com/bluenviron/gortsplib/v4/pkg/description"
	"github.com/bluenviron/gortsplib/v4/pkg/format"
	"github.com/urfave/cli/v3"

	"github.com/bluenviron/gortsplib/v4/pkg/format/rtph264"
	"github.com/bluenviron/gortsplib/v4/pkg/format/rtph265"
	"github.com/pion/rtp"

	"github.com/shareed2k/reolinkproxy/pkg/baichuan"
)

var (
	Version = "dev"
	Commit  = "none"
	cfg     = defaultConfig()
)

func envVars(names ...string) cli.ValueSourceChain {
	prefixed := make([]string, len(names))
	for i, name := range names {
		prefixed[i] = "REOLINK_" + name
	}
	return cli.EnvVars(prefixed...)
}

func main() {
	cmd := &cli.Command{
		Name:                      "reolinkproxy",
		Usage:                     "restream reolink camera feeds as RTSP and ONVIF",
		UsageText:                 "reolinkproxy [options]\n\nExample camera env:\n  REOLINK_CAMERA_0_NAME=front \n  REOLINK_CAMERA_0_UID=123456 \n  REOLINK_CAMERA_0_HOST=192.168.1.10 \n  REOLINK_CAMERA_0_USERNAME=admin \n  REOLINK_CAMERA_0_PASSWORD=secret",
		Version:                   fmt.Sprintf("%s (commit: %s)", Version, Commit),
		DisableSliceFlagSeparator: true,
		Commands:                  []*cli.Command{newHealthcheckCommand()},
		Flags: []cli.Flag{
			&cli.StringFlag{
				Name:        "mqtt-broker",
				Usage:       "mqtt broker address",
				Sources:     envVars("MQTT_BROKER"),
				Value:       cfg.MQTT.Broker,
				Destination: &cfg.MQTT.Broker,
			},
			&cli.StringFlag{
				Name:        "mqtt-username",
				Usage:       "mqtt username",
				Sources:     envVars("MQTT_USERNAME"),
				Value:       cfg.MQTT.Username,
				Destination: &cfg.MQTT.Username,
			},
			&cli.StringFlag{
				Name:        "mqtt-password",
				Usage:       "mqtt password",
				Sources:     envVars("MQTT_PASSWORD"),
				Value:       cfg.MQTT.Password,
				Destination: &cfg.MQTT.Password,
			},
			&cli.StringFlag{
				Name:        "mqtt-topic",
				Usage:       "mqtt topic",
				Sources:     envVars("MQTT_TOPIC"),
				Value:       cfg.MQTT.Topic,
				Destination: &cfg.MQTT.Topic,
			},
			&cli.StringFlag{
				Name:        "server-rtsp-address",
				Usage:       "rtsp server listen address",
				Sources:     envVars("SERVER_RTSP_ADDRESS"),
				Value:       cfg.Server.RTSPAddress,
				Destination: &cfg.Server.RTSPAddress,
			},
			&cli.StringFlag{
				Name:        "server-rtp-address",
				Usage:       "rtp server listen address",
				Sources:     envVars("SERVER_RTP_ADDRESS"),
				Value:       cfg.Server.RTPAddress,
				Destination: &cfg.Server.RTPAddress,
			},
			&cli.StringFlag{
				Name:        "server-rtcp-address",
				Usage:       "rtcp server listen address",
				Sources:     envVars("SERVER_RTCP_ADDRESS"),
				Value:       cfg.Server.RTCPAddress,
				Destination: &cfg.Server.RTCPAddress,
			},
			&cli.StringFlag{
				Name:        "server-onvif-address",
				Usage:       "onvif server listen address",
				Sources:     envVars("SERVER_ONVIF_ADDRESS"),
				Value:       cfg.Server.ONVIFAddress,
				Destination: &cfg.Server.ONVIFAddress,
			},
			&cli.StringFlag{
				Name:        "server-advertise-host",
				Usage:       "advertise host for onvif and rtsp",
				Sources:     envVars("SERVER_ADVERTISE_HOST"),
				Value:       cfg.Server.AdvertiseHost,
				Destination: &cfg.Server.AdvertiseHost,
			},
			&cli.StringFlag{
				Name:        "server-log-level",
				Usage:       "log level (debug, info, warn, error)",
				Sources:     envVars("SERVER_LOG_LEVEL"),
				Value:       cfg.Server.LogLevel,
				Destination: &cfg.Server.LogLevel,
			},
			&cli.BoolFlag{
				Name:        "server-log-packets",
				Usage:       "enable packet logging",
				Sources:     envVars("SERVER_LOG_PACKETS"),
				Value:       cfg.Server.LogPackets,
				Destination: &cfg.Server.LogPackets,
			},
			&cli.StringFlag{
				Name:        "onvif-username",
				Usage:       "onvif server username",
				Sources:     envVars("ONVIF_USERNAME"),
				Value:       cfg.ONVIF.Username,
				Destination: &cfg.ONVIF.Username,
			},
			&cli.StringFlag{
				Name:        "onvif-password",
				Usage:       "onvif server password",
				Sources:     envVars("ONVIF_PASSWORD"),
				Value:       cfg.ONVIF.Password,
				Destination: &cfg.ONVIF.Password,
			},
		},
		Action: func(ctx context.Context, _ *cli.Command) error {
			if err := log.Configure(cfg.Server.LogLevel); err != nil {
				return err
			}

			envCameras, err := loadCamerasFromEnv()
			if err != nil {
				return fmt.Errorf("load cameras from environment: %w", err)
			}
			cfg.Cameras = envCameras

			if len(cfg.Cameras) == 0 {
				return fmt.Errorf("no cameras defined in environment")
			}

			return runApp(ctx, cfg)
		},
	}
	exitCode := 0
	if err := cmd.Run(context.Background(), os.Args); err != nil {
		log.Errorf("%v", err)
		exitCode = 1
	}
	log.Sync()
	if exitCode != 0 {
		os.Exit(exitCode)
	}
}

func signalContext(ctx context.Context) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(ctx)
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)

	go func() {
		select {
		case sig := <-sigCh:
			log.Printf("shutdown signal received signal=%s", sig)
			cancel()
		case <-ctx.Done():
		}
	}()

	return ctx, func() {
		signal.Stop(sigCh)
		cancel()
	}
}

func runApp(ctx context.Context, cfg *Config) error {
	ctx, cancel := signalContext(ctx)
	defer cancel()
	defer log.Printf("application stopped")

	serverHandler := newRTSPServerHandler()
	server := &gortsplib.Server{
		Handler:           serverHandler,
		RTSPAddress:       cfg.Server.RTSPAddress,
		UDPRTPAddress:     cfg.Server.RTPAddress,
		UDPRTCPAddress:    cfg.Server.RTCPAddress,
		WriteQueueSize:    4096,
		MulticastIPRange:  "224.1.0.0/16",
		MulticastRTPPort:  8000,
		MulticastRTCPPort: 8001,
	}
	serverHandler.server = server

	if err := server.Start(); err != nil {
		return fmt.Errorf("start rtsp server: %w", err)
	}
	defer server.Close()

	var metas []*streamMetadata

	// Initialize MQTT client once
	mqttClient, err := connectMQTT(cfg.MQTT)
	if err != nil {
		log.Printf("mqtt connect error: %v", err)
	}
	if mqttClient != nil {
		defer func() {
			mqttClient.Publish(fmt.Sprintf("%s/status", cfg.MQTT.Topic), 1, true, "offline").Wait()
			mqttClient.Disconnect(250)
		}()
	}

	// Connect to each camera and setup streams
	for _, camCfg := range cfg.Cameras {
		bcCfg := baichuan.Config{
			Host:     camCfg.Host,
			Port:     camCfg.Port,
			UID:      camCfg.UID,
			Username: camCfg.Username,
			Password: camCfg.Password,
			Timeout:  camCfg.Timeout,
		}
		clientManager := newCameraClientManager(camCfg.Name, bcCfg)
		if _, err := clientManager.Ensure(ctx); err != nil {
			log.Warnf("camera %s initial connect error: %v", camCfg.Name, err)
		}

		talkPath := talkPathForCamera(camCfg.RTSPPath)
		talkPublisher := newRTSPTalkPublisher(
			talkPath,
			camCfg.Name,
			uint8(camCfg.Channel),
			bcCfg,
			camCfg.TalkVolume,
			camCfg.TalkEncoder,
			camCfg.TalkEncoderCmd,
		)
		serverHandler.addTalk(talkPath, talkPublisher)
		log.Printf("talk path registered camera=%s path=%s", camCfg.Name, talkPath)

		var motionState *cameraMotionState
		if mqttClient != nil || camCfg.PauseOnMotion {
			motionState = newCameraMotionState()
			go runCameraMotionListener(ctx, clientManager, camCfg.Name, uint8(camCfg.Channel), motionState)
		}

		streamsList := splitCameraStreams(camCfg.Stream)
		preferredTalkProfile := camCfg.preferredTalkProfile()
		basePath := strings.TrimPrefix(camCfg.RTSPPath, "/")
		cameraMetas := make([]*streamMetadata, 0, len(streamsList))
		var (
			preferredMeta          *streamMetadata
			preferredHandler       *rtspStreamHandler
			preferredTwoWayHandler *rtspStreamHandler
		)
		for _, s := range streamsList {
			path := basePath
			if len(streamsList) > 1 {
				path = basePath + "_" + s
			}

			metaPath := path
			if len(streamsList) > 1 && preferredTalkProfile != "" && s == preferredTalkProfile {
				metaPath = basePath
			}

			meta := &streamMetadata{
				cameraName: camCfg.Name,
				name:       s,
				token:      onvifProfileToken(camCfg.Name, s),
				path:       metaPath,
			}
			if len(streamsList) > 1 && preferredTalkProfile != "" && s == preferredTalkProfile {
				preferredMeta = meta
			} else {
				cameraMetas = append(cameraMetas, meta)
			}

			streamHandler := newRTSPStreamHandler(path)
			streamHandler.attachServer(server)
			serverHandler.addStream(path, streamHandler)

			twoWayPath := twoWayPathForStream(path)
			twoWayHandler := newRTSPStreamHandler(twoWayPath)
			twoWayHandler.attachServer(server)
			twoWayHandler.setExtraMedias(newBackChannelMedia())
			streamHandler.addMirror(twoWayHandler)
			serverHandler.addStream(twoWayPath, twoWayHandler)
			serverHandler.addTalkAlias(twoWayPath, talkPublisher)

			if len(streamsList) > 1 && preferredTalkProfile != "" && s == preferredTalkProfile {
				preferredHandler = streamHandler
				preferredTwoWayHandler = twoWayHandler
			}

			log.Printf("stream registered camera=%s stream=%s path=%s", camCfg.Name, s, path)
			log.Printf("two-way stream registered camera=%s stream=%s path=%s", camCfg.Name, s, twoWayPath)

			go runStream(
				ctx,
				clientManager,
				uint8(camCfg.Channel),
				parseStream(s),
				streamHandler,
				meta,
				cfg.Server.LogPackets,
				camCfg.streamPauseConfig(motionState),
				camCfg.streamLifecycleConfig(),
			)
		}
		if preferredMeta != nil {
			metas = append(metas, preferredMeta)
		}
		metas = append(metas, cameraMetas...)
		if len(streamsList) > 1 && preferredHandler != nil {
			serverHandler.addStream(basePath, preferredHandler)
			log.Printf("stream alias registered camera=%s stream=%s path=%s", camCfg.Name, preferredTalkProfile, basePath)
			if preferredTwoWayHandler != nil {
				twoWayBasePath := twoWayPathForStream(basePath)
				serverHandler.addStream(twoWayBasePath, preferredTwoWayHandler)
				serverHandler.addTalkAlias(twoWayBasePath, talkPublisher)
				log.Printf("two-way stream alias registered camera=%s stream=%s path=%s", camCfg.Name, preferredTalkProfile, twoWayBasePath)
			}
		}

		if mqttClient != nil {
			registerCameraMQTT(ctx, mqttClient, cfg.MQTT, clientManager, camCfg.Name, uint8(camCfg.Channel), motionState)
		}
	}

	onvifCfg := onvifConfig{
		Address:         cfg.Server.ONVIFAddress,
		DevicePath:      "/onvif/device_service",
		MediaPath:       "/onvif/media_service",
		Media2Path:      "/onvif/media2_service",
		AdvertiseHost:   cfg.Server.AdvertiseHost,
		RTSPAddress:     cfg.Server.RTSPAddress,
		RTSPPath:        "", // Extracted per-camera in onvif
		DeviceName:      "ReolinkProxy",
		Manufacturer:    "ReolinkProxy",
		Model:           "Multi-Camera NVR",
		FirmwareVersion: Version,
		SerialNumber:    "reolinkproxy-nvr",
		HardwareID:      "reolinkproxy",
		Username:        cfg.ONVIF.Username,
		Password:        cfg.ONVIF.Password,
	}

	startWSDiscovery(onvifCfg)

	onvifServer := &http.Server{
		Addr:              onvifCfg.Address,
		Handler:           newONVIFHandler(onvifCfg, metas),
		ReadHeaderTimeout: 5 * time.Second,
	}
	serverErrCh := make(chan error, 1)
	go func() {
		if err := onvifServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			serverErrCh <- fmt.Errorf("start onvif server: %w", err)
			cancel()
		}
	}()
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		log.Debugf("onvif server shutting down")
		if err := onvifServer.Shutdown(shutdownCtx); err != nil {
			log.Printf("onvif server shutdown error: %v", err)
		}
	}()

	log.Printf("rtsp server listening at %s", cfg.Server.RTSPAddress)
	log.Printf("onvif device service listening at %s%s", cfg.Server.ONVIFAddress, onvifCfg.DevicePath)

	select {
	case <-ctx.Done():
		log.Printf("application shutdown started: %v", ctx.Err())
		return nil
	case err := <-serverErrCh:
		return err
	}
}

//nolint:gocyclo
func runStream(
	ctx context.Context,
	clientManager *cameraClientManager,
	channel uint8,
	stream baichuan.Stream,
	handler *rtspStreamHandler,
	meta *streamMetadata,
	logPackets bool,
	pauseCfg streamPauseConfig,
	lifecycleCfg streamLifecycleConfig,
) {
	var (
		infoPackets          uint64
		videoPackets         uint64
		audioPackets         uint64
		videoBytes           uint64
		firstVideo           bool
		videoFormat          format.Format
		videoEncoder         interface{}

		lastVideoPackets uint64
		stalledDuration  time.Duration
		paused           bool
		pauseReason      string
		reader           *baichuan.MediaReader
		readerPackets    <-chan baichuan.MediaPacket
		previewClient    *baichuan.Client
		idleSince        time.Time
		lastPacketAt     time.Time
		lastVideoAt      time.Time
		nextReconnectAt  time.Time
		reconnectDelay   = 50 * time.Millisecond
		frameCount       int
		highestContinuousUS uint64
		continuousUS        uint64
	)

	videoMedia := &description.Media{
		Type:    description.MediaTypeVideo,
		Control: "trackID=0",
	}

	audio := &audioPublisher{}
	startupDeadline := time.Now().Add(2 * time.Second)

	statsTicker := time.NewTicker(5 * time.Second)
	defer statsTicker.Stop()
	controlTicker := time.NewTicker(time.Second)
	defer controlTicker.Stop()

	updatePauseState := func(now time.Time) bool {
		nextPaused, nextReason := pauseCfg.shouldPause(now, handler)
		if nextPaused != paused || nextReason != pauseReason {
			if nextPaused {
				log.Printf("stream %s paused: %s", meta.name, nextReason)
			} else if paused {
				log.Printf("stream %s resumed", meta.name)
			}
			paused = nextPaused
			pauseReason = nextReason
		}
		return paused
	}

	scheduleReconnect := func(now time.Time) {
		delay := reconnectDelay
		maxDelay := lifecycleCfg.maxReconnectDelay()
		nextReconnectAt = now.Add(delay)
		reconnectDelay *= 2
		if reconnectDelay > maxDelay {
			reconnectDelay = maxDelay
		}
		log.Debugf("stream %s reconnect scheduled delay=%v next=%s", meta.name, delay, nextReconnectAt.Format(time.RFC3339Nano))
	}

	startPreview := func(now time.Time) {
		if !nextReconnectAt.IsZero() && now.Before(nextReconnectAt) {
			log.Debugf("stream %s reconnect waiting until %s", meta.name, nextReconnectAt.Format(time.RFC3339Nano))
			return
		}

		client, err := clientManager.Ensure(ctx)
		if err != nil {
			log.Warnf("connect camera %s stream %s: %v", meta.cameraName, meta.name, err)
			scheduleReconnect(now)
			return
		}

		newReader, err := client.StartPreview(ctx, channel, stream)
		if err != nil {
			log.Printf("start preview for camera %s stream %s: %v", meta.cameraName, meta.name, err)
			if closeErr := client.Err(); closeErr != nil {
				clientManager.ResetIfCurrent(client, fmt.Sprintf("preview start failed: %v", closeErr))
			}
			scheduleReconnect(now)
			return
		}

		previewClient = client
		reader = newReader
		readerPackets = newReader.Packets
		reconnectDelay = 50 * time.Millisecond
		nextReconnectAt = time.Time{}
		startupDeadline = time.Now().Add(2 * time.Second)
		idleSince = time.Time{}
		lastPacketAt = time.Time{}
		lastVideoAt = time.Time{}

		log.Printf("preview started camera=%s stream=%s path=%s", meta.cameraName, meta.name, meta.path)
	}

	stopPreview := func(reason string) {
		if reader == nil {
			return
		}

		log.Printf("preview stopped camera=%s stream=%s reason=%s", meta.cameraName, meta.name, reason)
		reader.Close()
		reader = nil
		readerPackets = nil
		previewClient = nil
		stalledDuration = 0
		lastVideoPackets = videoPackets
		idleSince = time.Time{}
		lastPacketAt = time.Time{}
		lastVideoAt = time.Time{}
	}

	maintainPreview := func(now time.Time) {
		wantsPreview := !lifecycleCfg.IdleDisconnect || !handler.ready() || handler.hasClients()
		if wantsPreview {
			idleSince = time.Time{}
			if reader == nil {
				startPreview(now)
			}
			return
		}

		if reader == nil {
			return
		}

		if idleSince.IsZero() {
			idleSince = now
			return
		}

		if now.Sub(idleSince) >= lifecycleCfg.IdleTimeout {
			stopPreview("idle disconnect")
		}
	}

	for {
		select {
		case <-ctx.Done():
			stopPreview("shutdown")
			return

		case packet, ok := <-readerPackets:
			if !ok {
				log.Debugf("stream %s packet reader closed", meta.name)
				reader = nil
				readerPackets = nil
				if previewClient != nil {
					if err := previewClient.Err(); err != nil && ctx.Err() == nil {
						log.Printf("stream %s preview closed: %v", meta.name, err)
						clientManager.ResetIfCurrent(previewClient, fmt.Sprintf("preview closed: %v", err))
					}
				}
				previewClient = nil
				scheduleReconnect(time.Now())
				continue
			}
			lastPacketAt = time.Now()

			continuousUS = unwrapTimestamp(packet.TimestampMicrosecs, highestContinuousUS)
			if continuousUS > highestContinuousUS {
				highestContinuousUS = continuousUS
			}

			switch packet.Kind {
			case baichuan.MediaPacketInfoV1, baichuan.MediaPacketInfoV2:
				infoPackets++
				meta.setVideoInfo(packet.Width, packet.Height, packet.FPS, "")
				log.Printf("stream %s info size=%dx%d fps=%d", meta.name, packet.Width, packet.Height, packet.FPS)

			case baichuan.MediaPacketIFrame, baichuan.MediaPacketPFrame:
				lastVideoAt = lastPacketAt
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

				if videoFormat == nil {
					meta.setVideoCodec(packet.Codec)
					if packet.Codec == "H265" {
						h265Format := &format.H265{PayloadTyp: 96}
						videoFormat = h265Format
						enc, err := h265Format.CreateEncoder()
						if err != nil {
							log.Printf("stream %s create h265 encoder: %v", meta.name, err)
							return
						}
						videoEncoder = enc
					} else {
						h264Format := &format.H264{PayloadTyp: 96}
						videoFormat = h264Format
						enc, err := h264Format.CreateEncoder()
						if err != nil {
							log.Printf("stream %s create h264 encoder: %v", meta.name, err)
							return
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
						log.Printf("stream %s prepare rtsp stream: %v", meta.name, err)
						return
					}
				}

				streamPaused := updatePauseState(time.Now())

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
					log.Printf("stream %s encode rtp: %v", meta.name, err)
					return
				}

				ts := rtpTimestampForClock(continuousUS, clockRate)
				if !streamPaused {
					for _, pkt := range pkts {
						pkt.Timestamp = ts
						handler.writePacket(videoMedia, pkt)
					}
				}

				videoPackets++
				frameCount++
				videoBytes += uint64(len(packet.Data))

				if !firstVideo || logPackets {
					firstVideo = true
					log.Printf("stream %s video packet kind=%s codec=%s nalus=%d bytes=%d ts_us=%d", meta.name, packet.Kind, packet.Codec, len(nalus), len(packet.Data), packet.TimestampMicrosecs)
				}

			case baichuan.MediaPacketAAC:
				audioPackets++
				if err := audio.processAAC(packet.Data, continuousUS, handler, meta, !updatePauseState(time.Now())); err != nil {
					log.Printf("stream %s audio publish error: %v", meta.name, err)
				}

			case baichuan.MediaPacketADPCM:
				audioPackets++
				if err := audio.processADPCM(packet.Data, continuousUS, handler, meta, !updatePauseState(time.Now())); err != nil {
					log.Printf("stream %s audio adpcm publish error: %v", meta.name, err)
				}
			}

		case <-statsTicker.C:
			now := time.Now()
			maintainPreview(now)
			updatePauseState(now)
			lastPacketAge := time.Duration(0)
			if !lastPacketAt.IsZero() {
				lastPacketAge = now.Sub(lastPacketAt)
			}
			lastVideoAge := time.Duration(0)
			if !lastVideoAt.IsZero() {
				lastVideoAge = now.Sub(lastVideoAt)
			}
			log.Debugf("stream %s stats info=%d video=%d audio=%d video_bytes=%d rtsp_ready=%t audio_ready=%t preview_active=%t has_clients=%t last_packet_age=%v last_video_age=%v", meta.name, infoPackets, videoPackets, audioPackets, videoBytes, handler.ready(), audio.ready(), reader != nil, handler.hasClients(), lastPacketAge, lastVideoAge)

			if reader != nil && videoPackets == lastVideoPackets {
				stalledDuration += 5 * time.Second
				if stalledDuration >= 15*time.Second {
					stallFor := stalledDuration
					log.Printf("stream %s stalled for %v, reconnecting camera session", meta.name, stallFor)
					stalledClient := previewClient
					stopPreview("stalled")
					if stalledClient != nil {
						clientManager.ResetIfCurrent(stalledClient, fmt.Sprintf("stream %s stalled for %v", meta.name, stallFor))
					}
					scheduleReconnect(now)
				}
			} else {
				stalledDuration = 0
			}
			lastVideoPackets = videoPackets

		case <-controlTicker.C:
			maintainPreview(time.Now())
			updatePauseState(time.Now())
		}
	}
}

func unwrapTimestamp(ts32 uint32, highest64 uint64) uint64 {
	if highest64 == 0 {
		return uint64(ts32)
	}

	high32 := highest64 >> 32
	cand1 := (high32 << 32) | uint64(ts32)
	
	cand2 := cand1
	if cand1 >= 0x100000000 {
		cand2 = cand1 - 0x100000000
	}
	
	cand3 := cand1 + 0x100000000

	absDiff := func(a, b uint64) uint64 {
		if a > b {
			return a - b
		}
		return b - a
	}

	bestCand := cand1
	bestDiff := absDiff(cand1, highest64)

	if diff2 := absDiff(cand2, highest64); diff2 < bestDiff {
		bestCand = cand2
		bestDiff = diff2
	}
	if diff3 := absDiff(cand3, highest64); diff3 < bestDiff {
		bestCand = cand3
	}

	return bestCand
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
