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

func registerCameraMQTT(ctx context.Context, client mqtt.Client, cfg MQTTConfig, device *CameraDevice, camName string, channel uint8, motion *cameraMotionState) {
	camName = strings.ReplaceAll(strings.TrimSpace(camName), " ", "_")

	s := &mqttService{
		cfg:     cfg,
		client:  client,
		device:  device,
		camName: camName,
		channel: channel,
	}

	// Publish Home Assistant Auto-Discovery for motion sensor
	type haDevice struct {
		Identifiers  []string `json:"identifiers"`
		Name         string   `json:"name"`
		Manufacturer string   `json:"manufacturer"`
		Model        string   `json:"model"`
	}
	type haConfig struct {
		Name        string   `json:"name"`
		DeviceClass string   `json:"device_class"`
		StateTopic  string   `json:"state_topic"`
		PayloadOn   string   `json:"payload_on"`
		PayloadOff  string   `json:"payload_off"`
		UniqueID    string   `json:"unique_id"`
		Device      haDevice `json:"device"`
	}

	motionStateTopic := fmt.Sprintf("%s/%s/status/motion", cfg.Topic, camName)
	discoveryTopic := fmt.Sprintf("homeassistant/binary_sensor/%s_motion/config", camName)

	discoveryMsg := haConfig{
		Name:        fmt.Sprintf("%s Motion", camName),
		DeviceClass: "motion",
		StateTopic:  motionStateTopic,
		PayloadOn:   "on",
		PayloadOff:  "off",
		UniqueID:    fmt.Sprintf("%s_motion", camName),
		Device: haDevice{
			Identifiers:  []string{camName},
			Name:         camName,
			Manufacturer: "Reolink",
			Model:        "reolinkproxy",
		},
	}
	if b, err := json.Marshal(discoveryMsg); err == nil {
		client.Publish(discoveryTopic, 1, true, string(b))
	}

	// Initialize the motion state
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
