package baichuan

import (
	"context"
	"encoding/xml"
	"strings"
	"sync"
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

// MotionEvent is one parsed cmd 33 alarm state: overall motion plus the AI
// detection types the camera reported active (e.g. people, vehicle, dog_cat,
// visitor).
type MotionEvent struct {
	Active  bool
	AITypes []string
}

// ListenForMotion subscribes to motion events and invokes the callback when motion is detected.
func (c *Client) ListenForMotion(ctx context.Context, channel uint8, callback func(MotionEvent)) (func(), error) {
	if err := c.Login(ctx); err != nil {
		return nil, err
	}

	if err := c.requireAbilityRW(ctx, channel, "motion"); err != nil {
		return nil, err
	}

	if _, err := c.sendRequest(ctx, request{
		MsgID:     msgIDMotionRequest,
		ChannelID: channel,
		Class:     classModernWithOffset,
		Body:      nil,
	}); err != nil {
		return nil, err
	}

	motionSub, unsubscribeMotion := c.Subscribe(msgIDMotion)
	stop := make(chan struct{})

	go func() {
		defer unsubscribeMotion()

		for {
			select {
			case <-ctx.Done():
				return
			case <-c.closed:
				return
			case <-stop:
				return
			case msg := <-motionSub:
				if msg == nil {
					continue
				}
				event, matched, err := parseMotionState(msg.XML, channel)
				if err == nil && matched {
					callback(event)
				}
			}
		}
	}()

	var stopOnce sync.Once
	return func() {
		stopOnce.Do(func() {
			close(stop)
		})
	}, nil
}

func parseMotionState(xmlText string, channel uint8) (MotionEvent, bool, error) {
	if xmlText == "" {
		return MotionEvent{}, false, nil
	}

	var payload AlarmMessage
	if err := xml.Unmarshal([]byte(xmlText), &payload); err != nil {
		return MotionEvent{}, false, err
	}

	if payload.AlarmEventList == nil {
		return MotionEvent{}, false, nil
	}

	for _, ev := range payload.AlarmEventList.AlarmEvents {
		if ev.ChannelID != channel {
			continue
		}

		event := MotionEvent{}
		if ev.AIType != "" && ev.AIType != "none" {
			for _, aiType := range strings.Split(ev.AIType, ",") {
				aiType = strings.TrimSpace(aiType)
				if aiType != "" && aiType != "none" {
					event.AITypes = append(event.AITypes, aiType)
				}
			}
		}
		event.Active = ev.Status != "none" || len(event.AITypes) > 0
		return event, true, nil
	}

	return MotionEvent{}, false, nil
}
