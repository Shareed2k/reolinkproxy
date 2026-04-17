// Package main provides the entry point for the reolinkproxy application.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	gortsplib "github.com/bluenviron/gortsplib/v4"
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
	var configPath string
	flag.StringVar(&configPath, "config", "config.yml", "path to configuration file")
	showVersion := flag.Bool("version", false, "print version and exit")
	flag.Parse()

	if *showVersion {
		log.Printf("reolinkproxy version=%s commit=%s", Version, Commit)
		os.Exit(0)
	}

	cfg, err := loadConfig(configPath)
	if err != nil {
		log.Fatalf("failed to load config: %v", err)
	}

	if len(cfg.Cameras) == 0 {
		log.Fatalf("no cameras defined in config")
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	serverHandler := newRTSPServerHandler()

	server := &gortsplib.Server{
		Handler:        serverHandler,
		RTSPAddress:    cfg.Server.RTSPAddress,
		UDPRTPAddress:  cfg.Server.RTPAddress,
		UDPRTCPAddress: cfg.Server.RTCPAddress,
		WriteQueueSize: 2048,
	}

	if err := server.Start(); err != nil {
		log.Fatalf("start rtsp server: %v", err)
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

		client, err := baichuan.Dial(ctx, bcCfg)
		if err != nil {
			log.Printf("camera %s dial error: %v", camCfg.Name, err)
			continue
		}
		// In a real app we might want to keep references to close them cleanly,
		// but since we only close on exit, OS will handle socket cleanup.

		if err := client.Login(ctx); err != nil {
			log.Printf("camera %s login error: %v", camCfg.Name, err)
			continue
		}

		streamsList := strings.Split(camCfg.Stream, ",")
		for _, s := range streamsList {
			s = strings.TrimSpace(s)
			if s == "" {
				continue
			}

			reader, err := client.StartPreview(ctx, uint8(camCfg.Channel), parseStream(s))
			if err != nil {
				log.Printf("start preview for camera %s stream %s: %v", camCfg.Name, s, err)
				continue
			}

			path := camCfg.RTSPPath
			if len(streamsList) > 1 {
				path = camCfg.RTSPPath + "_" + s
			}
			path = strings.TrimPrefix(path, "/")

			meta := &streamMetadata{cameraName: camCfg.Name, name: s, path: path}
			metas = append(metas, meta)

			streamHandler := newRTSPStreamHandler(path)
			streamHandler.attachServer(server)
			serverHandler.addStream(path, streamHandler)

			log.Printf("preview started camera=%s stream=%s path=%s", camCfg.Name, s, path)

			go runStream(ctx, reader, client, streamHandler, meta, cfg.Server.LogPackets)
		}

		if mqttClient != nil {
			registerCameraMQTT(ctx, mqttClient, cfg.MQTT, client, camCfg.Name, uint8(camCfg.Channel))
		}
	}

	onvifCfg := onvifConfig{
		Address:         cfg.Server.ONVIFAddress,
		DevicePath:      "/onvif/device_service",
		MediaPath:       "/onvif/media_service",
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

	log.Printf("rtsp server listening at %s", cfg.Server.RTSPAddress)
	log.Printf("onvif device service listening at %s%s", cfg.Server.ONVIFAddress, onvifCfg.DevicePath)

	<-ctx.Done()
}

//nolint:gocyclo
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
				log.Printf("stream %s preview closed: %v", meta.name, client.Err())
				return
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
					log.Printf("stream %s stalled for %v, restarting proxy to recover", meta.name, stalledDuration)
					//nolint:gocritic // By design we want to crash hard to let the docker container restart.
					os.Exit(1)
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
