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

	_ "net/http/pprof"

	gortsplib "github.com/bluenviron/gortsplib/v5"
	"github.com/bluenviron/gortsplib/v5/pkg/description"
	"github.com/bluenviron/gortsplib/v5/pkg/format"
	"github.com/urfave/cli/v3"

	"github.com/bluenviron/gortsplib/v5/pkg/format/rtph264"
	"github.com/bluenviron/gortsplib/v5/pkg/format/rtph265"
	"github.com/pion/rtp"

	"github.com/shareed2k/reolinkproxy/pkg/baichuan"
	"github.com/shareed2k/reolinkproxy/pkg/media"
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
				Name:        "server-pprof-address",
				Usage:       "pprof server listen address (e.g. :6060)",
				Sources:     envVars("SERVER_PPROF_ADDRESS"),
				Value:       cfg.Server.PprofAddress,
				Destination: &cfg.Server.PprofAddress,
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
			&cli.IntFlag{
				Name:        "server-audio-pacer-initial-latency-ms",
				Usage:       "RTSP audio pacer startup delay in ms (smooths bursts; default 1500, matched to the video pacer)",
				Sources:     envVars("SERVER_AUDIO_PACER_INITIAL_LATENCY_MS"),
				Value:       cfg.Server.AudioPacerInitialLatencyMs,
				Destination: &cfg.Server.AudioPacerInitialLatencyMs,
			},
			&cli.IntFlag{
				Name:        "server-audio-pacer-max-lead-ms",
				Usage:       "max audio pacer cursor lead over wall clock in ms before snapping (default 2000)",
				Sources:     envVars("SERVER_AUDIO_PACER_MAX_LEAD_MS"),
				Value:       cfg.Server.AudioPacerMaxLeadMs,
				Destination: &cfg.Server.AudioPacerMaxLeadMs,
			},
			&cli.BoolFlag{
				Name:        "server-audio-pacer-snap-on-past",
				Usage:       "when the audio pacer cursor is behind wall clock, snap to now (default true)",
				Sources:     envVars("SERVER_AUDIO_PACER_SNAP_ON_PAST"),
				Value:       cfg.Server.AudioPacerSnapOnPast,
				Destination: &cfg.Server.AudioPacerSnapOnPast,
			},
			&cli.IntFlag{
				Name:        "server-video-pacer-initial-latency-ms",
				Usage:       "RTSP video pacer startup delay in ms (default 1500)",
				Sources:     envVars("SERVER_VIDEO_PACER_INITIAL_LATENCY_MS"),
				Value:       cfg.Server.VideoPacerInitialLatencyMs,
				Destination: &cfg.Server.VideoPacerInitialLatencyMs,
			},
			&cli.IntFlag{
				Name:        "server-video-pacer-max-lead-ms",
				Usage:       "max video pacer cursor lead over wall clock in ms before snapping (default 3000)",
				Sources:     envVars("SERVER_VIDEO_PACER_MAX_LEAD_MS"),
				Value:       cfg.Server.VideoPacerMaxLeadMs,
				Destination: &cfg.Server.VideoPacerMaxLeadMs,
			},
			&cli.BoolFlag{
				Name:        "server-video-pacer-snap-on-past",
				Usage:       "when the video pacer cursor is behind wall clock, snap to now (default false)",
				Sources:     envVars("SERVER_VIDEO_PACER_SNAP_ON_PAST"),
				Value:       cfg.Server.VideoPacerSnapOnPast,
				Destination: &cfg.Server.VideoPacerSnapOnPast,
			},
			&cli.BoolFlag{
				Name:        "server-disable-rtcp-sender-reports",
				Usage:       "omit periodic RTCP Sender Reports (default false; SR carries camera-anchored NTP for A/V sync)",
				Sources:     envVars("SERVER_DISABLE_RTCP_SENDER_REPORTS"),
				Value:       cfg.Server.DisableRTCPSenderReports,
				Destination: &cfg.Server.DisableRTCPSenderReports,
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

func setupCameraStreams(
	ctx context.Context,
	cfg *Config,
	camCfg CameraConfig,
	device *CameraDevice,
	serverHandler *rtspServerHandler,
	talkPublisher *rtspTalkPublisher,
	motionState *cameraMotionState,
) []*streamMetadata {
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
			cameraName:           camCfg.Name,
			name:                 s,
			token:                onvifProfileToken(camCfg.Name, s),
			path:                 metaPath,
			device:               device,
			channel:              camCfg.channelID(),
			ptzRelativeMsPerUnit: camCfg.PTZRelativeMsPerUnit,
		}
		if len(streamsList) > 1 && preferredTalkProfile != "" && s == preferredTalkProfile {
			preferredMeta = meta
		} else {
			cameraMetas = append(cameraMetas, meta)
		}

		streamHandler := newRTSPStreamHandler(path)
		streamHandler.attachServer(serverHandler.server)
		serverHandler.addStream(path, streamHandler)
		meta.rtspHandler = streamHandler

		twoWayPath := twoWayPathForStream(path)
		twoWayHandler := newRTSPStreamHandler(twoWayPath)
		twoWayHandler.attachServer(serverHandler.server)
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
			device,
			camCfg.channelID(),
			parseStream(s),
			streamHandler,
			meta,
			cfg.Server,
			camCfg.streamPauseConfig(motionState),
		)
	}

	metas := make([]*streamMetadata, 0, len(cameraMetas)+1)
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
	return metas
}

func runApp(ctx context.Context, cfg *Config) error {
	ctx, cancel := signalContext(ctx)
	defer cancel()
	defer log.Printf("application stopped")

	if cfg.Server.PprofAddress != "" {
		go func() {
			log.Printf("starting pprof server on %s", cfg.Server.PprofAddress)
			if err := http.ListenAndServe(cfg.Server.PprofAddress, nil); err != nil {
				log.Warnf("pprof server error: %v", err)
			}
		}()
	}

	serverHandler := newRTSPServerHandler()
	server := &gortsplib.Server{
		Handler:                  serverHandler,
		RTSPAddress:              cfg.Server.RTSPAddress,
		UDPRTPAddress:            cfg.Server.RTPAddress,
		UDPRTCPAddress:           cfg.Server.RTCPAddress,
		DisableRTCPSenderReports: cfg.Server.DisableRTCPSenderReports,
		WriteQueueSize:           4096,
		MulticastIPRange:         "224.1.0.0/16",
		MulticastRTPPort:         8000,
		MulticastRTCPPort:        8001,
	}
	serverHandler.server = server

	if err := server.Start(); err != nil {
		return fmt.Errorf("start rtsp server: %w", err)
	}
	defer server.Close()
	if cfg.Server.DisableRTCPSenderReports {
		log.Printf("periodic RTCP Sender Reports: disabled")
	} else {
		log.Printf("periodic RTCP Sender Reports: enabled")
	}

	var metas []*streamMetadata
	eventManager := newONVIFEventManager()

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
		device := NewCameraDevice(camCfg.Name, bcCfg)
		if _, err := device.Ensure(ctx); err != nil {
			log.Warnf("camera %s initial connect error: %v", camCfg.Name, err)
		}

		talkPath := talkPathForCamera(camCfg.RTSPPath)
		talkPublisher := newRTSPTalkPublisher(
			talkPath,
			camCfg.Name,
			camCfg.channelID(),
			device,
			camCfg.TalkVolume,
			camCfg.TalkEncoder,
			camCfg.TalkEncoderCmd,
		)
		serverHandler.addTalk(talkPath, talkPublisher)
		log.Printf("talk path registered camera=%s path=%s", camCfg.Name, talkPath)

		motionState := newCameraMotionState()
		// The ONVIF event service consumes motion too, so the listener runs
		// for every powered camera; battery cameras keep the old opt-in
		// behavior (MQTT or pause-on-motion) so motion polling cannot keep
		// them awake unrequested.
		if mqttClient != nil || camCfg.PauseOnMotion || !camCfg.BatteryCamera {
			device.WatchMotion(ctx, camCfg.channelID(), func(ev baichuan.MotionEvent) {
				motionState.setDetection(ev.Active, ev.AITypes)
			}, motionState.markUnsupported)
		}
		eventManager.watchCamera(camCfg.Name, motionState)

		camMetas := setupCameraStreams(ctx, cfg, camCfg, device, serverHandler, talkPublisher, motionState)
		metas = append(metas, camMetas...)

		if mqttClient != nil {
			registerCameraMQTT(ctx, mqttClient, cfg.MQTT, device, camCfg.Name, camCfg.channelID(), motionState, camCfg.BatteryCamera)
		}
	}

	onvifCfg := onvifConfig{
		Address:         cfg.Server.ONVIFAddress,
		DevicePath:      "/onvif/device_service",
		MediaPath:       "/onvif/media_service",
		Media2Path:      "/onvif/media2_service",
		PTZPath:         "/onvif/ptz_service",
		EventPath:       "/onvif/event_service",
		ImagingPath:     "/onvif/imaging_service",
		AnalyticsPath:   "/onvif/analytics_service",
		RecordingPath:   "/onvif/recording_service",
		SearchPath:      "/onvif/search_service",
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
		Handler:           newONVIFHandler(onvifCfg, metas, eventManager),
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
	device *CameraDevice,
	channel uint8,
	stream baichuan.Stream,
	handler *rtspStreamHandler,
	meta *streamMetadata,
	server ServerConfig,
	pauseCfg streamPauseConfig,
) {
	logPackets := server.LogPackets
	var (
		infoPackets      uint64
		videoPackets     uint64
		audioPackets     uint64
		videoBytes       uint64
		firstVideo       bool
		videoFormat      format.Format
		videoEncoder     any
		paused           bool
		pauseReason      string
		lastPacketAt     time.Time
		lastVideoAt      time.Time
		frameCount       int
		streamTimestamps timestampUnwrapper
		videoRTP         rtpTimestampGuard

		codecMismatchLogged bool
		resumeNeedsKeyframe bool
	)

	videoMedia := &description.Media{
		Type:    description.MediaTypeVideo,
		Control: "trackID=0",
	}

	// The audio-decision window opens with the first media packet, not process
	// start: slow connects (UID discovery, battery wake) would otherwise burn
	// the whole window before any packet arrives and publish streams
	// video-only even though the camera sends audio moments later.
	var startupDeadline time.Time

	var videoPace videoPaceState
	videoPacer := &mediaPacer{
		ch:             make(chan pacedFrame, 400),
		maxLead:        server.videoPacerMaxLead(),
		initialLatency: server.videoPacerInitialLatency(),
		snapOnPast:     server.VideoPacerSnapOnPast,
		handler:        handler,
	}
	go videoPacer.run(ctx)

	audioPacer := &mediaPacer{
		ch:             make(chan pacedFrame, 200),
		maxLead:        server.audioPacerMaxLead(),
		initialLatency: server.audioPacerInitialLatency(),
		snapOnPast:     server.AudioPacerSnapOnPast,
		handler:        handler,
	}
	go audioPacer.run(ctx)

	audio := &audioPublisher{audioPacer: audioPacer}

	emitVideo := func(pkts []*rtp.Packet, continuousUS uint64) {
		if len(pkts) == 0 {
			return
		}
		dur := videoPace.durationForFrame(continuousUS)
		videoPacer.enqueue(pacedFrame{pkts: pkts, media: videoMedia, duration: dur, ntp: ntpFromMicros(continuousUS)})
	}

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

	meta.startedAtMicro.Store(time.Now().UnixMicro())
	streamCh := device.StreamPackets(ctx, channel, stream)

	for {
		select {
		case <-ctx.Done():
			return

		case packet, ok := <-streamCh:
			if !ok {
				return
			}
			lastPacketAt = time.Now()
			if startupDeadline.IsZero() {
				startupDeadline = lastPacketAt.Add(2 * time.Second)
			}

			switch packet.Kind {
			case baichuan.MediaPacketInfoV1, baichuan.MediaPacketInfoV2:
				infoPackets++
				meta.setVideoInfo(packet.Width, packet.Height, packet.FPS, "")
				log.Printf("stream %s info size=%dx%d fps=%d", meta.name, packet.Width, packet.Height, packet.FPS)

			case baichuan.MediaPacketIFrame, baichuan.MediaPacketPFrame:
				lastVideoAt = lastPacketAt
				meta.lastVideoAtMicro.Store(lastPacketAt.UnixMicro())
				if packet.Codec != "H265" && packet.Codec != "H264" {
					if !firstVideo {
						log.Printf("stream %s skipping unsupported codec %q", meta.name, packet.Codec)
					}
					continue
				}

				nalus := media.SplitAnnexB(packet.Data)
				switch packet.Codec {
				case "H265":
					nalus = media.FilterH265DecodableNALs(nalus)
					nalus = media.ReorderH265NALsForAccessUnit(nalus)
				case "H264":
					nalus = media.ReorderH264NALsForAccessUnit(nalus)
				}
				if len(nalus) == 0 {
					continue
				}
				if !packet.HasTimestamp {
					log.Printf("stream %s skipping video packet without timestamp", meta.name)
					continue
				}
				continuousUS := streamTimestamps.unwrap(packet.TimestampMicrosecs)

				// The RTP format and encoder are negotiated from the first
				// frame; a mid-stream codec flip would hit unchecked type
				// assertions below, so drop such frames instead of panicking.
				if videoFormat != nil {
					_, isH265 := videoFormat.(*format.H265)
					if (packet.Codec == "H265") != isH265 {
						if !codecMismatchLogged {
							codecMismatchLogged = true
							log.Printf("stream %s dropping %s frame: stream already negotiated a different codec (mid-stream codec changes require a reconnect)", meta.name, packet.Codec)
						}
						continue
					}
				}

				if videoFormat == nil {
					meta.setVideoCodec(packet.Codec)
					switch packet.Codec {
					case "H265":
						h265Format := &format.H265{PayloadTyp: 96}
						videoFormat = h265Format
						enc, err := h265Format.CreateEncoder()
						if err != nil {
							log.Printf("stream %s create h265 encoder: %v", meta.name, err)
							videoFormat = nil
							continue
						}
						videoEncoder = enc
					default:
						h264Format := &format.H264{PayloadTyp: 96, PacketizationMode: 1}
						videoFormat = h264Format
						enc, err := h264Format.CreateEncoder()
						if err != nil {
							log.Printf("stream %s create h264 encoder: %v", meta.name, err)
							videoFormat = nil
							continue
						}
						videoEncoder = enc
					}
					videoMedia.Formats = []format.Format{videoFormat}
				}

				var readyToExpose bool
				var clockRate int

				switch packet.Codec {
				case "H265":
					h265Format := videoFormat.(*format.H265)
					clockRate = h265Format.ClockRate()
					vps, sps, pps := media.ExtractH265Params(nalus)
					if vps != nil || sps != nil || pps != nil {
						h265Format.VPS = coalesce(vps, h265Format.VPS)
						h265Format.SPS = coalesce(sps, h265Format.SPS)
						h265Format.PPS = coalesce(pps, h265Format.PPS)
					}
					readyToExpose = h265Format.VPS != nil && h265Format.SPS != nil && h265Format.PPS != nil
				default:
					h264Format := videoFormat.(*format.H264)
					clockRate = h264Format.ClockRate()
					sps, pps := media.ExtractH264Params(nalus)
					if sps != nil || pps != nil {
						h264Format.SPS = coalesce(sps, h264Format.SPS)
						h264Format.PPS = coalesce(pps, h264Format.PPS)
					}
					readyToExpose = h264Format.SPS != nil && h264Format.PPS != nil
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
						continue
					}
				}

				streamPaused := updatePauseState(time.Now())

				var pkts []*rtp.Packet
				var err error
				switch packet.Codec {
				case "H265":
					pkts, err = videoEncoder.(*rtph265.Encoder).Encode(nalus)
					if err == nil {
						media.FixH265AggregationTemporalID(pkts)
					}
				default:
					pkts, err = videoEncoder.(*rtph264.Encoder).Encode(nalus)
				}

				if err != nil {
					log.Printf("stream %s encode rtp: %v", meta.name, err)
					continue
				}

				rawVideoRTP := rtpTimestampForClock(continuousUS, clockRate)
				switch {
				case streamPaused:
					// Frames (including IDRs) are dropped while paused; make
					// sure resumed clients start on a keyframe, not P-frame
					// garbage.
					resumeNeedsKeyframe = true
				case resumeNeedsKeyframe && packet.Kind != baichuan.MediaPacketIFrame:
					// still waiting for the first IDR after resume
				default:
					resumeNeedsKeyframe = false
					ts := videoRTP.next(rawVideoRTP)
					for _, pkt := range pkts {
						pkt.Timestamp = ts
					}
					emitVideo(pkts, continuousUS)
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
				timestamp := audioTimestampForPacket(packet, &streamTimestamps)
				if err := audio.processAAC(packet.Data, timestamp, handler, meta, !updatePauseState(time.Now())); err != nil {
					log.Printf("stream %s audio publish error: %v", meta.name, err)
				}

			case baichuan.MediaPacketADPCM:
				audioPackets++
				timestamp := audioTimestampForPacket(packet, &streamTimestamps)
				if err := audio.processADPCM(packet.Data, timestamp, handler, meta, !updatePauseState(time.Now())); err != nil {
					log.Printf("stream %s audio adpcm publish error: %v", meta.name, err)
				}
			}

		case <-statsTicker.C:
			now := time.Now()
			updatePauseState(now)
			lastPacketAge := time.Duration(0)
			if !lastPacketAt.IsZero() {
				lastPacketAge = now.Sub(lastPacketAt)
			}
			lastVideoAge := time.Duration(0)
			if !lastVideoAt.IsZero() {
				lastVideoAge = now.Sub(lastVideoAt)
			}
			log.Debugf("stream %s stats info=%d video=%d audio=%d video_bytes=%d rtsp_ready=%t audio_ready=%t has_clients=%t last_packet_age=%v last_video_age=%v", meta.name, infoPackets, videoPackets, audioPackets, videoBytes, handler.ready(), audio.ready(), handler.hasClients(), lastPacketAge, lastVideoAge)

		case <-controlTicker.C:
			updatePauseState(time.Now())
		}
	}
}

type timestampUnwrapper struct {
	highest uint64
	offset  uint64
	baseSet bool
	// nowUnixMicro is optional; when nil, time.Now().UnixMicro is used (first sample anchors to wall clock).
	nowUnixMicro func() int64
}

func (u *timestampUnwrapper) unwrap(ts32 uint32) uint64 {
	if !u.baseSet {
		nowFn := func() int64 { return time.Now().UnixMicro() }
		if u.nowUnixMicro != nil {
			nowFn = u.nowUnixMicro
		}
		micros := nowFn()
		if micros < 0 {
			micros = 0
		}
		systemMicro := uint64(micros)
		u.offset = systemMicro - uint64(ts32)
		u.highest = uint64(ts32)
		u.baseSet = true
		return systemMicro
	}

	continuous := unwrapTimestamp(ts32, u.highest)
	if continuous > u.highest {
		u.highest = continuous
	}
	return continuous + u.offset
}

type rtpTimestampGuard struct {
	offset uint32
	last   uint32
	set    bool
}

func (g *rtpTimestampGuard) next(ts uint32) uint32 {
	if !g.set {
		g.last = ts
		g.set = true
		return ts
	}
	adjusted := ts + g.offset
	if ts == g.last {
		g.offset = g.last + 1 - ts
		adjusted = g.last + 1
	} else if !rtpTimestampAfter(adjusted, g.last) {
		jumpBackward := uint32(int32(g.last - adjusted))
		if jumpBackward > 90000 {
			g.offset = g.last + 1 - ts
			adjusted = ts + g.offset
		} else {
			adjusted = g.last + 1
		}
	}
	g.last = adjusted
	return adjusted
}

func (g *rtpTimestampGuard) applyBaseToPackets(pkts []*rtp.Packet, base uint32, duration uint32) uint32 {
	if len(pkts) == 0 {
		return base
	}

	sum := base + pkts[0].Timestamp //#nosec G115
	first := sum + g.offset
	if g.set && sum == g.last {
		g.offset = 0
		first = sum
	}
	if g.set && rtpTimestampBefore(first, g.last) {
		jumpBackward := uint32(int32(g.last - first))
		if jumpBackward > 90000 {
			g.offset = g.last - sum
			first = sum + g.offset
		} else {
			first = g.last
		}
	}

	adjusted := first
	if duration == 0 {
		g.last = adjusted
	} else {
		g.last = adjusted + duration
	}
	g.set = true
	return adjusted - pkts[0].Timestamp
}

func rtpTimestampAfter(ts uint32, prev uint32) bool {
	return int32(ts-prev) > 0 //#nosec G115
}

func rtpTimestampBefore(ts uint32, prev uint32) bool {
	return int32(ts-prev) < 0 //#nosec G115
}

func audioTimestampForPacket(packet baichuan.MediaPacket, audioTimestamps *timestampUnwrapper) mediaTimestamp {
	if packet.HasTimestamp {
		return mediaTimestamp{
			Microseconds:  audioTimestamps.unwrap(packet.TimestampMicrosecs),
			Valid:         true,
			Authoritative: true,
		}
	}
	return mediaTimestamp{}
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
