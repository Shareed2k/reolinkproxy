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

	gortsplib "github.com/bluenviron/gortsplib/v5"
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

func generatePlaintextAuthHeader(username, password string) string {
	nonce := "1234567890"
	nonceB64 := base64.StdEncoding.EncodeToString([]byte(nonce))
	created := time.Now().UTC().Format(time.RFC3339)

	return fmt.Sprintf(`
		<soap:Header>
			<Security xmlns="http://docs.oasis-open.org/wss/2004/01/oasis-200401-wss-wssecurity-secext-1.0.xsd">
				<UsernameToken>
					<Username>%s</Username>
					<Password Type="http://docs.oasis-open.org/wss/2004/01/oasis-200401-wss-username-token-profile-1.0#PasswordText">%s</Password>
					<Nonce EncodingType="http://docs.oasis-open.org/wss/2004/01/oasis-200401-wss-soap-message-security-1.0#Base64Binary">%s</Nonce>
					<Created xmlns="http://docs.oasis-open.org/wss/2004/01/oasis-200401-wss-wssecurity-utility-1.0.xsd">%s</Created>
				</UsernameToken>
			</Security>
		</soap:Header>`, username, password, nonceB64, created)
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

	t.Run("Valid Plaintext Credentials", func(t *testing.T) {
		body := `<soap:Envelope>` + generatePlaintextAuthHeader("admin", "password123") + `</soap:Envelope>`
		if !server.authenticate(body) {
			t.Errorf("expected plaintext authentication to succeed")
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
	server := &onvifServer{cfg: cfg, metas: []*streamMetadata{{}}}

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
	server := &onvifServer{cfg: cfg, metas: []*streamMetadata{{}}}

	tests := []struct {
		name     string
		snap     streamMetadataSnapshot
		expected string
	}{
		{
			name:     "Default Fallback",
			snap:     streamMetadataSnapshot{AudioSampleRate: 0, AudioChannels: 0, AudioCodec: ""},
			expected: `<tt:Encoding>AAC</tt:Encoding>`,
		},
		{
			name:     "ADPCM to G711",
			snap:     streamMetadataSnapshot{AudioSampleRate: 8000, AudioChannels: 1, AudioCodec: "PCMA"},
			expected: `<tt:Encoding>G711</tt:Encoding>`,
		},
		{
			name:     "AAC",
			snap:     streamMetadataSnapshot{AudioSampleRate: 16000, AudioChannels: 1, AudioCodec: "AAC"},
			expected: `<tt:Encoding>AAC</tt:Encoding>`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := server.audioEncoderConfigXML("tt:AudioEncoderConfiguration", "main", tt.snap)
			if !strings.Contains(result, tt.expected) {
				t.Errorf("expected XML to contain %q, but got %q", tt.expected, result)
			}
		})
	}
}

func TestMediaProfilesResponseUsesUniqueCameraTokens(t *testing.T) {
	server := &onvifServer{
		cfg: onvifConfig{DeviceName: "ReolinkProxy"},
		metas: []*streamMetadata{
			{cameraName: "front", name: "main", token: onvifProfileToken("front", "main")},
			{cameraName: "garage", name: "main", token: onvifProfileToken("garage", "main")},
		},
	}

	resp := server.mediaProfilesResponse()
	if !strings.Contains(resp, `token="front_main"`) {
		t.Fatalf("expected front profile token in response: %s", resp)
	}
	if !strings.Contains(resp, `token="garage_main"`) {
		t.Fatalf("expected garage profile token in response: %s", resp)
	}
}

func TestMediaProfilesResponsePreservesPreferredProfileOrder(t *testing.T) {
	server := &onvifServer{
		cfg: onvifConfig{DeviceName: "ReolinkProxy"},
		metas: []*streamMetadata{
			{cameraName: "office", name: "sub", path: "office/stream", token: onvifProfileToken("office", "sub")},
			{cameraName: "office", name: "main", path: "office/stream_main", token: onvifProfileToken("office", "main")},
		},
	}

	resp := server.mediaProfilesResponse()
	subIdx := strings.Index(resp, `token="office_sub"`)
	mainIdx := strings.Index(resp, `token="office_main"`)
	if subIdx == -1 || mainIdx == -1 {
		t.Fatalf("expected both office profiles in response: %s", resp)
	}
	if subIdx > mainIdx {
		t.Fatalf("expected preferred sub profile before main profile: %s", resp)
	}
}

func TestHandleHealthz(t *testing.T) {
	t.Parallel()

	now := time.Now()

	newMeta := func(camera, stream string, videoAge time.Duration, clients bool) *streamMetadata {
		meta := &streamMetadata{cameraName: camera, name: stream}
		meta.startedAtMicro.Store(now.Add(-time.Hour).UnixMicro())
		meta.lastVideoAtMicro.Store(now.Add(-videoAge).UnixMicro())
		handler := newRTSPStreamHandler(camera + "/stream_" + stream)
		if clients {
			handler.clients[&gortsplib.ServerSession{}] = struct{}{}
		}
		meta.rtspHandler = handler
		return meta
	}

	tests := []struct {
		name       string
		query      string
		metas      []*streamMetadata
		wantStatus int
	}{
		{
			name:       "fresh stream with clients is healthy",
			query:      "?max_video_age=30s",
			metas:      []*streamMetadata{newMeta("cam", "sub", time.Second, true)},
			wantStatus: http.StatusOK,
		},
		{
			name:       "stale stream with clients fails",
			query:      "?max_video_age=30s",
			metas:      []*streamMetadata{newMeta("cam", "sub", time.Minute, true)},
			wantStatus: http.StatusServiceUnavailable,
		},
		{
			name:       "stale stream without clients is ignored",
			query:      "?max_video_age=30s",
			metas:      []*streamMetadata{newMeta("cam", "sub", time.Minute, false)},
			wantStatus: http.StatusOK,
		},
		{
			name:       "never-started stream is ignored",
			query:      "?max_video_age=30s",
			metas:      []*streamMetadata{{cameraName: "cam", name: "sub"}},
			wantStatus: http.StatusOK,
		},
		{
			name:       "no threshold reports only",
			query:      "",
			metas:      []*streamMetadata{newMeta("cam", "sub", time.Hour, true)},
			wantStatus: http.StatusOK,
		},
		{
			name:       "invalid threshold is a client error",
			query:      "?max_video_age=bogus",
			metas:      nil,
			wantStatus: http.StatusBadRequest,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			server := &onvifServer{metas: tt.metas}
			req := httptest.NewRequest("GET", "/healthz"+tt.query, nil)
			rec := httptest.NewRecorder()
			server.handleHealthz(rec, req)

			if rec.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d; body:\n%s", rec.Code, tt.wantStatus, rec.Body.String())
			}
		})
	}
}

func generateAuthHeaderAt(username, password, nonce string, created time.Time) string {
	nonceB64 := base64.StdEncoding.EncodeToString([]byte(nonce))
	createdStr := created.UTC().Format(time.RFC3339)

	h := sha1.New()
	h.Write([]byte(nonce))
	h.Write([]byte(createdStr))
	h.Write([]byte(password))
	digest := base64.StdEncoding.EncodeToString(h.Sum(nil))

	return fmt.Sprintf(`
		<soap:Header>
			<Security xmlns="http://docs.oasis-open.org/wss/2004/01/oasis-200401-wss-wssecurity-secext-1.0.xsd">
				<UsernameToken>
					<Username>%s</Username>
					<Password Type="http://docs.oasis-open.org/wss/2004/01/oasis-200401-wss-username-token-profile-1.0#PasswordDigest">%s</Password>
					<Nonce>%s</Nonce>
					<Created>%s</Created>
				</UsernameToken>
			</Security>
		</soap:Header>`, username, digest, nonceB64, createdStr)
}

func TestONVIFAuthHardening(t *testing.T) {
	t.Parallel()

	newServer := func() *onvifServer {
		return &onvifServer{cfg: onvifConfig{Username: "admin", Password: "password123"}}
	}

	t.Run("replayed nonce rejected", func(t *testing.T) {
		t.Parallel()
		server := newServer()
		body := `<soap:Envelope>` + generateAuthHeaderAt("admin", "password123", "replay-nonce", time.Now()) + `</soap:Envelope>`
		if !server.authenticate(body) {
			t.Fatal("first use of nonce must succeed")
		}
		if server.authenticate(body) {
			t.Fatal("replayed Security header must be rejected")
		}
	})

	t.Run("stale Created rejected", func(t *testing.T) {
		t.Parallel()
		server := newServer()
		body := `<soap:Envelope>` + generateAuthHeaderAt("admin", "password123", "stale-nonce", time.Now().Add(-time.Hour)) + `</soap:Envelope>`
		if server.authenticate(body) {
			t.Fatal("Created outside the skew window must be rejected")
		}
	})

	t.Run("future Created rejected", func(t *testing.T) {
		t.Parallel()
		server := newServer()
		body := `<soap:Envelope>` + generateAuthHeaderAt("admin", "password123", "future-nonce", time.Now().Add(time.Hour)) + `</soap:Envelope>`
		if server.authenticate(body) {
			t.Fatal("Created too far in the future must be rejected")
		}
	})

	t.Run("untyped token with nonce cannot downgrade to plaintext", func(t *testing.T) {
		t.Parallel()
		server := newServer()
		body := `<soap:Envelope>
			<soap:Header>
				<Security xmlns="http://docs.oasis-open.org/wss/2004/01/oasis-200401-wss-wssecurity-secext-1.0.xsd">
					<UsernameToken>
						<Username>admin</Username>
						<Password>password123</Password>
						<Nonce>` + base64.StdEncoding.EncodeToString([]byte("dg-nonce")) + `</Nonce>
						<Created>` + time.Now().UTC().Format(time.RFC3339) + `</Created>
					</UsernameToken>
				</Security>
			</soap:Header>
		</soap:Envelope>`
		if server.authenticate(body) {
			t.Fatal("untyped token with Nonce/Created must require a digest, not accept the plaintext password")
		}
	})
}

func TestProfileXMLSchemaOrder(t *testing.T) {
	t.Parallel()

	server := &onvifServer{cfg: onvifConfig{DeviceName: "dev"}}
	meta := &streamMetadata{cameraName: "cam", name: "main", token: "cam_main"}
	meta.setVideoInfo(1920, 1080, 20, "H264")
	meta.setAudioAAC(16000, 1)

	t.Run("ver10 profile nests audio output under Extension after PTZ", func(t *testing.T) {
		t.Parallel()
		profile := server.profileXML("trt:Profiles", "cam_main", meta)

		ptzIdx := strings.Index(profile, "<tt:PTZConfiguration")
		extIdx := strings.Index(profile, "<tt:Extension>")
		outIdx := strings.Index(profile, "<tt:AudioOutputConfiguration")
		if ptzIdx == -1 || extIdx == -1 || outIdx == -1 {
			t.Fatalf("profile is missing PTZ/Extension/AudioOutput:\n%s", profile)
		}
		if !(ptzIdx < extIdx && extIdx < outIdx) {
			t.Fatalf("onvif.xsd order violated (PTZ < Extension < AudioOutput):\n%s", profile)
		}
	})

	t.Run("ver20 profile emits AudioOutput after PTZ", func(t *testing.T) {
		t.Parallel()
		profile := server.profile2XML("tr2:Profiles", "cam_main", meta)

		encIdx := strings.Index(profile, "<tr2:VideoEncoder")
		ptzIdx := strings.Index(profile, "<tr2:PTZ")
		outIdx := strings.Index(profile, "<tr2:AudioOutput")
		if encIdx == -1 || ptzIdx == -1 || outIdx == -1 {
			t.Fatalf("profile is missing VideoEncoder/PTZ/AudioOutput:\n%s", profile)
		}
		if !(encIdx < ptzIdx && ptzIdx < outIdx) {
			t.Fatalf("media2 ConfigurationSet order violated (VideoEncoder < PTZ < AudioOutput):\n%s", profile)
		}
	})
}

func TestVideoEncoderConfigXMLVer10(t *testing.T) {
	t.Parallel()

	server := &onvifServer{}

	t.Run("H264 carries mandatory Multicast and H264Profile", func(t *testing.T) {
		t.Parallel()
		snap := streamMetadataSnapshot{Width: 1920, Height: 1080, FPS: 20, VideoCodec: "H264"}.normalized()
		got := server.videoEncoderConfigXML("tt:VideoEncoderConfiguration", "tok", snap)
		for _, want := range []string{"<tt:Multicast>", "<tt:SessionTimeout>", "<tt:H264Profile>Main</tt:H264Profile>", "<tt:H264><tt:GovLength>"} {
			if !strings.Contains(got, want) {
				t.Fatalf("missing %q in:\n%s", want, got)
			}
		}
	})

	t.Run("H265 is not representable in ver10", func(t *testing.T) {
		t.Parallel()
		snap := streamMetadataSnapshot{Width: 1920, Height: 1080, FPS: 20, VideoCodec: "H265"}.normalized()
		if got := server.videoEncoderConfigXML("tt:VideoEncoderConfiguration", "tok", snap); got != "" {
			t.Fatalf("expected empty encoder config for H265 in ver10, got:\n%s", got)
		}
	})
}

func TestVideoEncoder2ConfigXMLAttrsAndOrder(t *testing.T) {
	t.Parallel()

	server := &onvifServer{}
	snap := streamMetadataSnapshot{Width: 2560, Height: 1440, FPS: 25, VideoCodec: "H265"}.normalized()
	got := server.videoEncoder2ConfigXML("tr2:Configurations", "tok", snap)

	if !strings.Contains(got, `GovLength="50"`) || !strings.Contains(got, `Profile="Main"`) {
		t.Fatalf("GovLength/Profile must be attributes in ver20:\n%s", got)
	}
	if strings.Contains(got, "<tt:GovLength>") || strings.Contains(got, "<tt:EncodingInterval>") {
		t.Fatalf("ver20 encoder must not carry ver10-only elements:\n%s", got)
	}
	rcIdx := strings.Index(got, "<tt:RateControl>")
	qIdx := strings.Index(got, "<tt:Quality>")
	if rcIdx == -1 || qIdx == -1 || qIdx < rcIdx {
		t.Fatalf("Quality must come after RateControl in ver20:\n%s", got)
	}
}

func TestUnknownProfileTokenFaults(t *testing.T) {
	t.Parallel()

	server := &onvifServer{
		cfg:   onvifConfig{MediaPath: "/onvif/media_service"},
		metas: []*streamMetadata{{cameraName: "cam", name: "main", token: "cam_main", path: "cam/stream"}},
	}

	body := `<s:Envelope><s:Body><trt:GetStreamUri xmlns:trt="http://www.onvif.org/ver10/media/wsdl"><trt:ProfileToken>bogus_token</trt:ProfileToken></trt:GetStreamUri></s:Body></s:Envelope>`
	req := httptest.NewRequest("POST", "/onvif/media_service", strings.NewReader(body))
	req.Header.Set("SOAPAction", `"http://www.onvif.org/ver10/media/wsdl/GetStreamUri"`)
	rec := httptest.NewRecorder()
	server.handleMedia(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d; body:\n%s", rec.Code, http.StatusBadRequest, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "ter:NoProfile") {
		t.Fatalf("expected ter:NoProfile fault, got:\n%s", rec.Body.String())
	}

	// A valid token must still resolve.
	valid := strings.ReplaceAll(body, "bogus_token", "cam_main")
	req = httptest.NewRequest("POST", "/onvif/media_service", strings.NewReader(valid))
	req.Header.Set("SOAPAction", `"http://www.onvif.org/ver10/media/wsdl/GetStreamUri"`)
	rec = httptest.NewRecorder()
	server.handleMedia(rec, req)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "cam/stream") {
		t.Fatalf("valid token must return the stream URI, got %d:\n%s", rec.Code, rec.Body.String())
	}
}
