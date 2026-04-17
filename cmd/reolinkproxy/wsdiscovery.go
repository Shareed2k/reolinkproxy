package main

import (
	"encoding/xml"
	"fmt"
	"log"
	"net"
	"strings"

	"github.com/google/uuid"
)

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

	response := s.buildProbeMatch(env.Header.MessageID)
	_, err := conn.WriteToUDP([]byte(response), src)
	if err != nil {
		log.Printf("ws-discovery: write failed: %v", err)
	}
}

func (s *wsDiscoveryServer) buildProbeMatch(relatesTo string) string {
	messageID := "urn:uuid:" + uuid.New().String()

	// Format scopes
	model := strings.ReplaceAll(strings.TrimSpace(s.cfg.Model), " ", "_")
	name := strings.ReplaceAll(strings.TrimSpace(s.cfg.DeviceName), " ", "_")
	scopes := fmt.Sprintf("onvif://www.onvif.org/type/video_encoder onvif://www.onvif.org/hardware/%s onvif://www.onvif.org/name/%s onvif://www.onvif.org/Profile/Streaming", model, name)

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
    <wsa:To wsa:mustUnderstand="true">http://schemas.xmlsoap.org/ws/2004/08/addressing/role/anonymous</wsa:To>
    <wsa:Action wsa:mustUnderstand="true">http://schemas.xmlsoap.org/ws/2005/04/discovery/ProbeMatches</wsa:Action>
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
</env:Envelope>`, messageID, xmlEscape(relatesTo), uuid.New().String(), xmlEscape(scopes), xmlEscape(xaddr))
}
