package baichuan

import (
	"context"
	"encoding/xml"
	"fmt"
)

// snapMaxBytes is a sanity cap for a single snapshot JPEG.
const snapMaxBytes = 8 << 20

// Snap captures a JPEG snapshot from the camera (cmd 109).
//
// Protocol (mirrors neolink's get_snapshot): the XML request is answered by a
// Snap XML carrying fileName/pictureSize; the JPEG bytes then arrive as
// separate binary messages on cmd 109 with fresh message numbers — response
// code 200 while more data follows, 201 with the final chunk.
func (c *Client) Snap(ctx context.Context, channel uint8, streamType string) ([]byte, error) {
	if streamType != "main" && streamType != "sub" {
		streamType = "main"
	}

	// Subscribe before sending so no binary chunk can be missed.
	sub, unsubscribe := c.Subscribe(msgIDSnap)
	defer unsubscribe()

	resp, err := c.execCommand(ctx, msgIDSnap, channel, snapRequestBody(channel, streamType))
	if err != nil {
		return nil, err
	}

	var payload struct {
		XMLName xml.Name `xml:"body"`
		Snap    *xmlSnap `xml:"Snap"`
	}
	if err := xml.Unmarshal([]byte(resp.XML), &payload); err != nil {
		return nil, fmt.Errorf("failed to parse Snap XML: %w", err)
	}
	if payload.Snap == nil || payload.Snap.PictureSize == 0 {
		return nil, fmt.Errorf("camera returned no snapshot info")
	}
	expected := int(payload.Snap.PictureSize)
	if expected > snapMaxBytes {
		return nil, fmt.Errorf("snapshot size %d exceeds %d byte limit", expected, snapMaxBytes)
	}

	jpeg := make([]byte, 0, expected)
	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-c.Done():
			return nil, c.Err()
		case msg := <-sub:
			isBinary := msg.Binary ||
				(msg.ExtensionMeta != nil && msg.ExtensionMeta.BinaryData != nil && *msg.ExtensionMeta.BinaryData == 1)
			if !isBinary {
				continue // e.g. the XML reply echoed to the subscription
			}

			jpeg = append(jpeg, msg.Payload...)
			if len(jpeg) > snapMaxBytes {
				return nil, fmt.Errorf("snapshot exceeded %d byte limit", snapMaxBytes)
			}

			switch msg.Header.ResponseCode {
			case 200:
				// more chunks follow
			case 201:
				return jpeg, nil
			default:
				return nil, fmt.Errorf("baichuan cmd %d failed with status %d", msgIDSnap, msg.Header.ResponseCode)
			}
		}
	}
}

func snapRequestBody(channel uint8, streamType string) xmlSnapBody {
	logicChannel := channel
	fullFrame := uint32(0)
	return xmlSnapBody{
		Snap: xmlSnap{
			Version:      "1.1",
			ChannelID:    channel,
			LogicChannel: &logicChannel,
			Time:         0,
			FullFrame:    &fullFrame,
			StreamType:   streamType,
		},
	}
}
