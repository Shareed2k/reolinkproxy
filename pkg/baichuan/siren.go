package baichuan

import (
	"context"
	"fmt"
)

const msgIDPlayAudio = 39

// Siren triggers the camera's internal siren alarm to sound once.
func (c *Client) Siren(ctx context.Context, channel uint8) error {
	if err := c.Login(ctx); err != nil {
		return err
	}

	body := fmt.Sprintf(`<?xml version="1.0" encoding="utf-8"?><AudioPlayInfo><channelId>%d</channelId><playMode>0</playMode><playDuration>0</playDuration><playTimes>1</playTimes><onOff>0</onOff></AudioPlayInfo>`, channel)

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
