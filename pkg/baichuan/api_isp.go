package baichuan

import (
	"context"
	"encoding/xml"
	"fmt"
	"regexp"
	"strconv"
)

// ImageSettings are the camera's picture adjustments from the cmd 26
// <VideoInput> block. All values are the camera's native 0-255 range.
type ImageSettings struct {
	Bright     int `xml:"bright"`
	Contrast   int `xml:"contrast"`
	Saturation int `xml:"saturation"`
	Hue        int `xml:"hue"`
	Sharpen    int `xml:"sharpen"`
}

// GetImageSettings retrieves the picture adjustments (cmd 26).
func (c *Client) GetImageSettings(ctx context.Context, channel uint8) (*ImageSettings, error) {
	resp, err := c.execCommand(ctx, msgIDIspGet, channel, nil)
	if err != nil {
		return nil, err
	}

	var payload struct {
		XMLName    xml.Name       `xml:"body"`
		VideoInput *ImageSettings `xml:"VideoInput"`
	}
	if err := xml.Unmarshal([]byte(resp.XML), &payload); err != nil {
		return nil, fmt.Errorf("failed to parse VideoInput XML: %w", err)
	}
	if payload.VideoInput == nil {
		return nil, fmt.Errorf("no VideoInput in response")
	}
	return payload.VideoInput, nil
}

var ispFieldRes = map[string]*regexp.Regexp{
	"bright":     regexp.MustCompile(`(<bright>)[^<]*(</bright>)`),
	"contrast":   regexp.MustCompile(`(<contrast>)[^<]*(</contrast>)`),
	"saturation": regexp.MustCompile(`(<saturation>)[^<]*(</saturation>)`),
	"hue":        regexp.MustCompile(`(<hue>)[^<]*(</hue>)`),
	"sharpen":    regexp.MustCompile(`(<sharpen>)[^<]*(</sharpen>)`),
}

// SetImageSettings writes picture adjustments (cmd 25). The camera expects
// the full cmd 26 configuration blob back, so this is a read-modify-write:
// fetch the current XML, patch only the picture fields, and resend it —
// mirroring how the vendor clients drive this command.
func (c *Client) SetImageSettings(ctx context.Context, channel uint8, settings ImageSettings) error {
	resp, err := c.execCommand(ctx, msgIDIspGet, channel, nil)
	if err != nil {
		return err
	}

	body := resp.XML
	patch := func(field string, value int) {
		if value < 0 {
			value = 0
		}
		if value > 255 {
			value = 255
		}
		body = ispFieldRes[field].ReplaceAllString(body, "${1}"+strconv.Itoa(value)+"${2}")
	}
	patch("bright", settings.Bright)
	patch("contrast", settings.Contrast)
	patch("saturation", settings.Saturation)
	patch("hue", settings.Hue)
	patch("sharpen", settings.Sharpen)

	_, err = c.execCommand(ctx, msgIDIspSet, channel, rawXMLBody(body))
	return err
}
