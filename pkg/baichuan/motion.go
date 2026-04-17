package baichuan

import (
	"context"
	"encoding/xml"
	"fmt"
)

type AlarmEventList struct {
	AlarmEvents []AlarmEvent `xml:"AlarmEvent"`
}

type AlarmEvent struct {
	ChannelID uint8  `xml:"channelId"`
	Status    string `xml:"status"`
	AIType    string `xml:"AItype"`
}

type AlarmMessage struct {
	AlarmEventList *AlarmEventList `xml:"AlarmEventList"`
}

func (c *Client) ListenForMotion(ctx context.Context, channel uint8, callback func(bool)) (func(), error) {
	if err := c.Login(ctx); err != nil {
		return nil, err
	}

	msgNum := c.reserveMessageNumber()
	sub, unsubscribeReq := c.Subscribe(msgIDMotionRequest)

	body := fmt.Sprintf(`<?xml version="1.0" encoding="utf-8"?><Alarm><channel>%d</channel></Alarm>`, channel)

	if _, err := c.sendRequest(ctx, request{
		MsgID:     msgIDMotionRequest,
		MsgNum:    msgNum,
		ChannelID: channel,
		Class:     classModernWithOffset,
		Body:      []byte(body),
	}); err != nil {
		unsubscribeReq()
		return nil, err
	}

	// wait for ack
	select {
	case <-ctx.Done():
		unsubscribeReq()
		return nil, ctx.Err()
	case <-sub:
		// good
	}
	unsubscribeReq()

	motionSub, unsubscribeMotion := c.Subscribe(msgIDMotion)

	go func() {
		defer unsubscribeMotion()

		for {
			select {
			case <-ctx.Done():
				return
			case <-c.closed:
				return
			case msg := <-motionSub:
				if msg == nil {
					continue
				}

				if msg.XML != "" {
					var payload AlarmMessage
					if err := xml.Unmarshal([]byte(msg.XML), &payload); err == nil && payload.AlarmEventList != nil {
						motionDetected := false
						for _, ev := range payload.AlarmEventList.AlarmEvents {
							if ev.ChannelID == channel {
								if ev.Status != "none" || (ev.AIType != "" && ev.AIType != "none") {
									motionDetected = true
									break
								}
							}
						}
						callback(motionDetected)
					}
				}
			}
		}
	}()

	return unsubscribeMotion, nil
}
