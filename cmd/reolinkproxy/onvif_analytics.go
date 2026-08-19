package main

import (
	"fmt"
	"io"
	"net/http"
	"strings"
)

// Minimal ONVIF Analytics service (ver20/analytics/wsdl): read-only static
// module descriptions. The actual detections flow through the event service
// as tns1:RuleEngine topics; this service exists so clients that require an
// analytics configuration on the profile accept the device.

func (s *onvifServer) analyticsServiceURL(r *http.Request) string {
	path := s.cfg.AnalyticsPath
	if path == "" {
		path = "/onvif/analytics_service"
	}
	return buildURL("http", s.authorityForRequest(r, s.cfg.Address), path)
}

func analyticsConfigTokens(cameraName string) (configToken string, engineToken string) {
	replacer := strings.NewReplacer(" ", "_", "/", "_", "\\", "_")
	cameraName = replacer.Replace(strings.TrimSpace(cameraName))
	if cameraName == "" {
		cameraName = "camera"
	}
	return "AnalyticsConfig_" + cameraName, "AnalyticsEngine_" + cameraName
}

// analyticsConfigurationXML emits a tt:VideoAnalyticsConfiguration with the
// mandatory (but empty) engine and rule-engine configurations.
func analyticsConfigurationXML(tag string, cameraName string) string {
	configToken, _ := analyticsConfigTokens(cameraName)
	return fmt.Sprintf(
		`<%s token="%s"><tt:Name>%s</tt:Name><tt:UseCount>1</tt:UseCount><tt:AnalyticsEngineConfiguration></tt:AnalyticsEngineConfiguration><tt:RuleEngineConfiguration></tt:RuleEngineConfiguration></%s>`,
		tag, xmlEscape(configToken), xmlEscape(configToken), tag,
	)
}

func (s *onvifServer) handleAnalytics(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	rawBody, err := io.ReadAll(io.LimitReader(r.Body, 1024*1024)) // 1MB max payload
	if err != nil {
		writeSOAPFault(w, http.StatusBadRequest, "ter:InvalidArgVal", "failed to read request body")
		return
	}
	body := string(rawBody)

	if !s.authenticate(body) {
		writeSOAPFault(w, http.StatusUnauthorized, "ter:NotAuthorized", "The action requires authorization")
		return
	}

	switch action := soapAction(r, body, []string{
		"GetServiceCapabilities",
		"GetSupportedAnalyticsModules",
		"GetAnalyticsModules",
		"GetSupportedRules",
		"GetRules",
	}); action {
	case "GetServiceCapabilities":
		writeSOAPResponse(w, `<tan:GetServiceCapabilitiesResponse><tan:Capabilities RuleSupport="true" AnalyticsModuleSupport="true" CellBasedSceneDescriptionSupported="false" RuleOptionsSupported="false" AnalyticsModuleOptionsSupported="false"/></tan:GetServiceCapabilitiesResponse>`)
	case "GetSupportedAnalyticsModules":
		writeSOAPResponse(w, `<tan:GetSupportedAnalyticsModulesResponse><tan:SupportedAnalyticsModules><tt:AnalyticsModuleDescription Name="tt:CellMotionEngine" fixed="true" maxInstances="1"><tt:Parameters></tt:Parameters></tt:AnalyticsModuleDescription></tan:SupportedAnalyticsModules></tan:GetSupportedAnalyticsModulesResponse>`)
	case "GetAnalyticsModules":
		writeSOAPResponse(w, `<tan:GetAnalyticsModulesResponse><tan:AnalyticsModule Name="MotionDetector" Type="tt:CellMotionEngine"><tt:Parameters></tt:Parameters></tan:AnalyticsModule></tan:GetAnalyticsModulesResponse>`)
	case "GetSupportedRules":
		writeSOAPResponse(w, `<tan:GetSupportedRulesResponse><tan:SupportedRules><tt:RuleDescription Name="tt:CellMotionDetector" fixed="true" maxInstances="1"><tt:Parameters></tt:Parameters></tt:RuleDescription></tan:SupportedRules></tan:GetSupportedRulesResponse>`)
	case "GetRules":
		writeSOAPResponse(w, `<tan:GetRulesResponse><tan:Rule Name="MyMotionDetectorRule" Type="tt:CellMotionDetector"><tt:Parameters></tt:Parameters></tan:Rule></tan:GetRulesResponse>`)
	default:
		log.Printf("onvif analytics: unsupported action %q (body: %s)", action, body)
		writeSOAPFault(w, http.StatusBadRequest, "ter:ActionNotSupported", "analytics action not supported")
	}
}
