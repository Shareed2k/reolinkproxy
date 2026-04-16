package main

import (
	"bytes"
	"crypto/sha1"
	"encoding/base64"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func generateAuthHeader(username, password string) string {
	nonce := "1234567890"
	nonceB64 := base64.StdEncoding.EncodeToString([]byte(nonce))
	created := time.Now().UTC().Format(time.RFC3339)

	h := sha1.New()
	h.Write([]byte(nonce))
	h.Write([]byte(created))
	h.Write([]byte(password))
	digest := base64.StdEncoding.EncodeToString(h.Sum(nil))

	return fmt.Sprintf(`
		<soap:Header>
			<Security xmlns="http://docs.oasis-open.org/wss/2004/01/oasis-200401-wss-wssecurity-secext-1.0.xsd">
				<UsernameToken>
					<Username>%s</Username>
					<Password Type="http://docs.oasis-open.org/wss/2004/01/oasis-200401-wss-username-token-profile-1.0#PasswordDigest">%s</Password>
					<Nonce EncodingType="http://docs.oasis-open.org/wss/2004/01/oasis-200401-wss-soap-message-security-1.0#Base64Binary">%s</Nonce>
					<Created xmlns="http://docs.oasis-open.org/wss/2004/01/oasis-200401-wss-wssecurity-utility-1.0.xsd">%s</Created>
				</UsernameToken>
			</Security>
		</soap:Header>`, username, digest, nonceB64, created)
}

func TestONVIFAUTH(t *testing.T) {
	cfg := onvifConfig{
		Username: "admin",
		Password: "password123",
	}
	server := &onvifServer{cfg: cfg}

	t.Run("Valid Credentials", func(t *testing.T) {
		body := `<soap:Envelope>` + generateAuthHeader("admin", "password123") + `</soap:Envelope>`
		if !server.authenticate(body) {
			t.Errorf("expected authentication to succeed")
		}
	})

	t.Run("Invalid Password", func(t *testing.T) {
		body := `<soap:Envelope>` + generateAuthHeader("admin", "wrongpassword") + `</soap:Envelope>`
		if server.authenticate(body) {
			t.Errorf("expected authentication to fail")
		}
	})

	t.Run("Invalid Username", func(t *testing.T) {
		body := `<soap:Envelope>` + generateAuthHeader("guest", "password123") + `</soap:Envelope>`
		if server.authenticate(body) {
			t.Errorf("expected authentication to fail")
		}
	})

	t.Run("No Auth Header", func(t *testing.T) {
		body := `<soap:Envelope></soap:Envelope>`
		if server.authenticate(body) {
			t.Errorf("expected authentication to fail")
		}
	})
	
	t.Run("No Credentials Configured", func(t *testing.T) {
	    noAuthServer := &onvifServer{cfg: onvifConfig{}}
	    if !noAuthServer.authenticate(`<soap:Envelope></soap:Envelope>`) {
	        t.Errorf("expected authentication to succeed when no credentials are configured")
	    }
	})
}

func TestSOAPAction(t *testing.T) {
	tests := []struct {
		name     string
		header   string
		body     string
		expected string
	}{
		{
			name:     "From Header Quoted",
			header:   `"http://www.onvif.org/ver10/device/wsdl/GetDeviceInformation"`,
			expected: "GetDeviceInformation",
		},
		{
			name:     "From Header Unquoted",
			header:   `http://www.onvif.org/ver10/device/wsdl/GetDeviceInformation`,
			expected: "GetDeviceInformation",
		},
		{
			name:     "From Body Tag",
			body:     `<tds:GetDeviceInformation xmlns:tds="http://www.onvif.org/ver10/device/wsdl"/>`,
			expected: "GetDeviceInformation",
		},
		{
			name:     "From Body Tag with space",
			body:     `<tds:GetSystemDateAndTime />`,
			expected: "GetSystemDateAndTime",
		},
		{
			name:     "Not Found",
			body:     `<tds:UnknownAction />`,
			expected: "",
		},
	}

	knownActions := []string{"GetDeviceInformation", "GetSystemDateAndTime"}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req, _ := http.NewRequest("POST", "/", bytes.NewBufferString(tt.body))
			if tt.header != "" {
				req.Header.Set("SOAPAction", tt.header)
			}
			action := soapAction(req, tt.body, knownActions)
			if action != tt.expected {
				t.Errorf("expected %q, got %q", tt.expected, action)
			}
		})
	}
}

func TestDeviceHandler(t *testing.T) {
	cfg := onvifConfig{
		Username:     "admin",
		Password:     "password",
		DevicePath:   "/onvif/device_service",
		Manufacturer: "TestMfg",
	}
	server := &onvifServer{cfg: cfg, meta: &streamMetadata{}}

	t.Run("GetSystemDateAndTime without Auth", func(t *testing.T) {
		req := httptest.NewRequest("POST", "/onvif/device_service", strings.NewReader(`<s:Envelope><s:Body><GetSystemDateAndTime xmlns="http://www.onvif.org/ver10/device/wsdl"/></s:Body></s:Envelope>`))
		rec := httptest.NewRecorder()
		server.handleDevice(rec, req)

		if rec.Code != http.StatusOK {
			t.Errorf("expected status OK, got %d", rec.Code)
		}
		if !strings.Contains(rec.Body.String(), "GetSystemDateAndTimeResponse") {
			t.Errorf("missing expected response body, got %s", rec.Body.String())
		}
	})

	t.Run("GetDeviceInformation without Auth", func(t *testing.T) {
		req := httptest.NewRequest("POST", "/onvif/device_service", strings.NewReader(`<s:Envelope><s:Body><GetDeviceInformation xmlns="http://www.onvif.org/ver10/device/wsdl"/></s:Body></s:Envelope>`))
		rec := httptest.NewRecorder()
		server.handleDevice(rec, req)

		if rec.Code != http.StatusUnauthorized {
			t.Errorf("expected status Unauthorized, got %d", rec.Code)
		}
	})

	t.Run("GetDeviceInformation with Auth", func(t *testing.T) {
		body := `<s:Envelope>` + generateAuthHeader("admin", "password") + `<s:Body><GetDeviceInformation xmlns="http://www.onvif.org/ver10/device/wsdl"/></s:Body></s:Envelope>`
		req := httptest.NewRequest("POST", "/onvif/device_service", strings.NewReader(body))
		rec := httptest.NewRecorder()
		server.handleDevice(rec, req)

		if rec.Code != http.StatusOK {
			t.Errorf("expected status OK, got %d", rec.Code)
		}
		if !strings.Contains(rec.Body.String(), "TestMfg") {
			t.Errorf("expected manufacturer in response, got %s", rec.Body.String())
		}
	})
}

func TestAudioEncoderConfigXML(t *testing.T) {
	cfg := onvifConfig{ProfileToken: "test_token"}
	server := &onvifServer{cfg: cfg, meta: &streamMetadata{}}

	tests := []struct {
		name     string
		snap     streamMetadataSnapshot
		expected string
	}{
		{
			name: "Default Fallback",
			snap: streamMetadataSnapshot{AudioSampleRate: 0, AudioChannels: 0, AudioCodec: ""},
			expected: `<tt:Encoding>AAC</tt:Encoding>`,
		},
		{
			name: "ADPCM to G711",
			snap: streamMetadataSnapshot{AudioSampleRate: 8000, AudioChannels: 1, AudioCodec: "PCMA"},
			expected: `<tt:Encoding>G711</tt:Encoding>`,
		},
		{
			name: "AAC",
			snap: streamMetadataSnapshot{AudioSampleRate: 16000, AudioChannels: 1, AudioCodec: "AAC"},
			expected: `<tt:Encoding>AAC</tt:Encoding>`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := server.audioEncoderConfigXML("tt:AudioEncoderConfiguration", tt.snap)
			if !strings.Contains(result, tt.expected) {
				t.Errorf("expected XML to contain %q, but got %q", tt.expected, result)
			}
		})
	}
}
