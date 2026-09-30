package main

import (
	"fmt"
	"math"
	"net"
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
	PprofAddress  string `yaml:"pprof_address"`
	AdvertiseHost string `yaml:"advertise_host"`
	LogLevel      string `yaml:"log_level"`
	LogPackets    bool   `yaml:"log_packets"`

	// AudioPacerInitialLatencyMs is the media pacer startup delay for audio (wall clock before
	// the first packet is sent). Default 500ms.
	AudioPacerInitialLatencyMs int `yaml:"audio_pacer_initial_latency_ms"`
	// AudioPacerMaxLeadMs caps how far ahead of wall clock the audio pacer cursor may run;
	// if exceeded, the cursor is reset to now. Default 2s.
	AudioPacerMaxLeadMs int `yaml:"audio_pacer_max_lead_ms"`
	// AudioPacerSnapOnPast, when true, snaps the emission cursor to now if it falls behind
	// wall clock.
	AudioPacerSnapOnPast bool `yaml:"audio_pacer_snap_on_past"`

	// VideoPacerInitialLatencyMs is the media pacer startup delay for video. Default 1500ms.
	VideoPacerInitialLatencyMs int `yaml:"video_pacer_initial_latency_ms"`
	// VideoPacerMaxLeadMs caps how far ahead of wall clock the video pacer cursor may run. Default 3s.
	VideoPacerMaxLeadMs int `yaml:"video_pacer_max_lead_ms"`
	// VideoPacerSnapOnPast, when true, snaps the video pacer cursor to now when behind; default false for video.
	VideoPacerSnapOnPast bool `yaml:"video_pacer_snap_on_past"`

	// DisableRTCPSenderReports suppresses periodic RTCP Sender Reports on published streams (default true).
	// Some receivers (e.g. FFmpeg) re-anchor decode time on each SR, which can cause non-monotonic DTS warnings.
	DisableRTCPSenderReports bool `yaml:"disable_rtcp_sender_reports"`
}

type ONVIFConfig struct {
	Username  string `yaml:"username"`
	Password  string `yaml:"password"`
	HWAddress string `yaml:"hw_address"`
}

type CameraConfig struct {
	Name           string        `yaml:"name"`
	Host           string        `yaml:"host"`
	Port           int           `yaml:"port"`
	UID            string        `yaml:"uid"`
	Username       string        `yaml:"username"`
	Password       string        `yaml:"password"`
	Timeout        time.Duration `yaml:"timeout"`
	Stream         string        `yaml:"stream"`
	Channel        int           `yaml:"channel"`
	RTSPPath       string        `yaml:"rtsp_path"`
	TalkProfile    string        `yaml:"talk_profile"`
	TalkVolume     int           `yaml:"talk_volume"`
	TalkEncoder    string        `yaml:"talk_encoder"`
	TalkEncoderCmd string        `yaml:"talk_encoder_cmd"`
	PauseOnMotion  bool          `yaml:"pause_on_motion"`
	PauseOnClient  bool          `yaml:"pause_on_client"`
	PauseTimeout   time.Duration `yaml:"pause_timeout"`
	IdleDisconnect bool          `yaml:"idle_disconnect"`
	IdleTimeout    time.Duration `yaml:"idle_timeout"`
	BatteryCamera  bool          `yaml:"battery_camera"`
	// PTZRelativeMsPerUnit calibrates the emulated ONVIF RelativeMove: burst
	// duration in ms for a full-range (1.0) translation. Hardware varies —
	// tune per camera. Default 1000.
	PTZRelativeMsPerUnit int `yaml:"ptz_relative_ms_per_unit"`
	// PacerLatencyMs overrides the server pacer latency for this camera:
	// "200" for all its streams, or per stream "sub:200" / "main:1500,sub:200".
	PacerLatencyMs string `yaml:"pacer_latency_ms"`
}

var (
	cameraEnvKeyRE   = regexp.MustCompile(`^REOLINK_CAMERA_(\d+)_([A-Z0-9_]+)$`)
	cameraConfigType = reflect.TypeOf(CameraConfig{})
	durationType     = reflect.TypeOf(time.Duration(0))
)

func (c ServerConfig) audioPacerInitialLatency() time.Duration {
	return time.Duration(c.AudioPacerInitialLatencyMs) * time.Millisecond
}

func (c ServerConfig) audioPacerMaxLead() time.Duration {
	return time.Duration(c.AudioPacerMaxLeadMs) * time.Millisecond
}

func (c ServerConfig) videoPacerInitialLatency() time.Duration {
	return time.Duration(c.VideoPacerInitialLatencyMs) * time.Millisecond
}

func (c ServerConfig) videoPacerMaxLead() time.Duration {
	return time.Duration(c.VideoPacerMaxLeadMs) * time.Millisecond
}

// withPacerLatency returns a copy of c whose audio and video pacers both start
// ms behind (kept equal so audio does not lead video) with a max lead of
// max(500ms, 2*ms). The lead must exceed the initial latency, or the pacer
// re-anchors to now on the second frame and the buffer collapses; 200 gives
// the 200/500 low-latency preset from the README.
func (c ServerConfig) withPacerLatency(ms int) ServerConfig {
	lead := max(500, 2*ms)
	c.VideoPacerInitialLatencyMs = ms
	c.AudioPacerInitialLatencyMs = ms
	c.VideoPacerMaxLeadMs = lead
	c.AudioPacerMaxLeadMs = lead
	return c
}

func defaultConfig() *Config {
	return &Config{
		Server: ServerConfig{
			RTSPAddress:  ":8554",
			RTPAddress:   ":8000",
			RTCPAddress:  ":8001",
			ONVIFAddress: ":8002",
			PprofAddress: "",
			LogLevel:     "info",
			// Matches the video pacer's initial latency: with asymmetric
			// startup delays audio runs ahead of video on the wire by the
			// difference, forcing sync-aware players to buffer that much and
			// visibly desyncing players with small caches.
			AudioPacerInitialLatencyMs: 1500,
			AudioPacerMaxLeadMs:        2000,
			AudioPacerSnapOnPast:       true,
			VideoPacerInitialLatencyMs: 1500,
			VideoPacerMaxLeadMs:        3000,
			VideoPacerSnapOnPast:       false,
			// Sender Reports carry an honest RTP<->NTP mapping (frames are
			// stamped with their camera-anchored wall time), enabling client
			// A/V sync. Set true for legacy clients confused by SR.
			DisableRTCPSenderReports: false,
		},
		ONVIF: ONVIFConfig{
			HWAddress: "00:00:00:00:00:00",
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
	case reflect.Bool:
		value, err := strconv.ParseBool(rawValue)
		if err != nil {
			return fmt.Errorf("%s: invalid bool %q", envKey, rawValue)
		}
		field.SetBool(value)
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
	camera.TalkProfile = normalizeCameraProfileName(camera.TalkProfile)
	if camera.TalkVolume == 0 {
		camera.TalkVolume = 100
	}
	if camera.TalkEncoder == "" {
		camera.TalkEncoder = "internal"
	}
	if camera.PauseTimeout == 0 {
		camera.PauseTimeout = time.Second
	}
	if camera.IdleTimeout == 0 {
		camera.IdleTimeout = 30 * time.Second
	}
}

func validateCameraConfig(camera *CameraConfig) error {
	if camera.Name == "" {
		return fmt.Errorf("camera name is required")
	}
	if camera.Host == "" && camera.UID == "" {
		return fmt.Errorf("camera host or uid is required")
	}
	if camera.TalkProfile != "" && !camera.hasStream(camera.TalkProfile) {
		return fmt.Errorf("camera talk_profile %q must be one of configured streams %q", camera.TalkProfile, camera.Stream)
	}
	if camera.Channel < 0 || camera.Channel > math.MaxUint8 {
		return fmt.Errorf("camera channel %d out of range 0-%d", camera.Channel, math.MaxUint8)
	}
	latencies, err := parsePacerLatency(camera.PacerLatencyMs)
	if err != nil {
		return err
	}
	for stream := range latencies {
		if stream != "" && !camera.hasStream(stream) {
			return fmt.Errorf("camera pacer_latency_ms stream %q must be one of configured streams %q", stream, camera.Stream)
		}
	}
	return nil
}

// parsePacerLatency parses PACER_LATENCY_MS into per-stream milliseconds; the
// "" key holds a bare value that applies to every stream.
func parsePacerLatency(raw string) (map[string]int, error) {
	out := make(map[string]int)
	for _, part := range strings.Split(raw, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		stream, value, found := strings.Cut(part, ":")
		if !found {
			stream, value = "", part
		}
		ms, err := strconv.Atoi(strings.TrimSpace(value))
		if err != nil || ms < 0 {
			return nil, fmt.Errorf("camera pacer_latency_ms %q: want <ms> or <stream>:<ms>[,...]", raw)
		}
		out[normalizeCameraProfileName(stream)] = ms
	}
	return out, nil
}

// pacerLatencyFor returns the PACER_LATENCY_MS override for stream, preferring
// a per-stream entry over a bare camera-wide value.
func (c CameraConfig) pacerLatencyFor(stream string) (int, bool) {
	latencies, _ := parsePacerLatency(c.PacerLatencyMs) // validated at load
	if ms, ok := latencies[normalizeCameraProfileName(stream)]; ok {
		return ms, true
	}
	ms, ok := latencies[""]
	return ms, ok
}

// normalizeHWAddress validates the ONVIF-reported hardware address as a
// 48-bit MAC and returns it in canonical lower-case colon form, so a typo
// fails at startup instead of reaching NVRs that key devices by MAC (#31).
func normalizeHWAddress(raw string) (string, error) {
	hw, err := net.ParseMAC(raw)
	if err != nil || len(hw) != 6 {
		return "", fmt.Errorf("onvif hw address %q must be a 48-bit MAC like 02:42:ac:11:00:02", raw)
	}
	return hw.String(), nil
}

// channelID returns the configured channel as the Baichuan protocol's uint8.
// Range is enforced by validateCameraConfig at load time.
func (c CameraConfig) channelID() uint8 {
	return uint8(min(max(c.Channel, 0), math.MaxUint8))
}

func splitCameraStreams(raw string) []string {
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		name := normalizeCameraProfileName(part)
		if name == "" {
			continue
		}
		out = append(out, name)
	}
	return out
}

func normalizeCameraProfileName(raw string) string {
	return strings.ToLower(strings.TrimSpace(raw))
}

func (c CameraConfig) hasStream(name string) bool {
	name = normalizeCameraProfileName(name)
	for _, stream := range splitCameraStreams(c.Stream) {
		if stream == name {
			return true
		}
	}
	return false
}

func (c CameraConfig) preferredTalkProfile() string {
	if c.hasStream(c.TalkProfile) {
		return normalizeCameraProfileName(c.TalkProfile)
	}
	return ""
}
