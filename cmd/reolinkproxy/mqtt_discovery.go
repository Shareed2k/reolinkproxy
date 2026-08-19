package main

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/shareed2k/reolinkproxy/pkg/baichuan"
)

// Home Assistant MQTT discovery for camera entities (issue #25). All entities
// share one HA device block so they group under a single device per camera.

const batteryPollInterval = 10 * time.Minute

type haDevice struct {
	Identifiers  []string `json:"identifiers"`
	Name         string   `json:"name"`
	Manufacturer string   `json:"manufacturer"`
	Model        string   `json:"model"`
}

type haEntityConfig struct {
	Name         string   `json:"name"`
	UniqueID     string   `json:"unique_id"`
	DeviceClass  string   `json:"device_class,omitempty"`
	StateTopic   string   `json:"state_topic,omitempty"`
	CommandTopic string   `json:"command_topic,omitempty"`
	PayloadOn    string   `json:"payload_on,omitempty"`
	PayloadOff   string   `json:"payload_off,omitempty"`
	PayloadPress string   `json:"payload_press,omitempty"`
	Unit         string   `json:"unit_of_measurement,omitempty"`
	Optimistic   bool     `json:"optimistic,omitempty"`
	Device       haDevice `json:"device"`
}

func (s *mqttService) haDevice() haDevice {
	return haDevice{
		Identifiers:  []string{s.camName},
		Name:         s.camName,
		Manufacturer: "Reolink",
		Model:        "reolinkproxy",
	}
}

func (s *mqttService) statusTopic(name string) string {
	return fmt.Sprintf("%s/%s/status/%s", s.cfg.Topic, s.camName, name)
}

func (s *mqttService) controlTopic(name string) string {
	return fmt.Sprintf("%s/%s/control/%s", s.cfg.Topic, s.camName, name)
}

func (s *mqttService) publishDiscovery(component string, objectID string, entity haEntityConfig) {
	topic := fmt.Sprintf("homeassistant/%s/%s/config", component, objectID)
	payload, err := json.Marshal(entity)
	if err != nil {
		log.Printf("mqtt: marshal discovery %s: %v", objectID, err)
		return
	}
	s.client.Publish(topic, 1, true, string(payload))
}

// publishEntityDiscovery announces the entities every camera supports through
// the proxy's existing control topics.
func (s *mqttService) publishEntityDiscovery() {
	device := s.haDevice()

	s.publishDiscovery("binary_sensor", s.camName+"_motion", haEntityConfig{
		Name:        fmt.Sprintf("%s Motion", s.camName),
		UniqueID:    s.camName + "_motion",
		DeviceClass: "motion",
		StateTopic:  s.statusTopic("motion"),
		PayloadOn:   "on",
		PayloadOff:  "off",
		Device:      device,
	})

	s.publishDiscovery("switch", s.camName+"_siren", haEntityConfig{
		Name:         fmt.Sprintf("%s Siren", s.camName),
		UniqueID:     s.camName + "_siren",
		CommandTopic: s.controlTopic("siren"),
		PayloadOn:    "on",
		PayloadOff:   "off",
		Optimistic:   true,
		Device:       device,
	})

	s.publishDiscovery("button", s.camName+"_reboot", haEntityConfig{
		Name:         fmt.Sprintf("%s Reboot", s.camName),
		UniqueID:     s.camName + "_reboot",
		DeviceClass:  "restart",
		CommandTopic: s.controlTopic("reboot"),
		PayloadPress: "reboot",
		Device:       device,
	})

	s.publishDiscovery("switch", s.camName+"_privacy", haEntityConfig{
		Name:         fmt.Sprintf("%s Privacy Mode", s.camName),
		UniqueID:     s.camName + "_privacy",
		CommandTopic: s.controlTopic("privacy"),
		PayloadOn:    "on",
		PayloadOff:   "off",
		Optimistic:   true,
		Device:       device,
	})

	s.publishDiscovery("switch", s.camName+"_autofocus", haEntityConfig{
		Name:         fmt.Sprintf("%s Auto Focus", s.camName),
		UniqueID:     s.camName + "_autofocus",
		CommandTopic: s.controlTopic("autofocus"),
		PayloadOn:    "on",
		PayloadOff:   "off",
		Optimistic:   true,
		Device:       device,
	})

	for aiType, label := range mqttAIClasses {
		s.publishDiscovery("binary_sensor", s.camName+"_ai_"+aiType, haEntityConfig{
			Name:       fmt.Sprintf("%s %s", s.camName, label),
			UniqueID:   s.camName + "_ai_" + aiType,
			StateTopic: s.statusTopic("ai_" + aiType),
			PayloadOn:  "on",
			PayloadOff: "off",
			Device:     device,
		})
	}
}

// mqttAIClasses maps Baichuan AI detection classes to HA entity labels.
var mqttAIClasses = map[string]string{
	"people":  "Person",
	"vehicle": "Vehicle",
	"dog_cat": "Pet",
	"visitor": "Visitor",
}

func (s *mqttService) publishBatteryDiscovery() {
	s.publishDiscovery("sensor", s.camName+"_battery", haEntityConfig{
		Name:        fmt.Sprintf("%s Battery", s.camName),
		UniqueID:    s.camName + "_battery",
		DeviceClass: "battery",
		StateTopic:  s.statusTopic("battery_level"),
		Unit:        "%",
		Device:      s.haDevice(),
	})
}

// pollBattery periodically publishes the battery level. The battery sensor is
// only announced after the first successful read, so cameras without a
// battery never grow a ghost entity; non-battery cameras stop polling after
// the first failure.
func (s *mqttService) pollBattery(ctx context.Context, batteryCamera bool) {
	ticker := time.NewTicker(batteryPollInterval)
	defer ticker.Stop()

	announced := false
	poll := func() bool {
		pollCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()

		var info *baichuan.BatteryInfo
		err := s.device.WithClient(pollCtx, func(bc *baichuan.Client) error {
			var err error
			info, err = bc.GetBattery(pollCtx, s.channel)
			return err
		})
		if err != nil || info == nil {
			log.Debugf("mqtt: battery poll %s failed: %v", s.camName, err)
			return false
		}

		if !announced {
			announced = true
			s.publishBatteryDiscovery()
		}
		s.client.Publish(s.statusTopic("battery_level"), 1, true, fmt.Sprintf("%d", info.BatteryPercent))
		return true
	}

	if !poll() && !batteryCamera {
		return // first read failed and the camera is not marked as battery-powered
	}

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			poll()
		}
	}
}
