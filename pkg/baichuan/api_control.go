package baichuan

import (
	"context"
	"encoding/xml"
	"fmt"
)

// execCommand is a generic helper to send XML commands to the camera.
func (c *Client) execCommand(ctx context.Context, msgID uint32, channel uint8, body any) (*Message, error) {
	if err := c.Login(ctx); err != nil {
		return nil, err
	}

	var bodyBytes []byte
	var err error
	if body != nil {
		bodyBytes, err = marshalXMLDocument(body)
		if err != nil {
			return nil, fmt.Errorf("marshal xml: %w", err)
		}
	}

	req := request{
		MsgID:     msgID,
		ChannelID: channel,
		Class:     classModernWithOffset,
		Body:      bodyBytes,
	}

	// PTZ Control needs an extension
	if msgID == msgIDPTZControl {
		req.Extension = []byte(fmt.Sprintf(`<?xml version="1.0" encoding="utf-8"?><Extension version="1.1"><channelId>%d</channelId></Extension>`, channel))
	}

	resp, err := c.sendRequest(ctx, req)
	if err != nil {
		return nil, err
	}

	if err := resp.success(); err != nil {
		return nil, err
	}

	return resp, nil
}

// Siren triggers the camera's internal siren alarm to sound continuously (manual mode).
func (c *Client) Siren(ctx context.Context, channel uint8, enable int) error {
	_, err := c.execCommand(ctx, msgIDPlayAudio, channel, xmlAudioPlayInfoBody{
		AudioPlayInfo: xmlAudioPlayInfo{Version: "1.1", ChannelID: channel, PlayMode: 2, PlayDuration: 10, PlayTimes: 1, OnOff: enable},
	})
	return err
}

// SirenTimes triggers the camera's internal siren alarm to sound for a specific number of times.
func (c *Client) SirenTimes(ctx context.Context, channel uint8, times int) error {
	_, err := c.execCommand(ctx, msgIDPlayAudio, channel, xmlAudioPlayInfoBody{
		AudioPlayInfo: xmlAudioPlayInfo{Version: "1.1", ChannelID: channel, PlayMode: 0, PlayDuration: 10, PlayTimes: times, OnOff: 1},
	})
	return err
}

// SirenHub triggers the Hub's internal siren alarm to sound continuously (manual mode).
func (c *Client) SirenHub(ctx context.Context, enable int) error {
	_, err := c.execCommand(ctx, msgIDPlayAudio, 0, xmlAudioPlayInfoBody{
		AudioPlayInfo: xmlAudioPlayInfo{Version: "1.1", PlayMode: 2, PlayDuration: 10, PlayTimes: 1, OnOff: enable},
	})
	return err
}

// SirenHubTimes triggers the Hub's internal siren alarm to sound for a specific number of times.
func (c *Client) SirenHubTimes(ctx context.Context, times int) error {
	_, err := c.execCommand(ctx, msgIDPlayAudio, 0, xmlAudioPlayInfoBody{
		AudioPlayInfo: xmlAudioPlayInfo{Version: "1.1", PlayMode: 0, PlayDuration: 10, PlayTimes: times, OnOff: 1},
	})
	return err
}

// SetWhiteLed enables or disables the white LED (floodlight).
func (c *Client) SetWhiteLed(ctx context.Context, channel uint8, status int) error {
	_, err := c.execCommand(ctx, msgIDWhiteLedSet, channel, xmlFloodlightManualBody{
		FloodlightManual: xmlFloodlightManual{Version: "1.1", ChannelID: channel, Status: status, Duration: 180},
	})
	return err
}

// GetWhiteLed retrieves the current state of the floodlight.
func (c *Client) GetWhiteLed(ctx context.Context, channel uint8) (*Message, error) {
	return c.execCommand(ctx, msgIDWhiteLedGet, channel, nil)
}

// SetPrivacyMode puts the camera into privacy/sleep mode.
func (c *Client) SetPrivacyMode(ctx context.Context, channel uint8, enable int) error {
	_, err := c.execCommand(ctx, msgIDPrivacyModeSet, channel, xmlSleepStateBody{
		SleepState: xmlSleepState{Version: "1.1", Operate: 2, Sleep: enable},
	})
	return err
}

// GetPrivacyMode retrieves the current privacy/sleep mode state.
func (c *Client) GetPrivacyMode(ctx context.Context, channel uint8) (*Message, error) {
	return c.execCommand(ctx, msgIDPrivacyModeGet, channel, nil)
}

// SetAutoFocus enables or disables auto-focus on supported cameras.
func (c *Client) SetAutoFocus(ctx context.Context, channel uint8, disable int) error {
	_, err := c.execCommand(ctx, msgIDAutoFocusSet, channel, xmlAutoFocusBody{
		AutoFocus: xmlAutoFocus{Version: "1.1", ChannelID: channel, Disable: disable},
	})
	return err
}

// GetAutoFocus retrieves the current state of auto-focus.
func (c *Client) GetAutoFocus(ctx context.Context, channel uint8) (*Message, error) {
	return c.execCommand(ctx, msgIDAutoFocusGet, channel, nil)
}

// RingChimeWithTone rings the chime using a specific tone
func (c *Client) RingChimeWithTone(ctx context.Context, channel uint8, chimeID int, toneID int) error {
	_, err := c.execCommand(ctx, msgIDDingDongOpt2, channel, xmlDingDongOptBody{
		DingdongDeviceOpt: xmlDingDongOpt{Version: "1.1", Opt: "ringWithMusic", ID: chimeID, MusicID: toneID},
	})
	return err
}

// GetChimeConfig retrieves the configuration of a paired chime
func (c *Client) GetChimeConfig(ctx context.Context, channel uint8) (*Message, error) {
	return c.execCommand(ctx, msgIDDingDongGet, channel, nil)
}

// SetChimeSilentMode sets the silent mode (DND) on the chime for a specific duration (in minutes)
func (c *Client) SetChimeSilentMode(ctx context.Context, channel uint8, chimeID int, time int) error {
	_, err := c.execCommand(ctx, msgIDDingDongSilentSet, channel, xmlDingDongSilentBody{
		DingdongSilentMode: xmlDingDongSilentMode{Version: "1.1", ID: chimeID, Time: time, Type: 63},
	})
	return err
}

// PlayQuickReply plays a pre-recorded audio file on the camera's speaker.
func (c *Client) PlayQuickReply(ctx context.Context, channel uint8, fileID int) error {
	_, err := c.execCommand(ctx, msgIDQuickReplyPlay, channel, xmlAudioFileInfoBody{
		AudioFileInfo: xmlAudioFileInfo{Version: "1.1", ChannelID: channel, ID: fileID, Timeout: 0},
	})
	return err
}

// PTZControl sends a raw PTZ command to the camera.
func (c *Client) PTZControl(ctx context.Context, channel uint8, command string, speed int) error {
	if speed == 0 {
		speed = 32
	}
	_, err := c.execCommand(ctx, msgIDPTZControl, channel, xmlPtzControlBody{
		PtzControl: xmlPtzControl{Version: "1.1", ChannelID: channel, Command: command, Speed: speed},
	})
	return err
}

// PTZPreset moves the camera to a saved PTZ preset ID.
func (c *Client) PTZPreset(ctx context.Context, channel uint8, presetID int) error {
	_, err := c.execCommand(ctx, msgIDPTZControlPreset, channel, ptzPresetBody(channel, presetID))
	return err
}

// ptzPresetBody builds the cmd 19 payload. The camera only understands the
// commands "toPos" and "setPos" (case-sensitive); anything else returns 400.
func ptzPresetBody(channel uint8, presetID int) xmlPtzPresetBody {
	return xmlPtzPresetBody{
		PtzPreset: xmlPtzPreset{Version: "1.1", ChannelID: channel, PresetList: xmlPtzPresetList{Preset: xmlPtzPresetItem{ID: presetID, Command: "toPos"}}},
	}
}

// PTZPresetInfo is one saved PTZ preset position on the camera.
type PTZPresetInfo struct {
	ID   int    `xml:"id"`
	Name string `xml:"name"`
}

// GetPTZPresets retrieves the list of PTZ presets stored on the camera (cmd 190).
func (c *Client) GetPTZPresets(ctx context.Context, channel uint8) ([]PTZPresetInfo, error) {
	resp, err := c.execCommand(ctx, msgIDGetPtzPreset, channel, nil)
	if err != nil {
		return nil, err
	}

	var payload struct {
		XMLName   xml.Name `xml:"body"`
		PtzPreset *struct {
			PresetList struct {
				Preset []PTZPresetInfo `xml:"preset"`
			} `xml:"presetList"`
		} `xml:"PtzPreset"`
	}
	if err := xml.Unmarshal([]byte(resp.XML), &payload); err != nil {
		return nil, fmt.Errorf("failed to parse PtzPreset XML: %w", err)
	}
	if payload.PtzPreset == nil {
		return nil, nil
	}
	return payload.PtzPreset.PresetList.Preset, nil
}

// PtzGuard sets the guard position or patrol for a PTZ camera.
func (c *Client) PtzGuard(ctx context.Context, channel uint8, enable int, cmdStr string, timeout int, setPos int) error {
	_, err := c.execCommand(ctx, msgIDPtzGuardSet, channel, xmlPtzGuardBody{
		PtzGuard: xmlPtzGuard{Version: "1.1", ChannelID: channel, Benable: enable, Command: cmdStr, Timeout: timeout, NeedSetPos: setPos},
	})
	return err
}

// Ptz3DLocation zooms or centers the camera onto a specific 3D box region.
func (c *Client) Ptz3DLocation(ctx context.Context, channel uint8, posX, posY, posWidth, posHeight, speed, width, height int) error {
	_, err := c.execCommand(ctx, msgIDPtz3DLocation, channel, xmlPtz3DLocationBody{
		Ptz3DLocation: xmlPtz3DLocation{Version: "1.1", ChannelID: channel, PosX: posX, PosY: posY, PosWidth: posWidth, PosHeight: posHeight, Speed: speed, Width: width, Height: height},
	})
	return err
}

// Reboot sends a reboot command to the camera channel.
func (c *Client) Reboot(ctx context.Context, channel uint8) error {
	_, err := c.execCommand(ctx, msgIDReboot, channel, xmlRebootBody{
		Reboot: xmlReboot{Channel: channel},
	})
	return err
}

// GetBattery retrieves battery status from the camera for the given channel.
func (c *Client) GetBattery(ctx context.Context, channel uint8) (*BatteryInfo, error) {
	resp, err := c.execCommand(ctx, msgIDBatteryInfo, channel, nil)
	if err != nil {
		return nil, err
	}

	var payload BatteryMessage
	if err := xml.Unmarshal([]byte(resp.XML), &payload); err != nil {
		return nil, fmt.Errorf("failed to parse battery XML: %w", err)
	}

	if payload.BatteryInfo == nil {
		return nil, fmt.Errorf("no BatteryInfo in response")
	}

	return payload.BatteryInfo, nil
}
