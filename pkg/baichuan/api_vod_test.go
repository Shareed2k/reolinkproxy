package baichuan

import (
	"encoding/xml"
	"strings"
	"testing"
	"time"
)

func TestFindAlarmVideoOpenBody(t *testing.T) {
	t.Parallel()

	start := time.Date(2026, 8, 19, 0, 0, 0, 0, time.Local)
	end := time.Date(2026, 8, 19, 23, 59, 59, 0, time.Local)
	data, err := xml.Marshal(xmlFindAlarmVideoOpenBody{
		FindAlarmVideo: xmlFindAlarmVideoOpen{
			Version:        "1.1",
			ChannelID:      0,
			UID:            "UID123",
			LogicChnBitmap: 255,
			NotSearchVideo: 0,
			StartTime:      toXMLRecTime(start),
			EndTime:        toXMLRecTime(end),
			AlarmType:      vodAlarmTypes,
		},
	})
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}

	got := string(data)
	for _, want := range []string{
		`<findAlarmVideo version="1.1">`,
		`<uid>UID123</uid>`,
		`<logicChnBitmap>255</logicChnBitmap>`,
		`<startTime><year>2026</year><month>8</month><day>19</day>`,
		`<alarmType>md, pir, io, people, face, vehicle, dog_cat, visitor, other, package, cry, crossline, intrusion, loitering, legacy, loss</alarmType>`,
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("open body %q missing %q", got, want)
		}
	}
}

func TestAlarmVideoInfoParsing(t *testing.T) {
	t.Parallel()

	reply := `<?xml version="1.0" encoding="UTF-8"?><body><alarmVideoInfo version="1.1"><channelId>0</channelId><bFinished>1</bFinished><alarmVideoList><alarmVideo><fileName>01_20260819100000.mp4</fileName><alarmType>people</alarmType><startTime><year>2026</year><month>8</month><day>19</day><hour>10</hour><minute>0</minute><second>0</second></startTime><endTime><year>2026</year><month>8</month><day>19</day><hour>10</hour><minute>0</minute><second>30</second></endTime></alarmVideo></alarmVideoList></alarmVideoInfo></body>`

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
	if err := xml.Unmarshal([]byte(reply), &page); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	if page.Info == nil || page.Info.BFinished != 1 || len(page.Info.List.Videos) != 1 {
		t.Fatalf("unexpected parse result: %+v", page.Info)
	}
	video := page.Info.List.Videos[0]
	if video.FileName != "01_20260819100000.mp4" || video.AlarmType != "people" {
		t.Fatalf("video = %+v", video)
	}
	if video.StartTime.time().Hour() != 10 || video.EndTime.time().Second() != 30 {
		t.Fatalf("times = %v .. %v", video.StartTime.time(), video.EndTime.time())
	}
}
