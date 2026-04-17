package main

import (
	"os"
	"time"

	"gopkg.in/yaml.v3"
)

type Config struct {
	MQTT    MQTTConfig     `yaml:"mqtt"`
	Server  ServerConfig   `yaml:"server"`
	ONVIF   ONVIFConfig    `yaml:"onvif"`
	Cameras []CameraConfig `yaml:"cameras"`
}

type MQTTConfig struct {
	Broker   string `yaml:"broker"`
	Username string `yaml:"username"`
	Password string `yaml:"password"`
	Topic    string `yaml:"topic"`
}

type ServerConfig struct {
	RTSPAddress   string `yaml:"rtsp_address"`
	RTPAddress    string `yaml:"rtp_address"`
	RTCPAddress   string `yaml:"rtcp_address"`
	ONVIFAddress  string `yaml:"onvif_address"`
	AdvertiseHost string `yaml:"advertise_host"`
	LogPackets    bool   `yaml:"log_packets"`
}

type ONVIFConfig struct {
	Username string `yaml:"username"`
	Password string `yaml:"password"`
}

type CameraConfig struct {
	Name     string        `yaml:"name"`
	Host     string        `yaml:"host"`
	Port     int           `yaml:"port"`
	UID      string        `yaml:"uid"`
	Username string        `yaml:"username"`
	Password string        `yaml:"password"`
	Timeout  time.Duration `yaml:"timeout"`
	Stream   string        `yaml:"stream"`
	Channel  int           `yaml:"channel"`
	RTSPPath string        `yaml:"rtsp_path"`
}

func loadConfig(path string) (*Config, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	cfg := &Config{
		Server: ServerConfig{
			RTSPAddress:  ":8554",
			RTPAddress:   ":8000",
			RTCPAddress:  ":8001",
			ONVIFAddress: ":8002",
		},
		MQTT: MQTTConfig{
			Topic: "reolinkproxy",
		},
	}

	if err := yaml.Unmarshal(b, cfg); err != nil {
		return nil, err
	}

	// Apply defaults for cameras
	for i := range cfg.Cameras {
		if cfg.Cameras[i].Port == 0 {
			cfg.Cameras[i].Port = 9000
		}
		if cfg.Cameras[i].Stream == "" {
			cfg.Cameras[i].Stream = "main"
		}
		if cfg.Cameras[i].RTSPPath == "" {
			cfg.Cameras[i].RTSPPath = cfg.Cameras[i].Name + "/stream"
		}
		if cfg.Cameras[i].Timeout == 0 {
			cfg.Cameras[i].Timeout = 10 * time.Second
		}
	}

	return cfg, nil
}
