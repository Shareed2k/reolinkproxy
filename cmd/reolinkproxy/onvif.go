package main

import (
	"bytes"
	"crypto/sha1"
	"encoding/base64"
	"encoding/xml"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"strings"
	"time"
)

type onvifConfig struct {
	Address         string
	DevicePath      string
	MediaPath       string
	AdvertiseHost   string
	RTSPAddress     string
	RTSPPath        string
	DeviceName      string
	Manufacturer    string
	Model           string
	FirmwareVersion string
	SerialNumber    string
	HardwareID      string
	ProfileToken    string
	Username        string
	Password        string
}

type onvifServer struct {
	cfg  onvifConfig
	meta *streamMetadata
}

func newONVIFHandler(cfg onvifConfig, meta *streamMetadata) http.Handler {
	server := &onvifServer{cfg: cfg, meta: meta}
	mux := http.NewServeMux()
	mux.HandleFunc(cfg.DevicePath, server.handleDevice)
	mux.HandleFunc(cfg.MediaPath, server.handleMedia)
	return mux
}

func (s *onvifServer) authenticate(body string) bool {
	if s.cfg.Username == "" && s.cfg.Password == "" {
		return true
	}

	type Security struct {
		Username string `xml:"UsernameToken>Username"`
		Password string `xml:"UsernameToken>Password"`
		Nonce    string `xml:"UsernameToken>Nonce"`
		Created  string `xml:"UsernameToken>Created"`
	}
	type Envelope struct {
		Security Security `xml:"Header>Security"`
	}

	var env Envelope
	if err := xml.Unmarshal([]byte(body), &env); err != nil {
		log.Printf("onvif auth: xml unmarshal error: %v", err)
		return false
	}

	if env.Security.Username != s.cfg.Username {
		log.Printf("onvif auth: expected username %q, got %q", s.cfg.Username, env.Security.Username)
		return false
	}

	nonce, err := base64.StdEncoding.DecodeString(env.Security.Nonce)
	if err != nil {
		log.Printf("onvif auth: failed to decode nonce: %v", err)
		return false
	}

	h := sha1.New()
	h.Write(nonce)
	h.Write([]byte(env.Security.Created))
	h.Write([]byte(s.cfg.Password))
	expected := base64.StdEncoding.EncodeToString(h.Sum(nil))

	if expected != env.Security.Password {
		log.Printf("onvif auth: digest mismatch. Expected: %s, Got: %s", expected, env.Security.Password)
		return false
	}

	return true
}

func (s *onvifServer) handleDevice(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	body, err := io.ReadAll(r.Body)
	if err != nil {
		writeSOAPFault(w, http.StatusBadRequest, "ter:InvalidArgVal", "failed to read request body")
		return
	}

	action := soapAction(r, string(body), []string{
		"GetCapabilities",
		"GetDeviceInformation",
		"GetScopes",
		"GetServices",
		"GetSystemDateAndTime",
	})

	if action != "GetSystemDateAndTime" && !s.authenticate(string(body)) {
		writeSOAPFault(w, http.StatusUnauthorized, "ter:NotAuthorized", "The action requires authorization")
		return
	}

	switch action {
	case "GetCapabilities":
		writeSOAPResponse(w, s.deviceCapabilitiesResponse(r))
	case "GetDeviceInformation":
		writeSOAPResponse(w, s.deviceInformationResponse())
	case "GetScopes":
		writeSOAPResponse(w, s.deviceScopesResponse())
	case "GetServices":
		writeSOAPResponse(w, s.deviceServicesResponse(r))
	case "GetSystemDateAndTime":
		writeSOAPResponse(w, s.deviceSystemDateAndTimeResponse())
	default:
		writeSOAPFault(w, http.StatusBadRequest, "ter:ActionNotSupported", "device action not supported")
	}
}

func (s *onvifServer) handleMedia(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	body, err := io.ReadAll(r.Body)
	if err != nil {
		writeSOAPFault(w, http.StatusBadRequest, "ter:InvalidArgVal", "failed to read request body")
		return
	}

	if !s.authenticate(string(body)) {
		writeSOAPFault(w, http.StatusUnauthorized, "ter:NotAuthorized", "The action requires authorization")
		return
	}

	switch action := soapAction(r, string(body), []string{
		"GetAudioEncoderConfigurations",
		"GetAudioSources",
		"GetProfile",
		"GetProfiles",
		"GetServiceCapabilities",
		"GetStreamUri",
		"GetVideoEncoderConfigurations",
		"GetVideoSources",
	}); action {
	case "GetProfiles":
		writeSOAPResponse(w, s.mediaProfilesResponse())
	case "GetProfile":
		writeSOAPResponse(w, s.mediaProfileResponse())
	case "GetStreamUri":
		writeSOAPResponse(w, s.mediaStreamURIResponse(r))
	case "GetServiceCapabilities":
		writeSOAPResponse(w, `<trt:GetServiceCapabilitiesResponse><trt:Capabilities SnapshotUri="false" Rotation="false" VideoSourceMode="false" OSD="false" TemporaryOSDText="false" EXICompression="false"/></trt:GetServiceCapabilitiesResponse>`)
	case "GetVideoSources":
		writeSOAPResponse(w, s.mediaVideoSourcesResponse())
	case "GetVideoEncoderConfigurations":
		writeSOAPResponse(w, s.mediaVideoEncoderConfigurationsResponse())
	case "GetAudioSources":
		writeSOAPResponse(w, s.mediaAudioSourcesResponse())
	case "GetAudioEncoderConfigurations":
		writeSOAPResponse(w, s.mediaAudioEncoderConfigurationsResponse())
	default:
		writeSOAPFault(w, http.StatusBadRequest, "ter:ActionNotSupported", "media action not supported")
	}
}

func (s *onvifServer) deviceInformationResponse() string {
	return fmt.Sprintf(
		`<tds:GetDeviceInformationResponse><tds:Manufacturer>%s</tds:Manufacturer><tds:Model>%s</tds:Model><tds:FirmwareVersion>%s</tds:FirmwareVersion><tds:SerialNumber>%s</tds:SerialNumber><tds:HardwareId>%s</tds:HardwareId></tds:GetDeviceInformationResponse>`,
		xmlEscape(s.cfg.Manufacturer),
		xmlEscape(s.cfg.Model),
		xmlEscape(s.cfg.FirmwareVersion),
		xmlEscape(s.cfg.SerialNumber),
		xmlEscape(s.cfg.HardwareID),
	)
}

func (s *onvifServer) deviceServicesResponse(r *http.Request) string {
	deviceXAddr := xmlEscape(s.deviceServiceURL(r))
	mediaXAddr := xmlEscape(s.mediaServiceURL(r))

	return fmt.Sprintf(
		`<tds:GetServicesResponse>`+
			`<tds:Service><tds:Namespace>http://www.onvif.org/ver10/device/wsdl</tds:Namespace><tds:XAddr>%s</tds:XAddr><tds:Version><tt:Major>1</tt:Major><tt:Minor>0</tt:Minor></tds:Version></tds:Service>`+
			`<tds:Service><tds:Namespace>http://www.onvif.org/ver10/media/wsdl</tds:Namespace><tds:XAddr>%s</tds:XAddr><tds:Version><tt:Major>1</tt:Major><tt:Minor>0</tt:Minor></tds:Version></tds:Service>`+
			`</tds:GetServicesResponse>`,
		deviceXAddr,
		mediaXAddr,
	)
}

func (s *onvifServer) deviceCapabilitiesResponse(r *http.Request) string {
	deviceXAddr := xmlEscape(s.deviceServiceURL(r))
	mediaXAddr := xmlEscape(s.mediaServiceURL(r))

	return fmt.Sprintf(
		`<tds:GetCapabilitiesResponse><tds:Capabilities>`+
			`<tt:Device>`+
			`<tt:XAddr>%s</tt:XAddr>`+
			`<tt:Network><tt:IPFilter>false</tt:IPFilter><tt:ZeroConfiguration>false</tt:ZeroConfiguration><tt:IPVersion6>false</tt:IPVersion6><tt:DynDNS>false</tt:DynDNS></tt:Network>`+
			`<tt:System><tt:DiscoveryResolve>false</tt:DiscoveryResolve><tt:DiscoveryBye>false</tt:DiscoveryBye><tt:RemoteDiscovery>false</tt:RemoteDiscovery><tt:SystemBackup>false</tt:SystemBackup><tt:SystemLogging>false</tt:SystemLogging><tt:FirmwareUpgrade>false</tt:FirmwareUpgrade></tt:System>`+
			`<tt:IO><tt:InputConnectors>0</tt:InputConnectors><tt:RelayOutputs>0</tt:RelayOutputs></tt:IO>`+
			`<tt:Security><tt:TLS1.1>false</tt:TLS1.1><tt:TLS1.2>false</tt:TLS1.2><tt:OnboardKeyGeneration>false</tt:OnboardKeyGeneration><tt:AccessPolicyConfig>false</tt:AccessPolicyConfig><tt:X.509Token>false</tt:X.509Token><tt:SAMLToken>false</tt:SAMLToken><tt:KerberosToken>false</tt:KerberosToken><tt:RELToken>false</tt:RELToken></tt:Security>`+
			`</tt:Device>`+
			`<tt:Media>`+
			`<tt:XAddr>%s</tt:XAddr>`+
			`<tt:StreamingCapabilities><tt:RTPMulticast>false</tt:RTPMulticast><tt:RTP_TCP>true</tt:RTP_TCP><tt:RTP_RTSP_TCP>true</tt:RTP_RTSP_TCP></tt:StreamingCapabilities>`+
			`<tt:ProfileCapabilities><tt:MaximumNumberOfProfiles>1</tt:MaximumNumberOfProfiles></tt:ProfileCapabilities>`+
			`</tt:Media>`+
			`</tds:Capabilities></tds:GetCapabilitiesResponse>`,
		deviceXAddr,
		mediaXAddr,
	)
}

func (s *onvifServer) deviceScopesResponse() string {
	model := strings.ReplaceAll(strings.TrimSpace(s.cfg.Model), " ", "_")
	name := strings.ReplaceAll(strings.TrimSpace(s.cfg.DeviceName), " ", "_")

	return fmt.Sprintf(
		`<tds:GetScopesResponse>`+
			`<tds:Scopes><tt:ScopeDef>Fixed</tt:ScopeDef><tt:ScopeItem>onvif://www.onvif.org/Profile/Streaming</tt:ScopeItem></tds:Scopes>`+
			`<tds:Scopes><tt:ScopeDef>Fixed</tt:ScopeDef><tt:ScopeItem>onvif://www.onvif.org/type/video_encoder</tt:ScopeItem></tds:Scopes>`+
			`<tds:Scopes><tt:ScopeDef>Fixed</tt:ScopeDef><tt:ScopeItem>onvif://www.onvif.org/hardware/%s</tt:ScopeItem></tds:Scopes>`+
			`<tds:Scopes><tt:ScopeDef>Fixed</tt:ScopeDef><tt:ScopeItem>onvif://www.onvif.org/name/%s</tt:ScopeItem></tds:Scopes>`+
			`</tds:GetScopesResponse>`,
		xmlEscape(model),
		xmlEscape(name),
	)
}

func (s *onvifServer) deviceSystemDateAndTimeResponse() string {
	now := time.Now().UTC()
	return fmt.Sprintf(
		`<tds:GetSystemDateAndTimeResponse><tds:SystemDateAndTime><tt:DateTimeType>NTP</tt:DateTimeType><tt:DaylightSavings>false</tt:DaylightSavings><tt:TimeZone><tt:TZ>UTC</tt:TZ></tt:TimeZone><tt:UTCDateTime><tt:Time><tt:Hour>%d</tt:Hour><tt:Minute>%d</tt:Minute><tt:Second>%d</tt:Second></tt:Time><tt:Date><tt:Year>%d</tt:Year><tt:Month>%d</tt:Month><tt:Day>%d</tt:Day></tt:Date></tt:UTCDateTime><tt:LocalDateTime><tt:Time><tt:Hour>%d</tt:Hour><tt:Minute>%d</tt:Minute><tt:Second>%d</tt:Second></tt:Time><tt:Date><tt:Year>%d</tt:Year><tt:Month>%d</tt:Month><tt:Day>%d</tt:Day></tt:Date></tt:LocalDateTime></tds:SystemDateAndTime></tds:GetSystemDateAndTimeResponse>`,
		now.Hour(), now.Minute(), now.Second(),
		now.Year(), int(now.Month()), now.Day(),
		now.Hour(), now.Minute(), now.Second(),
		now.Year(), int(now.Month()), now.Day(),
	)
}

func (s *onvifServer) mediaProfilesResponse() string {
	return `<trt:GetProfilesResponse>` + s.profileXML("trt:Profiles") + `</trt:GetProfilesResponse>`
}

func (s *onvifServer) mediaProfileResponse() string {
	return `<trt:GetProfileResponse>` + s.profileXML("trt:Profile") + `</trt:GetProfileResponse>`
}

func (s *onvifServer) mediaStreamURIResponse(r *http.Request) string {
	return fmt.Sprintf(
		`<trt:GetStreamUriResponse><trt:MediaUri><tt:Uri>%s</tt:Uri><tt:InvalidAfterConnect>false</tt:InvalidAfterConnect><tt:InvalidAfterReboot>false</tt:InvalidAfterReboot><tt:Timeout>PT0S</tt:Timeout></trt:MediaUri></trt:GetStreamUriResponse>`,
		xmlEscape(s.rtspStreamURL(r)),
	)
}

func (s *onvifServer) mediaVideoSourcesResponse() string {
	snap := s.meta.snapshot().normalized()
	return fmt.Sprintf(
		`<trt:GetVideoSourcesResponse><trt:VideoSources token="VideoSource_0"><tt:Framerate>%d</tt:Framerate><tt:Resolution><tt:Width>%d</tt:Width><tt:Height>%d</tt:Height></tt:Resolution></trt:VideoSources></trt:GetVideoSourcesResponse>`,
		snap.FPS,
		snap.Width,
		snap.Height,
	)
}

func (s *onvifServer) mediaVideoEncoderConfigurationsResponse() string {
	return `<trt:GetVideoEncoderConfigurationsResponse>` + s.videoEncoderConfigXML("trt:Configurations") + `</trt:GetVideoEncoderConfigurationsResponse>`
}

func (s *onvifServer) mediaAudioSourcesResponse() string {
	snap := s.meta.snapshot().normalized()
	if snap.AudioCodec == "" {
		return `<trt:GetAudioSourcesResponse/>`
	}

	return fmt.Sprintf(
		`<trt:GetAudioSourcesResponse><trt:AudioSources token="AudioSource_0"><tt:Channels>%d</tt:Channels></trt:AudioSources></trt:GetAudioSourcesResponse>`,
		snap.AudioChannels,
	)
}

func (s *onvifServer) mediaAudioEncoderConfigurationsResponse() string {
	snap := s.meta.snapshot().normalized()
	if snap.AudioCodec == "" {
		return `<trt:GetAudioEncoderConfigurationsResponse/>`
	}

	return `<trt:GetAudioEncoderConfigurationsResponse>` + s.audioEncoderConfigXML("trt:Configurations", snap) + `</trt:GetAudioEncoderConfigurationsResponse>`
}

func (s *onvifServer) profileXML(tag string) string {
	snap := s.meta.snapshot().normalized()
	videoSourceToken := "VideoSource_0"
	token := xmlEscape(s.cfg.ProfileToken)
	name := xmlEscape(s.cfg.DeviceName)

	var b strings.Builder
	fmt.Fprintf(&b, `<%s token="%s" fixed="true">`, tag, token)
	fmt.Fprintf(&b, `<tt:Name>%s</tt:Name>`, name)
	fmt.Fprintf(&b, `<tt:VideoSourceConfiguration token="VideoSourceConfig_%s"><tt:Name>VideoSource</tt:Name><tt:UseCount>1</tt:UseCount><tt:SourceToken>%s</tt:SourceToken><tt:Bounds x="0" y="0" width="%d" height="%d"/></tt:VideoSourceConfiguration>`, token, videoSourceToken, snap.Width, snap.Height)
	b.WriteString(s.videoEncoderConfigXML("tt:VideoEncoderConfiguration"))
	if snap.AudioCodec != "" {
		b.WriteString(s.audioEncoderConfigXML("tt:AudioEncoderConfiguration", snap))
	}
	fmt.Fprintf(&b, `</%s>`, tag)
	return b.String()
}

func (s *onvifServer) videoEncoderConfigXML(tag string) string {
	snap := s.meta.snapshot().normalized()
	token := xmlEscape(s.cfg.ProfileToken)

	return fmt.Sprintf(
		`<%s token="VideoEncoder_%s"><tt:Name>H265</tt:Name><tt:UseCount>1</tt:UseCount><tt:Encoding>H265</tt:Encoding><tt:Resolution><tt:Width>%d</tt:Width><tt:Height>%d</tt:Height></tt:Resolution><tt:Quality>5</tt:Quality><tt:RateControl><tt:FrameRateLimit>%d</tt:FrameRateLimit><tt:EncodingInterval>1</tt:EncodingInterval><tt:BitrateLimit>4096</tt:BitrateLimit></tt:RateControl><tt:GovLength>1</tt:GovLength><tt:H265><tt:Profile>Main</tt:Profile></tt:H265><tt:SessionTimeout>PT60S</tt:SessionTimeout></%s>`,
		tag,
		token,
		snap.Width,
		snap.Height,
		snap.FPS,
		tag,
	)
}

func (s *onvifServer) audioEncoderConfigXML(tag string, snap streamMetadataSnapshot) string {
	token := xmlEscape(s.cfg.ProfileToken)
	if snap.AudioSampleRate == 0 {
		snap.AudioSampleRate = 16000
	}
	if snap.AudioChannels == 0 {
		snap.AudioChannels = 1
	}

	encoding := snap.AudioCodec
	if encoding == "" {
		encoding = "AAC"
	}
	// G711 must be G711 according to ONVIF
	if encoding == "PCMA" || encoding == "PCMU" {
		encoding = "G711"
	}

	return fmt.Sprintf(
		`<%s token="AudioEncoder_%s"><tt:Name>%s</tt:Name><tt:UseCount>1</tt:UseCount><tt:Encoding>%s</tt:Encoding><tt:Bitrate>128</tt:Bitrate><tt:SampleRate>%d</tt:SampleRate><tt:Multicast><tt:Address><tt:Type>IPv4</tt:Type><tt:IPv4Address>0.0.0.0</tt:IPv4Address></tt:Address><tt:Port>0</tt:Port><tt:TTL>0</tt:TTL><tt:AutoStart>false</tt:AutoStart></tt:Multicast><tt:SessionTimeout>PT60S</tt:SessionTimeout></%s>`,
		tag,
		token,
		snap.AudioCodec,
		encoding,
		snap.AudioSampleRate,
		tag,
	)
}

func (s *onvifServer) deviceServiceURL(r *http.Request) string {
	return buildURL("http", s.authorityForRequest(r, s.cfg.Address), s.cfg.DevicePath)
}

func (s *onvifServer) mediaServiceURL(r *http.Request) string {
	return buildURL("http", s.authorityForRequest(r, s.cfg.Address), s.cfg.MediaPath)
}

func (s *onvifServer) rtspStreamURL(r *http.Request) string {
	return buildURL("rtsp", s.authorityForRequest(r, s.cfg.RTSPAddress), s.cfg.RTSPPath)
}

func (s *onvifServer) authorityForRequest(r *http.Request, listenAddr string) string {
	if s.cfg.AdvertiseHost != "" {
		return advertisedAuthority(listenAddr, s.cfg.AdvertiseHost)
	}

	if r != nil && r.Host != "" {
		host := r.Host
		if parsedHost, _, err := net.SplitHostPort(r.Host); err == nil {
			host = parsedHost
		}
		return advertisedAuthority(listenAddr, host)
	}

	return advertisedAuthority(listenAddr, "")
}

func soapAction(r *http.Request, body string, known []string) string {
	if raw := strings.Trim(strings.TrimSpace(r.Header.Get("SOAPAction")), `"`); raw != "" {
		if idx := strings.LastIndexAny(raw, "/#"); idx >= 0 && idx < len(raw)-1 {
			return raw[idx+1:]
		}
		return raw
	}

	for _, action := range known {
		if hasSOAPActionBody(body, action) {
			return action
		}
	}
	return ""
}

func hasSOAPActionBody(body string, action string) bool {
	patterns := []string{
		":" + action + ">",
		":" + action + " ",
		":" + action + "/",
		"<" + action + ">",
		"<" + action + " ",
		"<" + action + "/",
	}

	for _, pattern := range patterns {
		if strings.Contains(body, pattern) {
			return true
		}
	}

	return false
}

func writeSOAPResponse(w http.ResponseWriter, inner string) {
	w.Header().Set("Content-Type", `application/soap+xml; charset=utf-8`)
	w.WriteHeader(http.StatusOK)
	_, _ = io.WriteString(w, soapEnvelope(inner))
}

func writeSOAPFault(w http.ResponseWriter, statusCode int, subcode string, reason string) {
	w.Header().Set("Content-Type", `application/soap+xml; charset=utf-8`)
	w.WriteHeader(statusCode)
	_, _ = io.WriteString(w, soapEnvelope(
		fmt.Sprintf(
			`<soap:Fault><soap:Code><soap:Value>soap:Sender</soap:Value><soap:Subcode><soap:Value>%s</soap:Value></soap:Subcode></soap:Code><soap:Reason><soap:Text xml:lang="en">%s</soap:Text></soap:Reason></soap:Fault>`,
			xmlEscape(subcode),
			xmlEscape(reason),
		),
	))
}

func soapEnvelope(inner string) string {
	return `<?xml version="1.0" encoding="UTF-8"?>` +
		`<soap:Envelope xmlns:soap="http://www.w3.org/2003/05/soap-envelope" xmlns:tds="http://www.onvif.org/ver10/device/wsdl" xmlns:trt="http://www.onvif.org/ver10/media/wsdl" xmlns:tt="http://www.onvif.org/ver10/schema" xmlns:ter="http://www.onvif.org/ver10/error">` +
		`<soap:Body>` + inner + `</soap:Body></soap:Envelope>`
}

func xmlEscape(v string) string {
	var buf bytes.Buffer
	_ = xml.EscapeText(&buf, []byte(v))
	return buf.String()
}
