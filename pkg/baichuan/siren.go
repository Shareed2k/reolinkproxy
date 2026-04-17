package baichuan

import (
	"context"
	"fmt"
)

const msgIDPlayAudio = 263

// Siren triggers the camera's internal siren alarm to sound once.
func (c *Client) Siren(ctx context.Context, channel uint8) error {
	if err := c.Login(ctx); err != nil {
		return err
	}

	body := fmt.Sprintf(`<?xml version="1.0" encoding="utf-8"?><audioPlayInfo version="1.1"><channelId>%d</channelId><playMode>0</playMode><playDuration>10</playDuration><playTimes>1</playTimes><onOff>1</onOff></audioPlayInfo>`, channel)

	resp, err := c.sendRequest(ctx, request{
		MsgID:     msgIDPlayAudio,
		ChannelID: channel,
		Class:     classModernWithOffset,
		Body:      []byte(body),
	})
	if err != nil {
		return err
	}

	return resp.success()
}
