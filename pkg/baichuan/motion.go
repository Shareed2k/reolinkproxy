package baichuan

import (
	"context"
	"encoding/xml"
)

// AlarmEventList contains a list of alarm events from the camera.
type AlarmEventList struct {
	AlarmEvents []AlarmEvent `xml:"AlarmEvent"`
}

// AlarmEvent represents a single motion or AI alarm event.
type AlarmEvent struct {
	ChannelID uint8  `xml:"channelId"`
	Status    string `xml:"status"`
	AIType    string `xml:"AItype"`
}

// AlarmMessage is the XML payload containing an AlarmEventList.
type AlarmMessage struct {
	AlarmEventList *AlarmEventList `xml:"AlarmEventList"`
}

// ListenForMotion subscribes to motion events and invokes the callback when motion is detected.
func (c *Client) ListenForMotion(ctx context.Context, channel uint8, callback func(bool)) (func(), error) {
	if err := c.Login(ctx); err != nil {
		return nil, err
	}

	msgNum := c.reserveMessageNumber()
	sub, unsubscribeReq := c.Subscribe(msgIDMotionRequest)

	if _, err := c.sendRequest(ctx, request{
		MsgID:     msgIDMotionRequest,
		MsgNum:    msgNum,
		ChannelID: channel,
		Class:     classModernWithOffset,
		Body:      nil,
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
