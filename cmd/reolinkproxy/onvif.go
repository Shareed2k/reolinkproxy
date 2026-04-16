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
	cfg   onvifConfig
	metas []*streamMetadata
}

func newONVIFHandler(cfg onvifConfig, metas []*streamMetadata) http.Handler {
	server := &onvifServer{cfg: cfg, metas: metas}
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

	if expected != env.Security.Password && env.Security.Password != s.cfg.Password {
		log.Printf("onvif auth: digest mismatch. Expected: %s, Got: %s (nonce base64: %s, created: %s, username: %s)", expected, env.Security.Password, env.Security.Nonce, env.Security.Created, env.Security.Username)
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
		"GetNetworkInterfaces",
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
	case "GetNetworkInterfaces":
		writeSOAPResponse(w, s.deviceNetworkInterfacesResponse())
	default:
		log.Printf("onvif device: unsupported action %q (body: %s)", action, body)
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
		writeSOAPResponse(w, s.mediaProfileResponse(string(body)))
	case "GetStreamUri":
		writeSOAPResponse(w, s.mediaStreamURIResponse(r, string(body)))
	case "GetServiceCapabilities":
		writeSOAPResponse(w, `<trt:GetServiceCapabilitiesResponse><trt:Capabilities SnapshotUri="false" Rotation="false" VideoSourceMode="false" OSD="false" TemporaryOSDText="false" EXICompression="false"/></trt:GetServiceCapabilitiesResponse>`)
	case "GetVideoSources":
		writeSOAPResponse(w, s.mediaVideoSourcesResponse(string(body)))
	case "GetVideoEncoderConfigurations":
		writeSOAPResponse(w, s.mediaVideoEncoderConfigurationsResponse(string(body)))
	case "GetAudioSources":
		writeSOAPResponse(w, s.mediaAudioSourcesResponse(string(body)))
	case "GetAudioEncoderConfigurations":
		writeSOAPResponse(w, s.mediaAudioEncoderConfigurationsResponse(string(body)))
	default:
		log.Printf("onvif media: unsupported action %q (body: %s)", action, body)
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
			`<tt:ProfileCapabilities><tt:MaximumNumberOfProfiles>%d</tt:MaximumNumberOfProfiles></tt:ProfileCapabilities>`+
			`</tt:Media>`+
			`</tds:Capabilities></tds:GetCapabilitiesResponse>`,
		deviceXAddr,
		mediaXAddr,
		len(s.metas),
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

func (s *onvifServer) deviceNetworkInterfacesResponse() string {
	host := "127.0.0.1"
	if s.cfg.AdvertiseHost != "" && s.cfg.AdvertiseHost != "0.0.0.0" && s.cfg.AdvertiseHost != "::" {
		host = s.cfg.AdvertiseHost
	} else if outbound := getOutboundIP(); outbound != "" {
		host = outbound
	} else if s.cfg.Address != "" {
		if parsedHost, _, err := net.SplitHostPort(s.cfg.Address); err == nil && parsedHost != "" && parsedHost != "0.0.0.0" && parsedHost != "::" {
			host = parsedHost
		}
	}

	return fmt.Sprintf(`<tds:GetNetworkInterfacesResponse><tds:NetworkInterfaces token="eth0"><tt:Enabled>true</tt:Enabled><tt:Info><tt:Name>eth0</tt:Name><tt:HwAddress>00:00:00:00:00:00</tt:HwAddress><tt:MTU>1500</tt:MTU></tt:Info><tt:IPv4><tt:Enabled>true</tt:Enabled><tt:Config><tt:Manual><tt:Address>%s</tt:Address><tt:PrefixLength>24</tt:PrefixLength></tt:Manual><tt:DHCP>false</tt:DHCP></tt:Config></tt:IPv4></tds:NetworkInterfaces></tds:GetNetworkInterfacesResponse>`, xmlEscape(host))
}

func (s *onvifServer) getMeta(token string) *streamMetadata {
	for _, m := range s.metas {
		if m.name == token {
			return m
		}
	}
	if len(s.metas) > 0 {
		return s.metas[0]
	}
	return nil
}

func (s *onvifServer) mediaProfilesResponse() string {
	var b strings.Builder
	b.WriteString(`<trt:GetProfilesResponse>`)
	for _, m := range s.metas {
		b.WriteString(s.profileXML("trt:Profiles", m.name, m))
	}
	b.WriteString(`</trt:GetProfilesResponse>`)
	return b.String()
}

func (s *onvifServer) mediaProfileResponse(body string) string {
	token := s.extractToken(body, "ProfileToken")
	m := s.getMeta(token)
	return `<trt:GetProfileResponse>` + s.profileXML("trt:Profile", token, m) + `</trt:GetProfileResponse>`
}

func (s *onvifServer) extractToken(body, element string) string {
	// Namespace-agnostic XML element extraction
	idx := strings.Index(body, ":"+element+">")
	if idx == -1 {
		idx = strings.Index(body, "<"+element+">")
	} else {
		// adjust idx to point exactly before the element name for parity
		idx = idx + 1
	}

	if idx != -1 {
		closeBracketIdx := idx + len(element)
		if closeBracketIdx < len(body) && body[closeBracketIdx] == '>' {
			valStart := closeBracketIdx + 1
			valEnd := strings.Index(body[valStart:], "<")
			if valEnd != -1 {
				return body[valStart : valStart+valEnd]
			}
		}
	}

	// default fallback
	if len(s.metas) > 0 {
		return s.metas[0].name
	}
	return "main"
}

func (s *onvifServer) mediaStreamURIResponse(r *http.Request, body string) string {
	token := s.extractToken(body, "ProfileToken")
	m := s.getMeta(token)
	path := s.cfg.RTSPPath
	if m != nil && m.path != "" {
		path = m.path
	}

	return fmt.Sprintf(
		`<trt:GetStreamUriResponse><trt:MediaUri><tt:Uri>%s</tt:Uri><tt:InvalidAfterConnect>false</tt:InvalidAfterConnect><tt:InvalidAfterReboot>false</tt:InvalidAfterReboot><tt:Timeout>PT0S</tt:Timeout></trt:MediaUri></trt:GetStreamUriResponse>`,
		xmlEscape(buildURL("rtsp", s.authorityForRequest(r, s.cfg.RTSPAddress), path)),
	)
}

func (s *onvifServer) mediaVideoSourcesResponse(body string) string {
	token := s.extractToken(body, "ProfileToken")
	m := s.getMeta(token)
	var snap streamMetadataSnapshot
	if m != nil {
		snap = m.snapshot().normalized()
	} else {
		snap = streamMetadataSnapshot{}.normalized()
	}

	return fmt.Sprintf(
		`<trt:GetVideoSourcesResponse><trt:VideoSources token="VideoSource_%s"><tt:Framerate>%d</tt:Framerate><tt:Resolution><tt:Width>%d</tt:Width><tt:Height>%d</tt:Height></tt:Resolution></trt:VideoSources></trt:GetVideoSourcesResponse>`,
		token,
		snap.FPS,
		snap.Width,
		snap.Height,
	)
}

func (s *onvifServer) mediaVideoEncoderConfigurationsResponse(body string) string {
	token := s.extractToken(body, "ProfileToken")
	m := s.getMeta(token)
	var snap streamMetadataSnapshot
	if m != nil {
		snap = m.snapshot().normalized()
	} else {
		snap = streamMetadataSnapshot{}.normalized()
	}
	return `<trt:GetVideoEncoderConfigurationsResponse>` + s.videoEncoderConfigXML("trt:Configurations", token, snap) + `</trt:GetVideoEncoderConfigurationsResponse>`
}

func (s *onvifServer) mediaAudioSourcesResponse(body string) string {
	token := s.extractToken(body, "ProfileToken")
	m := s.getMeta(token)
	var snap streamMetadataSnapshot
	if m != nil {
		snap = m.snapshot().normalized()
	} else {
		snap = streamMetadataSnapshot{}.normalized()
	}

	if snap.AudioCodec == "" {
		return `<trt:GetAudioSourcesResponse/>`
	}

	return fmt.Sprintf(
		`<trt:GetAudioSourcesResponse><trt:AudioSources token="AudioSource_%s"><tt:Channels>%d</tt:Channels></trt:AudioSources></trt:GetAudioSourcesResponse>`,
		token,
		snap.AudioChannels,
	)
}

func (s *onvifServer) mediaAudioEncoderConfigurationsResponse(body string) string {
	token := s.extractToken(body, "ProfileToken")
	m := s.getMeta(token)
	var snap streamMetadataSnapshot
	if m != nil {
		snap = m.snapshot().normalized()
	} else {
		snap = streamMetadataSnapshot{}.normalized()
	}

	if snap.AudioCodec == "" {
		return `<trt:GetAudioEncoderConfigurationsResponse/>`
	}

	return `<trt:GetAudioEncoderConfigurationsResponse>` + s.audioEncoderConfigXML("trt:Configurations", token, snap) + `</trt:GetAudioEncoderConfigurationsResponse>`
}

func (s *onvifServer) profileXML(tag string, token string, m *streamMetadata) string {
	var snap streamMetadataSnapshot
	if m != nil {
		snap = m.snapshot().normalized()
	} else {
		snap = streamMetadataSnapshot{}.normalized()
	}

	videoSourceToken := "VideoSource_" + token
	profileToken := xmlEscape(token)
	name := xmlEscape(s.cfg.DeviceName + "_" + token)

	var b strings.Builder
	fmt.Fprintf(&b, `<%s token="%s" fixed="true">`, tag, profileToken)
	fmt.Fprintf(&b, `<tt:Name>%s</tt:Name>`, name)
	fmt.Fprintf(&b, `<tt:VideoSourceConfiguration token="VideoSourceConfig_%s"><tt:Name>VideoSource</tt:Name><tt:UseCount>1</tt:UseCount><tt:SourceToken>%s</tt:SourceToken><tt:Bounds x="0" y="0" width="%d" height="%d"/></tt:VideoSourceConfiguration>`, profileToken, videoSourceToken, snap.Width, snap.Height)
	b.WriteString(s.videoEncoderConfigXML("tt:VideoEncoderConfiguration", token, snap))
	if snap.AudioCodec != "" {
		b.WriteString(s.audioEncoderConfigXML("tt:AudioEncoderConfiguration", token, snap))
	}
	fmt.Fprintf(&b, `</%s>`, tag)
	return b.String()
}

func (s *onvifServer) videoEncoderConfigXML(tag string, token string, snap streamMetadataSnapshot) string {
	encoding := snap.VideoCodec
	if encoding == "" {
		encoding = "H265"
	}

	return fmt.Sprintf(
		`<%s token="VideoEncoder_%s"><tt:Name>%s</tt:Name><tt:UseCount>1</tt:UseCount><tt:Encoding>%s</tt:Encoding><tt:Resolution><tt:Width>%d</tt:Width><tt:Height>%d</tt:Height></tt:Resolution><tt:Quality>5</tt:Quality><tt:RateControl><tt:FrameRateLimit>%d</tt:FrameRateLimit><tt:EncodingInterval>1</tt:EncodingInterval><tt:BitrateLimit>4096</tt:BitrateLimit></tt:RateControl><tt:GovLength>1</tt:GovLength><tt:%s><tt:Profile>Main</tt:Profile></tt:%s><tt:SessionTimeout>PT60S</tt:SessionTimeout></%s>`,
		tag,
		xmlEscape(token),
		encoding,
		encoding,
		snap.Width,
		snap.Height,
		snap.FPS,
		encoding,
		encoding,
		tag,
	)
}

func (s *onvifServer) audioEncoderConfigXML(tag string, token string, snap streamMetadataSnapshot) string {
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
		xmlEscape(token),
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

	// Sort known actions by length descending so longer matching strings win
	// e.g. "GetProfiles" matched before "GetProfile"
	for i := 0; i < len(known); i++ {
		for j := i + 1; j < len(known); j++ {
			if len(known[i]) < len(known[j]) {
				known[i], known[j] = known[j], known[i]
			}
		}
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
		action, // fallback just in case namespace is completely omitted or weird
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
