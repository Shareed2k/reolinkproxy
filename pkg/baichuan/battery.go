package baichuan

import (
	"context"
	"encoding/xml"
	"fmt"
)

// BatteryInfo represents the battery status and metrics of a camera.
type BatteryInfo struct {
	ChannelID      uint8  `xml:"channelId"`
	ChargeStatus   string `xml:"chargeStatus"`
	AdapterStatus  string `xml:"adapterStatus"`
	Voltage        int32  `xml:"voltage"`
	Current        int32  `xml:"current"`
	Temperature    int32  `xml:"temperature"`
	BatteryPercent uint32 `xml:"batteryPercent"`
	LowPower       uint32 `xml:"lowPower"`
	BatteryVersion uint32 `xml:"batteryVersion"`
}

// BatteryMessage is the XML payload for battery information.
type BatteryMessage struct {
	BatteryInfo *BatteryInfo `xml:"BatteryInfo"`
}

// GetBattery retrieves battery status from the camera for the given channel.
func (c *Client) GetBattery(ctx context.Context, channel uint8) (*BatteryInfo, error) {
	if err := c.Login(ctx); err != nil {
		return nil, err
	}

	resp, err := c.sendRequest(ctx, request{
		MsgID:     msgIDBatteryInfo,
		ChannelID: channel,
		Class:     classModernWithOffset,
		Body:      nil,
	})
	if err != nil {
		return nil, err
	}

	if err := resp.success(); err != nil {
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
