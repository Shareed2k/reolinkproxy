package baichuan

import (
	"context"
	"encoding/xml"
	"fmt"
	"time"
)

// RecordingFile is one recorded clip found on the camera's SD card.
type RecordingFile struct {
	FileName  string
	AlarmType string
	StartTime time.Time
	EndTime   time.Time
}

type xmlRecTime struct {
	Year   int `xml:"year"`
	Month  int `xml:"month"`
	Day    int `xml:"day"`
	Hour   int `xml:"hour"`
	Minute int `xml:"minute"`
	Second int `xml:"second"`
}

func toXMLRecTime(t time.Time) xmlRecTime {
	return xmlRecTime{Year: t.Year(), Month: int(t.Month()), Day: t.Day(), Hour: t.Hour(), Minute: t.Minute(), Second: t.Second()}
}

func (t xmlRecTime) time() time.Time {
	return time.Date(t.Year, time.Month(t.Month), t.Day, t.Hour, t.Minute, t.Second, 0, time.Local)
}

// vodAlarmTypes is the full alarm-type filter the vendor clients request.
const vodAlarmTypes = "md, pir, io, people, face, vehicle, dog_cat, visitor, other, package, cry, crossline, intrusion, loitering, legacy, loss"

type xmlFindAlarmVideoOpenBody struct {
	XMLName        xml.Name              `xml:"body"`
	FindAlarmVideo xmlFindAlarmVideoOpen `xml:"findAlarmVideo"`
}

type xmlFindAlarmVideoOpen struct {
	Version        string     `xml:"version,attr"`
	ChannelID      uint8      `xml:"channelId"`
	UID            string     `xml:"uid"`
	LogicChnBitmap int        `xml:"logicChnBitmap"`
	StreamType     int        `xml:"streamType"`
	NotSearchVideo int        `xml:"notSearchVideo"`
	StartTime      xmlRecTime `xml:"startTime"`
	EndTime        xmlRecTime `xml:"endTime"`
	AlarmType      string     `xml:"alarmType"`
}

type xmlFindAlarmVideoPageBody struct {
	XMLName        xml.Name              `xml:"body"`
	FindAlarmVideo xmlFindAlarmVideoPage `xml:"findAlarmVideo"`
}

type xmlFindAlarmVideoPage struct {
	Version    string `xml:"version,attr"`
	ChannelID  uint8  `xml:"channelId"`
	FileHandle string `xml:"fileHandle"`
}

// GetUID retrieves the camera's UID (cmd 114); falls back to the configured
// UID without a round-trip when one is known.
func (c *Client) GetUID(ctx context.Context) (string, error) {
	if c.cfg.UID != "" {
		return c.cfg.UID, nil
	}

	resp, err := c.execCommand(ctx, msgIDGetUid, 0, nil)
	if err != nil {
		return "", err
	}
	var payload struct {
		XMLName xml.Name `xml:"body"`
		UID     string   `xml:"Uid>uid"`
	}
	if err := xml.Unmarshal([]byte(resp.XML), &payload); err != nil {
		return "", fmt.Errorf("failed to parse Uid XML: %w", err)
	}
	if payload.UID == "" {
		return "", fmt.Errorf("camera returned no uid")
	}
	return payload.UID, nil
}

// FindRecordings lists SD-card clips in [start, end] via the alarm-video
// search commands (cmd 272 open -> 273 page -> 274 close), paginating the way
// the vendor clients do.
func (c *Client) FindRecordings(ctx context.Context, channel uint8, start, end time.Time) ([]RecordingFile, error) {
	uid, err := c.GetUID(ctx)
	if err != nil {
		return nil, fmt.Errorf("resolve camera uid: %w", err)
	}

	var files []RecordingFile
	for iteration := 0; iteration < 50; iteration++ {
		openResp, err := c.execCommand(ctx, msgIDFindAlarmVideoOpen, channel, xmlFindAlarmVideoOpenBody{
			FindAlarmVideo: xmlFindAlarmVideoOpen{
				Version:        "1.1",
				ChannelID:      channel,
				UID:            uid,
				LogicChnBitmap: 255,
				NotSearchVideo: 0,
				StartTime:      toXMLRecTime(start),
				EndTime:        toXMLRecTime(end),
				AlarmType:      vodAlarmTypes,
			},
		})
		if err != nil {
			return nil, err
		}

		var openPayload struct {
			XMLName    xml.Name `xml:"body"`
			FileHandle string   `xml:"findAlarmVideo>fileHandle"`
		}
		if err := xml.Unmarshal([]byte(openResp.XML), &openPayload); err != nil {
			return nil, fmt.Errorf("failed to parse findAlarmVideo open XML: %w", err)
		}

		pageBody := xmlFindAlarmVideoPageBody{
			FindAlarmVideo: xmlFindAlarmVideoPage{Version: "1.1", ChannelID: channel, FileHandle: openPayload.FileHandle},
		}
		pageResp, err := c.execCommand(ctx, msgIDFindAlarmVideoGet, channel, pageBody)
		closeSearch := func() {
			_, _ = c.execCommand(ctx, msgIDFindAlarmVideoClose, channel, pageBody)
		}
		if err != nil {
			closeSearch()
			return nil, err
		}

		var page struct {
			XMLName xml.Name `xml:"body"`
			Info    *struct {
				BFinished int `xml:"bFinished"`
				List      struct {
					Videos []struct {
						FileName  string     `xml:"fileName"`
						AlarmType string     `xml:"alarmType"`
						StartTime xmlRecTime `xml:"startTime"`
						EndTime   xmlRecTime `xml:"endTime"`
					} `xml:"alarmVideo"`
				} `xml:"alarmVideoList"`
			} `xml:"alarmVideoInfo"`
		}
		if err := xml.Unmarshal([]byte(pageResp.XML), &page); err != nil {
			closeSearch()
			return nil, fmt.Errorf("failed to parse alarmVideoInfo XML: %w", err)
		}
		if page.Info == nil {
			closeSearch()
			break
		}

		var lastEvent time.Time
		for _, video := range page.Info.List.Videos {
			file := RecordingFile{
				FileName:  video.FileName,
				AlarmType: video.AlarmType,
				StartTime: video.StartTime.time(),
				EndTime:   video.EndTime.time(),
			}
			files = append(files, file)
			lastEvent = file.StartTime
		}

		closeSearch()

		if page.Info.BFinished != 0 {
			break
		}
		if lastEvent.IsZero() {
			break
		}
		start = lastEvent
	}

	return files, nil
}
