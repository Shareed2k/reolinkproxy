package main

import (
	"fmt"
	"os"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
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

var (
	cameraEnvKeyRE   = regexp.MustCompile(`^REOLINK_CAMERA_(\d+)_([A-Z0-9_]+)$`)
	cameraConfigType = reflect.TypeOf(CameraConfig{})
	durationType     = reflect.TypeOf(time.Duration(0))
)

func defaultConfig() *Config {
	return &Config{
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
}

func loadCamerasFromEnv() ([]CameraConfig, error) {
	return loadCamerasFromEntries(os.Environ())
}

func loadCamerasFromEntries(entries []string) ([]CameraConfig, error) {
	fieldIndexes := cameraEnvFieldIndexes()
	camerasByIndex := make(map[int]*CameraConfig)

	for _, entry := range entries {
		key, value, ok := strings.Cut(entry, "=")
		if !ok {
			continue
		}

		matches := cameraEnvKeyRE.FindStringSubmatch(key)
		if len(matches) != 3 {
			continue
		}

		cameraIndex, err := strconv.Atoi(matches[1])
		if err != nil {
			return nil, fmt.Errorf("%s: invalid camera index: %w", key, err)
		}

		fieldIndex, found := fieldIndexes[matches[2]]
		if !found {
			continue
		}

		camera := camerasByIndex[cameraIndex]
		if camera == nil {
			camera = &CameraConfig{}
			camerasByIndex[cameraIndex] = camera
		}

		field := reflect.ValueOf(camera).Elem().Field(fieldIndex)
		if err := setFieldFromEnv(field, value, key); err != nil {
			return nil, err
		}
	}

	if len(camerasByIndex) == 0 {
		return nil, nil
	}

	indexes := make([]int, 0, len(camerasByIndex))
	for cameraIndex := range camerasByIndex {
		indexes = append(indexes, cameraIndex)
	}
	sort.Ints(indexes)

	cameras := make([]CameraConfig, 0, len(indexes))
	for _, cameraIndex := range indexes {
		camera := *camerasByIndex[cameraIndex]
		applyCameraDefaults(&camera)

		if err := validateCameraConfig(&camera); err != nil {
			return nil, fmt.Errorf("REOLINK_CAMERA_%d_*: %w", cameraIndex, err)
		}

		cameras = append(cameras, camera)
	}

	return cameras, nil
}

func cameraEnvFieldIndexes() map[string]int {
	out := make(map[string]int, cameraConfigType.NumField())

	for i := range cameraConfigType.NumField() {
		tag := strings.Split(cameraConfigType.Field(i).Tag.Get("yaml"), ",")[0]
		if tag == "" || tag == "-" {
			continue
		}
		out[strings.ToUpper(tag)] = i
	}

	return out
}

func setFieldFromEnv(field reflect.Value, rawValue string, envKey string) error {
	if field.Type() == durationType {
		duration, err := time.ParseDuration(rawValue)
		if err != nil {
			return fmt.Errorf("%s: invalid duration %q", envKey, rawValue)
		}
		field.SetInt(int64(duration))
		return nil
	}

	switch field.Kind() {
	case reflect.String:
		field.SetString(rawValue)
	case reflect.Int:
		value, err := strconv.Atoi(rawValue)
		if err != nil {
			return fmt.Errorf("%s: invalid int %q", envKey, rawValue)
		}
		field.SetInt(int64(value))
	default:
		return fmt.Errorf("%s: unsupported field type %s", envKey, field.Type())
	}

	return nil
}

func applyCameraDefaults(camera *CameraConfig) {
	if camera.Port == 0 {
		camera.Port = 9000
	}
	if camera.Stream == "" {
		camera.Stream = "main"
	}
	if camera.RTSPPath == "" {
		camera.RTSPPath = camera.Name + "/stream"
	}
	if camera.Timeout == 0 {
		camera.Timeout = 10 * time.Second
	}
}

func validateCameraConfig(camera *CameraConfig) error {
	if camera.Name == "" {
		return fmt.Errorf("camera name is required")
	}
	if camera.Host == "" && camera.UID == "" {
		return fmt.Errorf("camera host or uid is required")
	}
	return nil
}
