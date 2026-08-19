package main

import (
	"context"
	"fmt"
	"io"
	"math"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/shareed2k/reolinkproxy/pkg/baichuan"
)

// ONVIF PTZ service (ver20/ptz/wsdl) backed by Baichuan PTZ commands (issue #23).
//
// Element order inside the emitted tt: types follows the onvif.xsd sequences —
// strict SOAP clients (zeep/python-onvif used by Frigate and Home Assistant)
// validate against the schema.

const (
	ptzVelocityGenericSpace    = "http://www.onvif.org/ver10/tptz/PanTiltSpaces/VelocityGenericSpace"
	ptzTranslationGenericSpace = "http://www.onvif.org/ver10/tptz/PanTiltSpaces/TranslationGenericSpace"
	ptzZoomPositionSpace       = "http://www.onvif.org/ver10/tptz/ZoomSpaces/PositionGenericSpace"

	// Baichuan has no relative-move primitive, so RelativeMove is emulated as
	// a timed continuous move: |translation| * ptzDefaultRelativeMsPerUnit.
	ptzDefaultRelativeMsPerUnit = 1000
	ptzRelativeMinBurst         = 50 * time.Millisecond
	ptzRelativeMaxBurst         = 10 * time.Second
)

// ptzMoveTracker tracks in-flight emulated relative moves per camera so
// GetStatus can report MOVING and a new move replaces the previous one.
type ptzMoveTracker struct {
	mu     sync.Mutex
	active map[string]*time.Timer
}

func (t *ptzMoveTracker) start(camera string, stopAfter time.Duration, stopFn func()) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.active == nil {
		t.active = make(map[string]*time.Timer)
	}
	if old := t.active[camera]; old != nil {
		old.Stop()
	}
	var timer *time.Timer
	timer = time.AfterFunc(stopAfter, func() {
		stopFn()
		t.mu.Lock()
		if t.active[camera] == timer {
			delete(t.active, camera)
		}
		t.mu.Unlock()
	})
	t.active[camera] = timer
}

func (t *ptzMoveTracker) stop(camera string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if timer := t.active[camera]; timer != nil {
		timer.Stop()
		delete(t.active, camera)
	}
}

func (t *ptzMoveTracker) moving(camera string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.active[camera] != nil
}

func onvifPTZTokens(cameraName string) (configToken string, nodeToken string) {
	replacer := strings.NewReplacer(" ", "_", "/", "_", "\\", "_")
	cameraName = replacer.Replace(strings.TrimSpace(cameraName))
	if cameraName == "" {
		cameraName = "camera"
	}
	return "PTZConfig_" + cameraName, "PTZNode_" + cameraName
}

func (s *onvifServer) ptzServiceURL(r *http.Request) string {
	path := s.cfg.PTZPath
	if path == "" {
		path = "/onvif/ptz_service"
	}
	return buildURL("http", s.authorityForRequest(r, s.cfg.Address), path)
}

// ptzCameras returns one representative meta per camera, preserving order.
func (s *onvifServer) ptzCameras() []*streamMetadata {
	var out []*streamMetadata
	seen := make(map[string]struct{})
	for _, m := range s.metas {
		if m == nil {
			continue
		}
		if _, ok := seen[m.cameraName]; ok {
			continue
		}
		seen[m.cameraName] = struct{}{}
		out = append(out, m)
	}
	return out
}

func (s *onvifServer) handlePTZ(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	rawBody, err := io.ReadAll(io.LimitReader(r.Body, 1024*1024)) // 1MB max payload
	if err != nil {
		writeSOAPFault(w, http.StatusBadRequest, "ter:InvalidArgVal", "failed to read request body")
		return
	}
	body := string(rawBody)

	if !s.authenticate(body) {
		writeSOAPFault(w, http.StatusUnauthorized, "ter:NotAuthorized", "The action requires authorization")
		return
	}

	switch action := soapAction(r, body, []string{
		"GetServiceCapabilities",
		"GetConfigurations",
		"GetConfiguration",
		"GetConfigurationOptions",
		"GetNodes",
		"GetNode",
		"GetStatus",
		"ContinuousMove",
		"RelativeMove",
		"AbsoluteMove",
		"Stop",
		"GetPresets",
		"GotoPreset",
	}); action {
	case "GetServiceCapabilities":
		writeSOAPResponse(w, `<tptz:GetServiceCapabilitiesResponse><tptz:Capabilities EFlip="false" Reverse="false" GetCompatibleConfigurations="false" MoveStatus="true" StatusPosition="false"/></tptz:GetServiceCapabilitiesResponse>`)
	case "GetConfigurations":
		writeSOAPResponse(w, s.ptzConfigurationsResponse())
	case "GetConfiguration":
		writeSOAPResponse(w, s.ptzConfigurationResponse(body))
	case "GetConfigurationOptions":
		writeSOAPResponse(w, ptzConfigurationOptionsResponse())
	case "GetNodes":
		writeSOAPResponse(w, s.ptzNodesResponse())
	case "GetNode":
		writeSOAPResponse(w, s.ptzNodeResponse(body))
	case "GetStatus":
		writeSOAPResponse(w, s.ptzStatusResponse(body))
	case "ContinuousMove":
		s.ptzContinuousMove(w, r.Context(), body)
	case "RelativeMove":
		s.ptzRelativeMove(w, r.Context(), body)
	case "AbsoluteMove":
		s.ptzAbsoluteMove(w, r.Context(), body)
	case "Stop":
		s.ptzStop(w, r.Context(), body)
	case "GetPresets":
		s.ptzGetPresets(w, r.Context(), body)
	case "GotoPreset":
		s.ptzGotoPreset(w, r.Context(), body)
	default:
		log.Printf("onvif ptz: unsupported action %q (body: %s)", action, body)
		writeSOAPFault(w, http.StatusBadRequest, "ter:ActionNotSupported", "ptz action not supported")
	}
}

// ptzConfigurationXML emits a tt:PTZConfiguration. Sequence per onvif.xsd:
// Name, UseCount, NodeToken, DefaultAbsoluteZoomPositionSpace?,
// DefaultRelativePanTiltTranslationSpace?,
// DefaultContinuousPanTiltVelocitySpace?, DefaultPTZTimeout?.
func ptzConfigurationXML(tag string, cameraName string) string {
	configToken, nodeToken := onvifPTZTokens(cameraName)
	return fmt.Sprintf(
		`<%s token="%s"><tt:Name>%s</tt:Name><tt:UseCount>1</tt:UseCount><tt:NodeToken>%s</tt:NodeToken><tt:DefaultAbsoluteZoomPositionSpace>%s</tt:DefaultAbsoluteZoomPositionSpace><tt:DefaultRelativePanTiltTranslationSpace>%s</tt:DefaultRelativePanTiltTranslationSpace><tt:DefaultContinuousPanTiltVelocitySpace>%s</tt:DefaultContinuousPanTiltVelocitySpace><tt:DefaultPTZTimeout>PT5S</tt:DefaultPTZTimeout></%s>`,
		tag, xmlEscape(configToken), xmlEscape(configToken), xmlEscape(nodeToken), ptzZoomPositionSpace, ptzTranslationGenericSpace, ptzVelocityGenericSpace, tag,
	)
}

// ptzSpacesXML lists supported spaces in the tt:PTZSpaces sequence order:
// AbsoluteZoomPositionSpace, RelativePanTiltTranslationSpace,
// ContinuousPanTiltVelocitySpace.
func ptzSpacesXML() string {
	return `<tt:AbsoluteZoomPositionSpace><tt:URI>` + ptzZoomPositionSpace + `</tt:URI>` +
		`<tt:XRange><tt:Min>0.0</tt:Min><tt:Max>1.0</tt:Max></tt:XRange>` +
		`</tt:AbsoluteZoomPositionSpace>` +
		`<tt:RelativePanTiltTranslationSpace><tt:URI>` + ptzTranslationGenericSpace + `</tt:URI>` +
		`<tt:XRange><tt:Min>-1.0</tt:Min><tt:Max>1.0</tt:Max></tt:XRange>` +
		`<tt:YRange><tt:Min>-1.0</tt:Min><tt:Max>1.0</tt:Max></tt:YRange>` +
		`</tt:RelativePanTiltTranslationSpace>` +
		`<tt:ContinuousPanTiltVelocitySpace><tt:URI>` + ptzVelocityGenericSpace + `</tt:URI>` +
		`<tt:XRange><tt:Min>-1.0</tt:Min><tt:Max>1.0</tt:Max></tt:XRange>` +
		`<tt:YRange><tt:Min>-1.0</tt:Min><tt:Max>1.0</tt:Max></tt:YRange>` +
		`</tt:ContinuousPanTiltVelocitySpace>`
}

func (s *onvifServer) ptzConfigurationsResponse() string {
	var b strings.Builder
	b.WriteString(`<tptz:GetConfigurationsResponse>`)
	for _, m := range s.ptzCameras() {
		b.WriteString(ptzConfigurationXML("tptz:PTZConfiguration", m.cameraName))
	}
	b.WriteString(`</tptz:GetConfigurationsResponse>`)
	return b.String()
}

func (s *onvifServer) ptzConfigurationResponse(body string) string {
	meta := s.metaForPTZRequest(body)
	cameraName := ""
	if meta != nil {
		cameraName = meta.cameraName
	}
	return `<tptz:GetConfigurationResponse>` +
		ptzConfigurationXML("tptz:PTZConfiguration", cameraName) +
		`</tptz:GetConfigurationResponse>`
}

// ptzConfigurationOptionsResponse: tt:PTZConfigurationOptions requires Spaces
// and PTZTimeout (both mandatory per onvif.xsd).
func ptzConfigurationOptionsResponse() string {
	return `<tptz:GetConfigurationOptionsResponse><tptz:PTZConfigurationOptions>` +
		`<tt:Spaces>` + ptzSpacesXML() + `</tt:Spaces>` +
		`<tt:PTZTimeout><tt:Min>PT1S</tt:Min><tt:Max>PT10S</tt:Max></tt:PTZTimeout>` +
		`</tptz:PTZConfigurationOptions></tptz:GetConfigurationOptionsResponse>`
}

// ptzNodeXML emits a tt:PTZNode. Sequence per onvif.xsd: Name?,
// SupportedPTZSpaces, MaximumNumberOfPresets, HomeSupported.
func ptzNodeXML(tag string, cameraName string) string {
	_, nodeToken := onvifPTZTokens(cameraName)
	return fmt.Sprintf(
		`<%s token="%s" FixedHomePosition="false"><tt:Name>%s</tt:Name><tt:SupportedPTZSpaces>%s</tt:SupportedPTZSpaces><tt:MaximumNumberOfPresets>64</tt:MaximumNumberOfPresets><tt:HomeSupported>false</tt:HomeSupported></%s>`,
		tag, xmlEscape(nodeToken), xmlEscape(nodeToken), ptzSpacesXML(), tag,
	)
}

func (s *onvifServer) ptzNodesResponse() string {
	var b strings.Builder
	b.WriteString(`<tptz:GetNodesResponse>`)
	for _, m := range s.ptzCameras() {
		b.WriteString(ptzNodeXML("tptz:PTZNode", m.cameraName))
	}
	b.WriteString(`</tptz:GetNodesResponse>`)
	return b.String()
}

func (s *onvifServer) ptzNodeResponse(body string) string {
	meta := s.metaForPTZRequest(body)
	cameraName := ""
	if meta != nil {
		cameraName = meta.cameraName
	}
	return `<tptz:GetNodeResponse>` + ptzNodeXML("tptz:PTZNode", cameraName) + `</tptz:GetNodeResponse>`
}

// ptzStatusResponse: tt:PTZStatus requires UtcTime (last element in sequence).
// MoveStatus reflects the emulated relative-move tracker so autotracking
// clients can poll for move completion.
func (s *onvifServer) ptzStatusResponse(body string) string {
	panTilt := "IDLE"
	if meta := s.metaForPTZRequest(body); meta != nil && s.ptzMoves.moving(meta.cameraName) {
		panTilt = "MOVING"
	}
	return fmt.Sprintf(
		`<tptz:GetStatusResponse><tptz:PTZStatus><tt:MoveStatus><tt:PanTilt>%s</tt:PanTilt><tt:Zoom>IDLE</tt:Zoom></tt:MoveStatus><tt:UtcTime>%s</tt:UtcTime></tptz:PTZStatus></tptz:GetStatusResponse>`,
		panTilt,
		time.Now().UTC().Format("2006-01-02T15:04:05Z"),
	)
}

var ptzZoomTagRe = regexp.MustCompile(`<[^>]*Zoom[^>]*>`)

// extractZoomPosition pulls the x attribute off the Zoom element of an
// AbsoluteMove Position, namespace-agnostic.
func extractZoomPosition(body string) (float64, bool) {
	tag := ptzZoomTagRe.FindString(body)
	if tag == "" {
		return 0, false
	}
	m := ptzAttrXRe.FindStringSubmatch(tag)
	if m == nil {
		return 0, false
	}
	v, err := strconv.ParseFloat(m[1], 64)
	if err != nil {
		return 0, false
	}
	return v, true
}

// ptzRelativeMove emulates RelativeMove as a timed continuous move: the
// translation magnitude scales the burst duration, then the move is stopped.
// This is what enables Frigate's autotracking against Baichuan cameras.
func (s *onvifServer) ptzRelativeMove(w http.ResponseWriter, ctx context.Context, body string) {
	x, y, ok := extractPanTiltVelocity(body)
	if !ok {
		writeSOAPFault(w, http.StatusBadRequest, "ter:InvalidArgVal", "RelativeMove requires a PanTilt translation")
		return
	}

	meta := s.metaForPTZRequest(body)
	if meta == nil || meta.device == nil {
		writeSOAPFault(w, http.StatusBadRequest, "ter:NoPTZProfile", "no camera device for PTZ request")
		return
	}

	direction, _ := ptzDirection(x, y)
	if direction == "stop" {
		writeSOAPResponse(w, `<tptz:RelativeMoveResponse></tptz:RelativeMoveResponse>`)
		return
	}

	msPerUnit := meta.ptzRelativeMsPerUnit
	if msPerUnit <= 0 {
		msPerUnit = ptzDefaultRelativeMsPerUnit
	}
	mag := math.Max(math.Abs(x), math.Abs(y))
	burst := time.Duration(mag*float64(msPerUnit)) * time.Millisecond
	if burst < ptzRelativeMinBurst {
		burst = ptzRelativeMinBurst
	}
	if burst > ptzRelativeMaxBurst {
		burst = ptzRelativeMaxBurst
	}

	device := meta.device
	channel := meta.channel
	cameraName := meta.cameraName

	moveCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	err := device.WithClient(moveCtx, func(bc *baichuan.Client) error {
		return bc.PTZControl(moveCtx, channel, direction, 32)
	})
	if err != nil {
		log.Printf("onvif ptz: camera %s relative move failed: %v", cameraName, err)
		writeSOAPServerFault(w, "ter:Action", err.Error())
		return
	}

	s.ptzMoves.start(cameraName, burst, func() {
		stopCtx, cancelStop := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancelStop()
		if err := device.WithClient(stopCtx, func(bc *baichuan.Client) error {
			return bc.PTZControl(stopCtx, channel, "stop", 32)
		}); err != nil {
			log.Printf("onvif ptz: camera %s relative move stop failed: %v", cameraName, err)
		}
	})

	writeSOAPResponse(w, `<tptz:RelativeMoveResponse></tptz:RelativeMoveResponse>`)
}

// ptzAbsoluteMove supports the zoom axis only (Baichuan has an absolute zoom
// position command but no absolute pan/tilt).
func (s *onvifServer) ptzAbsoluteMove(w http.ResponseWriter, ctx context.Context, body string) {
	if x, y, ok := extractPanTiltVelocity(body); ok && (x != 0 || y != 0) {
		writeSOAPFault(w, http.StatusBadRequest, "ter:ActionNotSupported", "absolute pan/tilt is not supported by the camera protocol; only zoom")
		return
	}
	zoom, ok := extractZoomPosition(body)
	if !ok {
		writeSOAPFault(w, http.StatusBadRequest, "ter:InvalidArgVal", "AbsoluteMove requires a Zoom position")
		return
	}
	if zoom < 0 {
		zoom = 0
	}
	if zoom > 1 {
		zoom = 1
	}

	s.ptzExec(w, ctx, body, `<tptz:AbsoluteMoveResponse></tptz:AbsoluteMoveResponse>`, func(ctx context.Context, bc *baichuan.Client, channel uint8) error {
		info, err := bc.GetZoomFocus(ctx, channel)
		if err != nil {
			return err
		}
		span := float64(info.Zoom.MaxPos) - float64(info.Zoom.MinPos)
		pos := uint32(float64(info.Zoom.MinPos) + zoom*span)
		return bc.ZoomTo(ctx, channel, pos)
	})
}

// metaForPTZRequest resolves the target camera from a ProfileToken,
// ConfigurationToken, NodeToken, or PTZ config/node token in the request.
func (s *onvifServer) metaForPTZRequest(body string) *streamMetadata {
	for _, element := range []string{"ProfileToken", "ConfigurationToken", "NodeToken"} {
		token := s.extractToken(body, element)
		if token == "" {
			continue
		}
		if m := s.getMeta(token); m != nil && (m.token == token || m.name == token) {
			return m
		}
		for _, m := range s.ptzCameras() {
			configToken, nodeToken := onvifPTZTokens(m.cameraName)
			if token == configToken || token == nodeToken {
				return m
			}
		}
	}
	if len(s.metas) > 0 {
		return s.metas[0]
	}
	return nil
}

var (
	ptzPanTiltTagRe = regexp.MustCompile(`<[^>]*PanTilt[^>]*>`)
	ptzAttrXRe      = regexp.MustCompile(`\sx="([^"]+)"`)
	ptzAttrYRe      = regexp.MustCompile(`\sy="([^"]+)"`)
)

// extractPanTiltVelocity pulls the x/y attributes off the PanTilt element of a
// ContinuousMove Velocity, namespace-agnostic.
func extractPanTiltVelocity(body string) (x float64, y float64, ok bool) {
	tag := ptzPanTiltTagRe.FindString(body)
	if tag == "" {
		return 0, 0, false
	}
	if m := ptzAttrXRe.FindStringSubmatch(tag); m != nil {
		if v, err := strconv.ParseFloat(m[1], 64); err == nil {
			x = v
		}
	}
	if m := ptzAttrYRe.FindStringSubmatch(tag); m != nil {
		if v, err := strconv.ParseFloat(m[1], 64); err == nil {
			y = v
		}
	}
	return x, y, true
}

// ptzDirection maps an ONVIF velocity vector onto a Baichuan PTZ command.
// The Baichuan protocol only moves along one axis at a time, so the dominant
// axis wins. Speed scales the [0,1] magnitude onto the camera's 1..64 range.
func ptzDirection(x float64, y float64) (string, int) {
	ax, ay := math.Abs(x), math.Abs(y)
	if ax == 0 && ay == 0 {
		return "stop", 32
	}
	speed := int(math.Round(math.Max(ax, ay) * 64))
	if speed < 1 {
		speed = 1
	}
	if speed > 64 {
		speed = 64
	}
	if ax >= ay {
		if x > 0 {
			return "right", speed
		}
		return "left", speed
	}
	if y > 0 {
		return "up", speed
	}
	return "down", speed
}

func (s *onvifServer) ptzExec(w http.ResponseWriter, ctx context.Context, body string, response string, fn func(ctx context.Context, bc *baichuan.Client, channel uint8) error) {
	meta := s.metaForPTZRequest(body)
	if meta == nil || meta.device == nil {
		writeSOAPFault(w, http.StatusBadRequest, "ter:NoPTZProfile", "no camera device for PTZ request")
		return
	}

	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	err := meta.device.WithClient(ctx, func(bc *baichuan.Client) error {
		return fn(ctx, bc, meta.channel)
	})
	if err != nil {
		log.Printf("onvif ptz: camera %s command failed: %v", meta.cameraName, err)
		writeSOAPServerFault(w, "ter:Action", err.Error())
		return
	}
	writeSOAPResponse(w, response)
}

func (s *onvifServer) ptzContinuousMove(w http.ResponseWriter, ctx context.Context, body string) {
	x, y, ok := extractPanTiltVelocity(body)
	if !ok {
		writeSOAPFault(w, http.StatusBadRequest, "ter:InvalidArgVal", "ContinuousMove requires a PanTilt velocity")
		return
	}
	direction, speed := ptzDirection(x, y)
	s.ptzExec(w, ctx, body, `<tptz:ContinuousMoveResponse></tptz:ContinuousMoveResponse>`, func(ctx context.Context, bc *baichuan.Client, channel uint8) error {
		return bc.PTZControl(ctx, channel, direction, speed)
	})
}

func (s *onvifServer) ptzStop(w http.ResponseWriter, ctx context.Context, body string) {
	s.ptzExec(w, ctx, body, `<tptz:StopResponse></tptz:StopResponse>`, func(ctx context.Context, bc *baichuan.Client, channel uint8) error {
		return bc.PTZControl(ctx, channel, "stop", 32)
	})
}

func (s *onvifServer) ptzGotoPreset(w http.ResponseWriter, ctx context.Context, body string) {
	presetToken := s.extractToken(body, "PresetToken")
	presetID, err := strconv.Atoi(strings.TrimSpace(presetToken))
	if err != nil {
		writeSOAPFault(w, http.StatusBadRequest, "ter:InvalidArgVal", fmt.Sprintf("invalid preset token %q", presetToken))
		return
	}
	s.ptzExec(w, ctx, body, `<tptz:GotoPresetResponse></tptz:GotoPresetResponse>`, func(ctx context.Context, bc *baichuan.Client, channel uint8) error {
		return bc.PTZPreset(ctx, channel, presetID)
	})
}

func (s *onvifServer) ptzGetPresets(w http.ResponseWriter, ctx context.Context, body string) {
	meta := s.metaForPTZRequest(body)
	if meta == nil || meta.device == nil {
		writeSOAPFault(w, http.StatusBadRequest, "ter:NoPTZProfile", "no camera device for PTZ request")
		return
	}

	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	var presets []baichuan.PTZPresetInfo
	err := meta.device.WithClient(ctx, func(bc *baichuan.Client) error {
		var err error
		presets, err = bc.GetPTZPresets(ctx, meta.channel)
		return err
	})
	if err != nil {
		log.Printf("onvif ptz: camera %s get presets failed: %v", meta.cameraName, err)
		writeSOAPServerFault(w, "ter:Action", err.Error())
		return
	}

	var b strings.Builder
	b.WriteString(`<tptz:GetPresetsResponse>`)
	for _, preset := range presets {
		if strings.TrimSpace(preset.Name) == "" {
			continue
		}
		fmt.Fprintf(&b, `<tptz:Preset token="%d"><tt:Name>%s</tt:Name></tptz:Preset>`, preset.ID, xmlEscape(preset.Name))
	}
	b.WriteString(`</tptz:GetPresetsResponse>`)
	writeSOAPResponse(w, b.String())
}
