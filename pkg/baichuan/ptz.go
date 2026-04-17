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

	var body string
	if speed > 0 {
		body = fmt.Sprintf(`<?xml version="1.0" encoding="utf-8"?><PtzControl version="1.1"><channelId>%d</channelId><speed>%d</speed><command>%s</command></PtzControl>`, channel, speed, command)
	} else {
		// Neolink uses f32 for speed but we map it to int. Some firmware strictly expects speed. We will just pass 32 if 0.
		body = fmt.Sprintf(`<?xml version="1.0" encoding="utf-8"?><PtzControl version="1.1"><channelId>%d</channelId><speed>%d</speed><command>%s</command></PtzControl>`, channel, 32, command)
	}

	resp, err := c.sendRequest(ctx, request{
		MsgID:     msgIDPTZControl,
		ChannelID: channel,
		Class:     classModernWithOffset,
		Extension: []byte(fmt.Sprintf(`<?xml version="1.0" encoding="utf-8"?><Extension version="1.1"><channelId>%d</channelId></Extension>`, channel)),
		Body:      []byte(body),
	})
	if err != nil {
		return err
	}

	return resp.success()
}
