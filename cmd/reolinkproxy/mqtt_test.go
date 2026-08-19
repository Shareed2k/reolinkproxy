package main

import (
	"strings"
	"testing"
)

func TestParsePTZPayload(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		payload   string
		wantDir   string
		wantSpeed int
	}{
		{name: "direction only", payload: "left", wantDir: "left", wantSpeed: 32},
		{name: "uppercase normalized", payload: "UP", wantDir: "up", wantSpeed: 32},
		{name: "direction with speed", payload: "right 10", wantDir: "right", wantSpeed: 10},
		{name: "invalid speed falls back", payload: "down abc", wantDir: "down abc", wantSpeed: 32},
		{name: "zero speed falls back", payload: "left 0", wantDir: "left 0", wantSpeed: 32},
		{name: "stop", payload: "stop", wantDir: "stop", wantSpeed: 32},
		{name: "surrounding whitespace", payload: "  left 5  ", wantDir: "left", wantSpeed: 5},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			dir, speed := parsePTZPayload(tt.payload)
			if dir != tt.wantDir || speed != tt.wantSpeed {
				t.Fatalf("parsePTZPayload(%q) = (%q, %d), want (%q, %d)", tt.payload, dir, speed, tt.wantDir, tt.wantSpeed)
			}
		})
	}
}

func TestHAEntityDiscoveryPayloads(t *testing.T) {
	t.Parallel()

	s := &mqttService{cfg: MQTTConfig{Topic: "reolinkproxy"}, camName: "front"}

	tests := []struct {
		name       string
		entity     haEntityConfig
		wantTopics []string
	}{
		{
			name: "siren switch drives the control topic",
			entity: haEntityConfig{
				CommandTopic: s.controlTopic("siren"),
			},
			wantTopics: []string{"reolinkproxy/front/control/siren"},
		},
		{
			name: "battery sensor reads the status topic",
			entity: haEntityConfig{
				StateTopic: s.statusTopic("battery_level"),
			},
			wantTopics: []string{"reolinkproxy/front/status/battery_level"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := tt.entity.CommandTopic + tt.entity.StateTopic
			for _, want := range tt.wantTopics {
				if !strings.Contains(got, want) {
					t.Fatalf("topic %q missing %q", got, want)
				}
			}
		})
	}

	device := s.haDevice()
	if len(device.Identifiers) != 1 || device.Identifiers[0] != "front" {
		t.Fatalf("device identifiers = %v, want [front] (single HA device per camera)", device.Identifiers)
	}
}
