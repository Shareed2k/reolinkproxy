package baichuan

import (
	"fmt"
	"time"
)

// Default constants and internal message IDs for the Baichuan protocol.
const (
	DefaultPort    = 9000
	DefaultTimeout = 10 * time.Second

	magicHeader = 0x0ABCDEF0

	classLegacy           = 0x6514
	classModern           = 0x6614
	classModernWithOffset = 0x6414
	classModernAlt        = 0x0000

	msgIDPTZControl              = 18
	msgIDPTZControlPreset        = 19
	msgIDLogin                   = 1
	msgIDLogout                  = 2
	msgIDVideo                   = 3
	msgIDMotionRequest           = 31
	msgIDMotion                  = 33
	msgIDGetPorts                = 37
	msgIDPing                    = 93
	msgIDAbilityInfo             = 151
	msgIDUDPKeepAlive            = 234
	msgIDBatteryInfoList         = 252
	msgIDBatteryInfo             = 253
	defaultUIDMTU         uint32 = 1350
)

// EncryptionMode is the negotiated XML encryption mode used by Baichuan.
type EncryptionMode uint8

// Available encryption modes.
const (
	EncryptionNone EncryptionMode = iota
	EncryptionBC
	EncryptionAES
)

// Stream selects the requested camera stream.
type Stream string

// Supported stream types.
const (
	StreamMain   Stream = "mainStream"
	StreamSub    Stream = "subStream"
	StreamExtern Stream = "externStream"
)

// Config contains connection settings for a Baichuan client.
type Config struct {
	Host     string
	Port     int
	UID      string
	Username string
	Password string
	Timeout  time.Duration
}

func (c Config) normalized() Config {
	if c.Port == 0 {
		c.Port = DefaultPort
	}
	if c.Timeout == 0 {
		c.Timeout = DefaultTimeout
	}
	return c
}

// Header is the decoded Baichuan header.
type Header struct {
	MsgID         uint32
	BodyLen       uint32
	ChannelID     uint8
	StreamType    uint8
	MsgNum        uint16
	ResponseCode  uint16
	Class         uint16
	PayloadOffset uint32
}

// HasPayloadOffset reports whether the header carries the 24-byte modern layout.
func (h Header) HasPayloadOffset() bool {
	return hasPayloadOffset(h.Class)
}

// IsModern reports whether the message is modern, not legacy.
func (h Header) IsModern() bool {
	return h.Class != classLegacy
}

func hasPayloadOffset(class uint16) bool {
	return class == classModernWithOffset || class == classModernAlt
}

// Extension is the small XML block that may precede the primary payload.
type Extension struct {
	BinaryData *int `xml:"binaryData"`
	ChannelID  *int `xml:"channelId"`
	EncryptLen *int `xml:"encryptLen"`
}

// Message is a decoded Baichuan message.
type Message struct {
	Header        Header
	Extension     []byte
	Payload       []byte
	XML           string
	Binary        bool
	ExtensionMeta *Extension
}

func (m *Message) success() error {
	if !m.Header.HasPayloadOffset() {
		return nil
	}

	switch m.Header.ResponseCode {
	case 200, 201, 300:
		return nil
	default:
		return &StatusError{
			MsgID: m.Header.MsgID,
			Code:  m.Header.ResponseCode,
		}
	}
}

// StatusError is returned when the camera replies with a non-success status.
type StatusError struct {
	MsgID uint32
	Code  uint16
}

func (e *StatusError) Error() string {
	return fmt.Sprintf("baichuan cmd %d failed with status %d", e.MsgID, e.Code)
}

// MediaPacketKind identifies the parsed bcmedia payload type.
type MediaPacketKind int

// Available media packet kinds.
const (
	MediaPacketInfoV1 MediaPacketKind = iota + 1
	MediaPacketInfoV2
	MediaPacketIFrame
	MediaPacketPFrame
	MediaPacketAAC
	MediaPacketADPCM
)

func (k MediaPacketKind) String() string {
	switch k {
	case MediaPacketInfoV1:
		return "info-v1"
	case MediaPacketInfoV2:
		return "info-v2"
	case MediaPacketIFrame:
		return "iframe"
	case MediaPacketPFrame:
		return "pframe"
	case MediaPacketAAC:
		return "aac"
	case MediaPacketADPCM:
		return "adpcm"
	default:
		return "unknown"
	}
}

// MediaPacket is one parsed bcmedia unit.
type MediaPacket struct {
	Kind               MediaPacketKind
	Codec              string
	Data               []byte
	TimestampMicrosecs uint32
	UnixTime           *time.Time
	Width              uint32
	Height             uint32
	FPS                uint8
}

// MediaReader exposes the parsed bcmedia stream coming from a preview session.
type MediaReader struct {
	Packets  <-chan MediaPacket
	stop     chan struct{}
	stopOnce func()
}

// Close stops the media reader.
func (r *MediaReader) Close() {
	if r.stopOnce != nil {
		r.stopOnce()
	}
}
