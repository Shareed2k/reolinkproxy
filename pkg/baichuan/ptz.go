package baichuan

import (
	"context"
	"fmt"
)

// PTZControl sends a raw PTZ command to the camera (e.g. "left", "right", "up", "down", "stop").
func (c *Client) PTZControl(ctx context.Context, channel uint8, command string, speed int) error {
	if err := c.Login(ctx); err != nil {
		return err
	}

	body := fmt.Sprintf(`<?xml version="1.0" encoding="utf-8"?><body><PtzControl version="1.1"><channelId>%d</channelId><command>%s</command><speed>%d</speed></PtzControl></body>`, channel, command, speed)

	resp, err := c.sendRequest(ctx, request{
		MsgID:     msgIDPTZControl,
		ChannelID: channel,
		Class:     classModernWithOffset,
		Body:      []byte(body),
	})
	if err != nil {
		return err
	}

	return resp.success()
}
