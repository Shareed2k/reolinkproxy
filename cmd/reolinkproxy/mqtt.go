package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"
	"github.com/shareed2k/reolinkproxy/pkg/baichuan"
)

type mqttConfig struct {
	Broker   string
	Username string
	Password string
	Topic    string
}

type mqttService struct {
	cfg     mqttConfig
	client  mqtt.Client
	bc      *baichuan.Client
	camName string
	channel uint8
}

func startMQTT(ctx context.Context, cfg mqttConfig, bc *baichuan.Client, camName string, channel uint8) error {
	if cfg.Broker == "" {
		return nil // MQTT disabled
	}

	camName = strings.ReplaceAll(strings.TrimSpace(camName), " ", "_")

	s := &mqttService{
		cfg:     cfg,
		bc:      bc,
		camName: camName,
		channel: channel,
	}

	opts := mqtt.NewClientOptions().AddBroker(cfg.Broker)
	opts.SetClientID(fmt.Sprintf("reolinkproxy-%s", camName))
	if cfg.Username != "" {
		opts.SetUsername(cfg.Username)
	}
	if cfg.Password != "" {
		opts.SetPassword(cfg.Password)
	}

	opts.SetAutoReconnect(true)
	opts.SetMaxReconnectInterval(10 * time.Second)

	// Last Will configuration
	lwtTopic := fmt.Sprintf("%s/status", cfg.Topic)
	opts.SetWill(lwtTopic, "offline", 1, true)

	opts.OnConnect = func(c mqtt.Client) {
		log.Printf("mqtt: connected to broker at %s", cfg.Broker)
		c.Publish(lwtTopic, 1, true, "ready")

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
			c.Publish(discoveryTopic, 1, true, string(b))
		}

		// Initialize the motion state
		c.Publish(motionStateTopic, 1, true, "off")

		// Subscribe to control topics
		controlTopic := fmt.Sprintf("%s/%s/control/#", cfg.Topic, camName)
		c.Subscribe(controlTopic, 1, s.handleControl)

		queryTopic := fmt.Sprintf("%s/%s/query/#", cfg.Topic, camName)
		c.Subscribe(queryTopic, 1, s.handleQuery)
	}

	opts.OnConnectionLost = func(c mqtt.Client, err error) {
		log.Printf("mqtt: connection lost: %v", err)
	}

	client := mqtt.NewClient(opts)
	token := client.Connect()
	if token.Wait() && token.Error() != nil {
		return fmt.Errorf("mqtt connect: %w", token.Error())
	}

	s.client = client

	// Fire off the Baichuan Motion Listener
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			default:
			}

			log.Printf("mqtt: establishing camera motion listener...")
			cancelMotion, err := bc.ListenForMotion(ctx, channel, func(motionDetected bool) {
				topic := fmt.Sprintf("%s/%s/status/motion", cfg.Topic, camName)
				val := "off"
				if motionDetected {
					val = "on"
				}
				s.client.Publish(topic, 1, true, val)
			})

			if err != nil {
				log.Printf("mqtt: motion listener error: %v. retrying in 10s...", err)
				select {
				case <-ctx.Done():
					return
				case <-time.After(10 * time.Second):
				}
				continue
			}

			// Block until context is done or client is closed
			select {
			case <-ctx.Done():
				cancelMotion()
				return
			case <-time.After(5 * time.Minute): // Renew connection periodically
				cancelMotion()
			}
		}
	}()

	// Handle graceful shutdown
	go func() {
		<-ctx.Done()
		client.Publish(lwtTopic, 1, true, "offline").Wait()
		client.Disconnect(250)
	}()

	return nil
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

	// We wrap commands in a helper so we can send success/error to /config/status
	err := func() error {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		switch cmd {
		case "reboot":
			return s.bc.Reboot(ctx, s.channel)
		default:
			return fmt.Errorf("control command '%s' not yet implemented in reolinkproxy", cmd)
		}
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
		// ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		// defer cancel()

		switch cmd {
		default:
			return fmt.Errorf("query command '%s' not yet implemented in reolinkproxy", cmd)
		}
	}()

	statusTopic := fmt.Sprintf("%s/config/status", s.cfg.Topic)
	if err != nil {
		log.Printf("mqtt: query err: %v", err)
		client.Publish(statusTopic, 0, false, fmt.Sprintf("Error: %v", err))
	} else {
		client.Publish(statusTopic, 0, false, "Ok(())")
	}
}
