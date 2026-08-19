package main

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/shareed2k/reolinkproxy/pkg/baichuan"
)

// ONVIF Imaging service (ver20/imaging/wsdl) backed by the Baichuan picture
// settings (cmd 26 read / cmd 25 read-modify-write). The camera's native
// 0-255 range is exposed directly in GetOptions.

func (s *onvifServer) imagingServiceURL(r *http.Request) string {
	path := s.cfg.ImagingPath
	if path == "" {
		path = "/onvif/imaging_service"
	}
	return buildURL("http", s.authorityForRequest(r, s.cfg.Address), path)
}

// metaForImagingRequest resolves the VideoSourceToken ("VideoSource_<cam>")
// to a camera stream.
func (s *onvifServer) metaForImagingRequest(body string) *streamMetadata {
	token := extractTokenValue(body, "VideoSourceToken")
	if token != "" {
		name := strings.TrimPrefix(token, "VideoSource_")
		for _, m := range s.metas {
			if m != nil && m.cameraName == name {
				return m
			}
		}
		return nil
	}
	if len(s.metas) > 0 {
		return s.metas[0]
	}
	return nil
}

func (s *onvifServer) handleImaging(w http.ResponseWriter, r *http.Request) {
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
		"GetImagingSettings",
		"SetImagingSettings",
		"GetOptions",
	}); action {
	case "GetServiceCapabilities":
		writeSOAPResponse(w, `<timg:GetServiceCapabilitiesResponse><timg:Capabilities ImageStabilization="false" Presets="false"/></timg:GetServiceCapabilitiesResponse>`)
	case "GetImagingSettings":
		s.imagingGetSettings(r.Context(), w, body)
	case "SetImagingSettings":
		s.imagingSetSettings(r.Context(), w, body)
	case "GetOptions":
		writeSOAPResponse(w, imagingOptionsResponse())
	default:
		log.Printf("onvif imaging: unsupported action %q (body: %s)", action, body)
		writeSOAPFault(w, http.StatusBadRequest, "ter:ActionNotSupported", "imaging action not supported")
	}
}

func (s *onvifServer) imagingWithDevice(ctx context.Context, w http.ResponseWriter, body string, fn func(ctx context.Context, bc *baichuan.Client, channel uint8) (string, error)) {
	meta := s.metaForImagingRequest(body)
	if meta == nil || meta.device == nil {
		writeSOAPFault(w, http.StatusBadRequest, "ter:InvalidArgVal", "unknown video source token")
		return
	}

	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	var response string
	err := meta.device.WithClient(ctx, func(bc *baichuan.Client) error {
		var err error
		response, err = fn(ctx, bc, meta.channel)
		return err
	})
	if err != nil {
		log.Printf("onvif imaging: camera %s failed: %v", meta.cameraName, err)
		writeSOAPServerFault(w, err.Error())
		return
	}
	writeSOAPResponse(w, response)
}

// imagingGetSettings emits tt:ImagingSettings20 in xsd sequence order:
// Brightness, ColorSaturation, Contrast, ..., Sharpness.
func (s *onvifServer) imagingGetSettings(ctx context.Context, w http.ResponseWriter, body string) {
	s.imagingWithDevice(ctx, w, body, func(ctx context.Context, bc *baichuan.Client, channel uint8) (string, error) {
		settings, err := bc.GetImageSettings(ctx, channel)
		if err != nil {
			return "", err
		}
		return fmt.Sprintf(
			`<timg:GetImagingSettingsResponse><timg:ImagingSettings><tt:Brightness>%d</tt:Brightness><tt:ColorSaturation>%d</tt:ColorSaturation><tt:Contrast>%d</tt:Contrast><tt:Sharpness>%d</tt:Sharpness></timg:ImagingSettings></timg:GetImagingSettingsResponse>`,
			settings.Bright, settings.Saturation, settings.Contrast, settings.Sharpen,
		), nil
	})
}

func (s *onvifServer) imagingSetSettings(ctx context.Context, w http.ResponseWriter, body string) {
	parse := func(element string) (int, bool) {
		raw := extractTokenValue(body, element)
		if raw == "" {
			return 0, false
		}
		v, err := strconv.ParseFloat(raw, 64)
		if err != nil {
			return 0, false
		}
		return int(v), true
	}

	s.imagingWithDevice(ctx, w, body, func(ctx context.Context, bc *baichuan.Client, channel uint8) (string, error) {
		settings, err := bc.GetImageSettings(ctx, channel)
		if err != nil {
			return "", err
		}
		if v, ok := parse("Brightness"); ok {
			settings.Bright = v
		}
		if v, ok := parse("ColorSaturation"); ok {
			settings.Saturation = v
		}
		if v, ok := parse("Contrast"); ok {
			settings.Contrast = v
		}
		if v, ok := parse("Sharpness"); ok {
			settings.Sharpen = v
		}
		if err := bc.SetImageSettings(ctx, channel, *settings); err != nil {
			return "", err
		}
		return `<timg:SetImagingSettingsResponse></timg:SetImagingSettingsResponse>`, nil
	})
}

func imagingOptionsResponse() string {
	rangeXML := func(tag string) string {
		return fmt.Sprintf(`<tt:%s><tt:Min>0.0</tt:Min><tt:Max>255.0</tt:Max></tt:%s>`, tag, tag)
	}
	return `<timg:GetOptionsResponse><timg:ImagingOptions>` +
		rangeXML("Brightness") +
		rangeXML("ColorSaturation") +
		rangeXML("Contrast") +
		rangeXML("Sharpness") +
		`</timg:ImagingOptions></timg:GetOptionsResponse>`
}
