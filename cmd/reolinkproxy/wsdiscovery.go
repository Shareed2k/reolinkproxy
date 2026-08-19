package main

import (
	"encoding/xml"
	"fmt"
	"math/rand/v2"
	"net"
	"strings"
	"time"

	"github.com/google/uuid"
)

// wsDiscoveryMaxDelay is APP_MAX_DELAY from the WS-Discovery spec: responses
// to a multicast Probe must be delayed by a random 0..500ms to avoid response
// storms when many devices answer at once.
const wsDiscoveryMaxDelay = 500 * time.Millisecond

// deviceUUID returns a stable urn:uuid identity for this device, derived from
// its configured identity so WS-Discovery ProbeMatches and
// GetEndpointReference agree with each other and across restarts.
func deviceUUID(cfg onvifConfig) string {
	seed := "reolinkproxy:" + cfg.SerialNumber + "|" + cfg.DeviceName
	return uuid.NewSHA1(uuid.NameSpaceURL, []byte(seed)).String()
}

// onvifScopes is the single source of truth for the scopes advertised by both
// WS-Discovery ProbeMatches and the device service GetScopes.
func onvifScopes(cfg onvifConfig) []string {
	model := strings.ReplaceAll(strings.TrimSpace(cfg.Model), " ", "_")
	name := strings.ReplaceAll(strings.TrimSpace(cfg.DeviceName), " ", "_")
	return []string{
		"onvif://www.onvif.org/type/video_encoder",
		"onvif://www.onvif.org/hardware/" + model,
		"onvif://www.onvif.org/name/" + name,
		"onvif://www.onvif.org/Profile/Streaming",
		"onvif://www.onvif.org/Profile/S",
		"onvif://www.onvif.org/Profile/T",
	}
}

// matchesProbeScopes implements the default WS-Discovery matching rule
// (RFC 3986 prefix match per path segment, here simplified to string prefix):
// every scope requested by the Probe must match one advertised scope.
func matchesProbeScopes(requested string, advertised []string) bool {
	for _, want := range strings.Fields(requested) {
		matched := false
		for _, have := range advertised {
			if strings.HasPrefix(have, want) {
				matched = true
				break
			}
		}
		if !matched {
			return false
		}
	}
	return true
}

type wsDiscoveryServer struct {
	cfg onvifConfig
}

func startWSDiscovery(cfg onvifConfig) {
	addr, err := net.ResolveUDPAddr("udp4", "239.255.255.250:3702")
	if err != nil {
		log.Printf("ws-discovery: resolve addr failed: %v", err)
		return
	}

	conn, err := net.ListenMulticastUDP("udp4", nil, addr)
	if err != nil {
		log.Printf("ws-discovery: listen failed: %v", err)
		return
	}

	s := &wsDiscoveryServer{cfg: cfg}
	go s.serve(conn)
}

func (s *wsDiscoveryServer) serve(conn *net.UDPConn) {
	buf := make([]byte, 8192)
	for {
		n, src, err := conn.ReadFromUDP(buf)
		if err != nil {
			log.Printf("ws-discovery: read failed: %v", err)
			return
		}

		s.handleMessage(conn, src, buf[:n])
	}
}

func (s *wsDiscoveryServer) handleMessage(conn *net.UDPConn, src *net.UDPAddr, msg []byte) {
	type Envelope struct {
		Header struct {
			MessageID string `xml:"MessageID"`
			Action    string `xml:"Action"`
		} `xml:"Header"`
		Body struct {
			Probe struct {
				Types  string `xml:"Types"`
				Scopes string `xml:"Scopes"`
			} `xml:"Probe"`
		} `xml:"Body"`
	}

	var env Envelope
	if err := xml.Unmarshal(msg, &env); err != nil {
		return
	}

	if !strings.Contains(env.Header.Action, "Probe") || env.Header.Action == "http://schemas.xmlsoap.org/ws/2005/04/discovery/ProbeMatches" {
		return
	}

	if env.Body.Probe.Types != "" && !strings.Contains(env.Body.Probe.Types, "NetworkVideoTransmitter") && !strings.Contains(env.Body.Probe.Types, "Device") {
		return
	}

	if !matchesProbeScopes(env.Body.Probe.Scopes, onvifScopes(s.cfg)) {
		return
	}

	response := s.buildProbeMatch(env.Header.MessageID)
	srcCopy := *src
	// Random 0..APP_MAX_DELAY delay per the WS-Discovery spec, off the read
	// loop so other probes are not blocked.
	time.AfterFunc(rand.N(wsDiscoveryMaxDelay), func() {
		if _, err := conn.WriteToUDP([]byte(response), &srcCopy); err != nil {
			log.Printf("ws-discovery: write failed: %v", err)
		}
	})
}

func (s *wsDiscoveryServer) buildProbeMatch(relatesTo string) string {
	messageID := "urn:uuid:" + uuid.New().String()
	scopes := strings.Join(onvifScopes(s.cfg), " ")

	var host string
	if s.cfg.AdvertiseHost != "" && s.cfg.AdvertiseHost != "0.0.0.0" && s.cfg.AdvertiseHost != "::" {
		host = s.cfg.AdvertiseHost
	} else {
		host = getOutboundIP()
	}

	xaddr := buildURL("http", advertisedAuthority(s.cfg.Address, host), s.cfg.DevicePath)

	return fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<env:Envelope xmlns:env="http://www.w3.org/2003/05/soap-envelope" xmlns:wsa="http://schemas.xmlsoap.org/ws/2004/08/addressing" xmlns:d="http://schemas.xmlsoap.org/ws/2005/04/discovery" xmlns:dn="http://www.onvif.org/ver10/network/wsdl">
  <env:Header>
    <wsa:MessageID>%s</wsa:MessageID>
    <wsa:RelatesTo>%s</wsa:RelatesTo>
    <wsa:To env:mustUnderstand="true">http://schemas.xmlsoap.org/ws/2004/08/addressing/role/anonymous</wsa:To>
    <wsa:Action env:mustUnderstand="true">http://schemas.xmlsoap.org/ws/2005/04/discovery/ProbeMatches</wsa:Action>
  </env:Header>
  <env:Body>
    <d:ProbeMatches>
      <d:ProbeMatch>
        <wsa:EndpointReference>
          <wsa:Address>urn:uuid:%s</wsa:Address>
        </wsa:EndpointReference>
        <d:Types>dn:NetworkVideoTransmitter</d:Types>
        <d:Scopes>%s</d:Scopes>
        <d:XAddrs>%s</d:XAddrs>
        <d:MetadataVersion>1</d:MetadataVersion>
      </d:ProbeMatch>
    </d:ProbeMatches>
  </env:Body>
</env:Envelope>`, messageID, xmlEscape(relatesTo), deviceUUID(s.cfg), xmlEscape(scopes), xmlEscape(xaddr))
}
