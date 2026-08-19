package baichuan

import "encoding/xml"

type xmlAudioPlayInfoBody struct {
	XMLName       xml.Name         `xml:"body"`
	AudioPlayInfo xmlAudioPlayInfo `xml:"audioPlayInfo"`
}

type xmlAudioPlayInfo struct {
	Version      string `xml:"version,attr,omitempty"`
	ChannelID    uint8  `xml:"channelId,omitempty"`
	PlayMode     int    `xml:"playMode"`
	PlayDuration int    `xml:"playDuration"`
	PlayTimes    int    `xml:"playTimes"`
	OnOff        int    `xml:"onOff"`
}

type xmlFloodlightManualBody struct {
	XMLName          xml.Name            `xml:"body"`
	FloodlightManual xmlFloodlightManual `xml:"FloodlightManual"`
}

type xmlFloodlightManual struct {
	Version   string `xml:"version,attr,omitempty"`
	ChannelID uint8  `xml:"channelId"`
	Status    int    `xml:"status"`
	Duration  int    `xml:"duration"`
}

type xmlSleepStateBody struct {
	XMLName    xml.Name      `xml:"body"`
	SleepState xmlSleepState `xml:"sleepState"`
}

type xmlSleepState struct {
	Version string `xml:"version,attr,omitempty"`
	Operate int    `xml:"operate"`
	Sleep   int    `xml:"sleep"`
}

type xmlAutoFocusBody struct {
	XMLName   xml.Name     `xml:"body"`
	AutoFocus xmlAutoFocus `xml:"AutoFocus"`
}

type xmlAutoFocus struct {
	Version   string `xml:"version,attr,omitempty"`
	ChannelID uint8  `xml:"channelId"`
	Disable   int    `xml:"disable"`
}

type xmlDingDongOptBody struct {
	XMLName           xml.Name       `xml:"body"`
	DingdongDeviceOpt xmlDingDongOpt `xml:"dingdongDeviceOpt"`
}

type xmlDingDongOpt struct {
	Version  string `xml:"version,attr,omitempty"`
	Opt      string `xml:"opt"`
	ID       int    `xml:"id"`
	VolLevel int    `xml:"volLevel,omitempty"`
	LedState int    `xml:"ledState,omitempty"`
	Name     string `xml:"name,omitempty"`
	MusicID  int    `xml:"musicId,omitempty"`
}

type xmlDingDongSilentBody struct {
	XMLName            xml.Name              `xml:"body"`
	DingdongSilentMode xmlDingDongSilentMode `xml:"dingdongSilentMode"`
}

type xmlDingDongSilentMode struct {
	Version string `xml:"version,attr,omitempty"`
	ID      int    `xml:"id"`
	Time    int    `xml:"time,omitempty"`
	Type    int    `xml:"type,omitempty"`
}

type xmlAudioFileInfoBody struct {
	XMLName       xml.Name         `xml:"body"`
	AudioFileInfo xmlAudioFileInfo `xml:"audioFileInfo"`
}

type xmlAudioFileInfo struct {
	Version   string `xml:"version,attr,omitempty"`
	ChannelID uint8  `xml:"channelId"`
	ID        int    `xml:"id"`
	Timeout   int    `xml:"timeout"`
}

type xmlPtzControlBody struct {
	XMLName    xml.Name      `xml:"body"`
	PtzControl xmlPtzControl `xml:"PtzControl"`
}

type xmlPtzControl struct {
	Version   string `xml:"version,attr,omitempty"`
	ChannelID uint8  `xml:"channelId"`
	Command   string `xml:"command"`
	Speed     int    `xml:"speed"`
}

type xmlPtzPresetBody struct {
	XMLName   xml.Name     `xml:"body"`
	PtzPreset xmlPtzPreset `xml:"PtzPreset"`
}

type xmlPtzPreset struct {
	Version    string           `xml:"version,attr,omitempty"`
	ChannelID  uint8            `xml:"channelId"`
	PresetList xmlPtzPresetList `xml:"presetList"`
}

type xmlPtzPresetList struct {
	Preset xmlPtzPresetItem `xml:"preset"`
}

type xmlPtzPresetItem struct {
	ID      int    `xml:"id"`
	Command string `xml:"command"`
}

type xmlPtzGuardBody struct {
	XMLName  xml.Name    `xml:"body"`
	PtzGuard xmlPtzGuard `xml:"PtzGuard"`
}

type xmlPtzGuard struct {
	Version    string `xml:"version,attr,omitempty"`
	ChannelID  uint8  `xml:"channelId"`
	Benable    int    `xml:"benable"`
	Command    string `xml:"command"`
	Timeout    int    `xml:"timeout"`
	NeedSetPos int    `xml:"needSetPos"`
	ImageName  string `xml:"imageName"`
}

type xmlPtz3DLocationBody struct {
	XMLName       xml.Name         `xml:"body"`
	Ptz3DLocation xmlPtz3DLocation `xml:"Ptz3DLocation"`
}

type xmlPtz3DLocation struct {
	Version   string `xml:"version,attr,omitempty"`
	ChannelID uint8  `xml:"channelId"`
	PosX      int    `xml:"posX"`
	PosY      int    `xml:"posY"`
	PosWidth  int    `xml:"posWidth"`
	PosHeight int    `xml:"posHeight"`
	Speed     int    `xml:"speed"`
	Width     int    `xml:"width"`
	Height    int    `xml:"height"`
}

type xmlRebootBody struct {
	XMLName xml.Name  `xml:"body"`
	Reboot  xmlReboot `xml:"Reboot"`
}

type xmlReboot struct {
	Channel uint8 `xml:"channel"`
}

// BatteryInfo represents the battery status and metrics of a camera.
type BatteryInfo struct {
	ChannelID      int    `xml:"channelId"`
	ChargeStatus   string `xml:"chargeStatus"`
	AdapterStatus  string `xml:"adapterStatus"`
	Voltage        int    `xml:"voltage"`
	Current        int    `xml:"current"`
	Temperature    int    `xml:"temperature"`
	BatteryPercent int    `xml:"batteryPercent"`
	LowPower       int    `xml:"lowPower"`
	BatteryVersion int    `xml:"batteryVersion"`
}

// BatteryMessage is the XML payload for battery information.
type BatteryMessage struct {
	BatteryInfo *BatteryInfo `xml:"BatteryInfo"`
}

type xmlSnapBody struct {
	XMLName xml.Name `xml:"body"`
	Snap    xmlSnap  `xml:"Snap"`
}

// xmlSnap doubles as the cmd 109 request (LogicChannel/FullFrame/StreamType
// set) and its reply (FileName/PictureSize set).
type xmlSnap struct {
	Version      string  `xml:"version,attr,omitempty"`
	ChannelID    uint8   `xml:"channelId"`
	LogicChannel *uint8  `xml:"logicChannel,omitempty"`
	Time         uint32  `xml:"time"`
	FullFrame    *uint32 `xml:"fullFrame,omitempty"`
	StreamType   string  `xml:"streamType,omitempty"`
	FileName     string  `xml:"fileName,omitempty"`
	PictureSize  uint32  `xml:"pictureSize,omitempty"`
}
