package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"
	"github.com/shareed2k/reolinkproxy/pkg/baichuan"
)

type mqttService struct {
	cfg     MQTTConfig
	client  mqtt.Client
	device  *CameraDevice
	camName string
	channel uint8
}

func connectMQTT(cfg MQTTConfig) (mqtt.Client, error) {
	if cfg.Broker == "" {
		return nil, nil // MQTT disabled
	}

	opts := mqtt.NewClientOptions().AddBroker(cfg.Broker)
	opts.SetClientID("reolinkproxy-main")
	if cfg.Username != "" {
		opts.SetUsername(cfg.Username)
	}
	if cfg.Password != "" {
		opts.SetPassword(cfg.Password)
	}

	opts.SetAutoReconnect(true)
	opts.SetMaxReconnectInterval(10 * time.Second)

	lwtTopic := fmt.Sprintf("%s/status", cfg.Topic)
	opts.SetWill(lwtTopic, "offline", 1, true)

	opts.OnConnect = func(c mqtt.Client) {
		log.Printf("mqtt: connected to broker at %s", cfg.Broker)
		c.Publish(lwtTopic, 1, true, "ready")
	}

	opts.OnConnectionLost = func(_ mqtt.Client, err error) {
		log.Printf("mqtt: connection lost: %v", err)
	}

	client := mqtt.NewClient(opts)
	token := client.Connect()
	if token.Wait() && token.Error() != nil {
		return nil, fmt.Errorf("mqtt connect: %w", token.Error())
	}

	return client, nil
}

func registerCameraMQTT(ctx context.Context, client mqtt.Client, cfg MQTTConfig, device *CameraDevice, camName string, channel uint8, motion *cameraMotionState, batteryCamera bool) {
	camName = strings.ReplaceAll(strings.TrimSpace(camName), " ", "_")

	s := &mqttService{
		cfg:     cfg,
		client:  client,
		device:  device,
		camName: camName,
		channel: channel,
	}

	// Home Assistant auto-discovery for all supported entities
	s.publishEntityDiscovery()
	go s.pollBattery(ctx, batteryCamera)

	// Initialize the motion state
	motionStateTopic := fmt.Sprintf("%s/%s/status/motion", cfg.Topic, camName)
	client.Publish(motionStateTopic, 1, true, "off")

	// Subscribe to control topics
	controlTopic := fmt.Sprintf("%s/%s/control/#", cfg.Topic, camName)
	client.Subscribe(controlTopic, 1, s.handleControl)

	queryTopic := fmt.Sprintf("%s/%s/query/#", cfg.Topic, camName)
	client.Subscribe(queryTopic, 1, s.handleQuery)

	if motion == nil {
		return
	}

	go func() {
		updates, unsubscribe := motion.subscribe()
		defer unsubscribe()

		lastAI := make(map[string]bool)
		for {
			select {
			case <-ctx.Done():
				return
			case snapshot, ok := <-updates:
				if !ok {
					return
				}
				if snapshot.Unsupported || !snapshot.Known {
					continue
				}

				topic := fmt.Sprintf("%s/%s/status/motion", cfg.Topic, camName)
				val := "off"
				if snapshot.Active {
					val = "on"
				}
				s.client.Publish(topic, 1, true, val)

				activeAI := make(map[string]bool, len(snapshot.AITypes))
				for _, aiType := range snapshot.AITypes {
					activeAI[aiType] = true
				}
				for aiType := range mqttAIClasses {
					next := activeAI[aiType]
					if prev, seen := lastAI[aiType]; seen && prev == next {
						continue
					}
					lastAI[aiType] = next
					aiVal := "off"
					if next {
						aiVal = "on"
					}
					s.client.Publish(s.statusTopic("ai_"+aiType), 1, true, aiVal)
				}
			}
		}
	}()
}

// parsePTZPayload splits an MQTT PTZ payload into direction and speed.
// Accepted forms: "left" (default speed 32) or "left 10". The camera expects
// lower-case direction names ("up", "down", "left", "right", "stop", ...).
func parsePTZPayload(payload string) (string, int) {
	payload = strings.ToLower(strings.TrimSpace(payload))
	if fields := strings.Fields(payload); len(fields) == 2 {
		if speed, err := strconv.Atoi(fields[1]); err == nil && speed > 0 {
			return fields[0], speed
		}
	}
	return payload, 32
}

func (s *mqttService) handleControl(client mqtt.Client, msg mqtt.Message) {
	topic := msg.Topic()
	payload := string(msg.Payload())
	log.Printf("mqtt: recv control %s -> %s", topic, payload)

	parts := strings.Split(topic, "/")
	if len(parts) < 4 {
		return
	}
	cmd := parts[len(parts)-1]
	subCmd := ""
	if len(parts) >= 5 {
		subCmd = parts[len(parts)-2]
	}

	// We wrap commands in a helper so we can send success/error to /config/status
	err := func() error {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		return s.device.WithClient(ctx, func(bc *baichuan.Client) error {
			if subCmd == "ptz" && cmd == "preset" {
				var presetID int
				if _, err := fmt.Sscanf(payload, "%d", &presetID); err != nil {
					return fmt.Errorf("invalid preset ID: %s", payload)
				}
				return bc.PTZPreset(ctx, s.channel, presetID)
			}

			switch cmd {
			case "reboot":
				return bc.Reboot(ctx, s.channel)
			case "ptz":
				direction, speed := parsePTZPayload(payload)
				return bc.PTZControl(ctx, s.channel, direction, speed)
			case "siren":
				switch payload {
				case "on":
					return bc.Siren(ctx, s.channel, 1)
				case "off":
					return bc.Siren(ctx, s.channel, 0)
				}
				return nil
			case "privacy":
				switch strings.ToLower(strings.TrimSpace(payload)) {
				case "on":
					return bc.SetPrivacyMode(ctx, s.channel, 1)
				case "off":
					return bc.SetPrivacyMode(ctx, s.channel, 0)
				}
				return fmt.Errorf("invalid privacy payload: %s", payload)
			case "autofocus":
				switch strings.ToLower(strings.TrimSpace(payload)) {
				case "on":
					return bc.SetAutoFocus(ctx, s.channel, 0)
				case "off":
					return bc.SetAutoFocus(ctx, s.channel, 1)
				}
				return fmt.Errorf("invalid autofocus payload: %s", payload)
			default:
				return fmt.Errorf("control command '%s' not yet implemented in reolinkproxy", cmd)
			}
		})
	}()

	statusTopic := fmt.Sprintf("%s/config/status", s.cfg.Topic)
	if err != nil {
		log.Printf("mqtt: control err: %v", err)
		client.Publish(statusTopic, 0, false, fmt.Sprintf("Error: %v", err))
	} else {
		client.Publish(statusTopic, 0, false, "Ok(())")
	}
}

func (s *mqttService) handleQuery(client mqtt.Client, msg mqtt.Message) {
	topic := msg.Topic()
	log.Printf("mqtt: recv query %s", topic)

	parts := strings.Split(topic, "/")
	if len(parts) < 4 {
		return
	}
	cmd := parts[len(parts)-1]

	err := func() error {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		return s.device.WithClient(ctx, func(bc *baichuan.Client) error {
			switch cmd {
			case "battery":
				info, err := bc.GetBattery(ctx, s.channel)
				if err != nil {
					return err
				}

				// Publish detailed battery info
				xmlBytes, _ := json.Marshal(info)
				client.Publish(fmt.Sprintf("%s/%s/status/battery", s.cfg.Topic, s.camName), 0, false, string(xmlBytes))
				client.Publish(fmt.Sprintf("%s/%s/status/battery_level", s.cfg.Topic, s.camName), 0, false, fmt.Sprintf("%d", info.BatteryPercent))
				return nil
			default:
				return fmt.Errorf("query command '%s' not yet implemented in reolinkproxy", cmd)
			}
		})
	}()

	statusTopic := fmt.Sprintf("%s/config/status", s.cfg.Topic)
	if err != nil {
		log.Printf("mqtt: query err: %v", err)
		client.Publish(statusTopic, 0, false, fmt.Sprintf("Error: %v", err))
	} else {
		client.Publish(statusTopic, 0, false, "Ok(())")
	}
}
