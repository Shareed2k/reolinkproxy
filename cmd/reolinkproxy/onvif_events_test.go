package main

import (
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"time"
)

func newEventTestServer() *onvifServer {
	return &onvifServer{
		cfg:    onvifConfig{EventPath: "/onvif/event_service"},
		events: newONVIFEventManager(),
	}
}

func doEvents(t *testing.T, s *onvifServer, action, target, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest("POST", target, strings.NewReader(body))
	req.Header.Set("SOAPAction", `"http://www.onvif.org/ver10/events/wsdl/`+action+`"`)
	rec := httptest.NewRecorder()
	s.handleEvents(rec, req)
	return rec
}

var subRefRe = regexp.MustCompile(`\?sub=(\d+)`)

func createSubscription(t *testing.T, s *onvifServer) string {
	t.Helper()
	rec := doEvents(t, s, "CreatePullPointSubscription", "/onvif/event_service", `<CreatePullPointSubscription><InitialTerminationTime>PT1M</InitialTerminationTime></CreatePullPointSubscription>`)
	if rec.Code != http.StatusOK {
		t.Fatalf("CreatePullPointSubscription status = %d; body:\n%s", rec.Code, rec.Body.String())
	}
	m := subRefRe.FindStringSubmatch(rec.Body.String())
	if m == nil {
		t.Fatalf("no subscription reference in response:\n%s", rec.Body.String())
	}
	return m[1]
}

func TestEventServiceSubscriptionLifecycle(t *testing.T) {
	t.Parallel()

	s := newEventTestServer()
	subID := createSubscription(t, s)

	// Motion transition lands in the subscription.
	s.events.dispatch("cam", true, time.Now())

	rec := doEvents(t, s, "PullMessages", "/onvif/event_service?sub="+subID, `<PullMessages><Timeout>PT1S</Timeout><MessageLimit>10</MessageLimit></PullMessages>`)
	if rec.Code != http.StatusOK {
		t.Fatalf("PullMessages status = %d; body:\n%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, want := range []string{
		"tns1:RuleEngine/CellMotionDetector/Motion",
		"tns1:VideoSource/MotionAlarm",
		`Name="IsMotion" Value="true"`,
		`Name="State" Value="true"`,
		`PropertyOperation="Changed"`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("PullMessages response missing %q:\n%s", want, body)
		}
	}

	// Renew extends, Unsubscribe removes.
	if rec := doEvents(t, s, "Renew", "/onvif/event_service?sub="+subID, `<Renew><TerminationTime>PT2M</TerminationTime></Renew>`); rec.Code != http.StatusOK {
		t.Fatalf("Renew status = %d; body:\n%s", rec.Code, rec.Body.String())
	}
	if rec := doEvents(t, s, "Unsubscribe", "/onvif/event_service?sub="+subID, `<Unsubscribe/>`); rec.Code != http.StatusOK {
		t.Fatalf("Unsubscribe status = %d; body:\n%s", rec.Code, rec.Body.String())
	}
	if rec := doEvents(t, s, "PullMessages", "/onvif/event_service?sub="+subID, `<PullMessages><Timeout>PT1S</Timeout></PullMessages>`); rec.Code != http.StatusBadRequest {
		t.Fatalf("PullMessages after Unsubscribe status = %d, want 400", rec.Code)
	}
}

func TestEventServicePullTimeoutReturnsEmpty(t *testing.T) {
	t.Parallel()

	s := newEventTestServer()
	subID := createSubscription(t, s)

	start := time.Now()
	rec := doEvents(t, s, "PullMessages", "/onvif/event_service?sub="+subID, `<PullMessages><Timeout>PT1S</Timeout></PullMessages>`)
	elapsed := time.Since(start)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "NotificationMessage") {
		t.Fatalf("expected empty pull, got:\n%s", rec.Body.String())
	}
	if elapsed < 900*time.Millisecond || elapsed > 5*time.Second {
		t.Fatalf("long-poll waited %v, want ~1s", elapsed)
	}
}

func TestEventServiceInitialStateIsDelivered(t *testing.T) {
	t.Parallel()

	s := newEventTestServer()
	s.events.dispatch("cam", true, time.Now()) // state known before subscribing

	subID := createSubscription(t, s)
	rec := doEvents(t, s, "PullMessages", "/onvif/event_service?sub="+subID, `<PullMessages><Timeout>PT1S</Timeout></PullMessages>`)
	body := rec.Body.String()
	if !strings.Contains(body, `PropertyOperation="Initialized"`) {
		t.Fatalf("expected Initialized property message on fresh subscription:\n%s", body)
	}
}

func TestEventServiceExpiry(t *testing.T) {
	t.Parallel()

	s := newEventTestServer()
	sub := s.events.create(time.Millisecond, time.Now())
	time.Sleep(5 * time.Millisecond)
	if got := s.events.get(sub.id, time.Now()); got != nil {
		t.Fatal("expired subscription must be purged")
	}
}

func TestEventServiceGetEventProperties(t *testing.T) {
	t.Parallel()

	rec := doEvents(t, newEventTestServer(), "GetEventProperties", "/onvif/event_service", `<GetEventProperties/>`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{"<wstop:TopicSet>", "CellMotionDetector", "MotionAlarm", "wstop:topic=\"true\""} {
		if !strings.Contains(body, want) {
			t.Fatalf("GetEventProperties missing %q:\n%s", want, body)
		}
	}
}

func TestParseXSDuration(t *testing.T) {
	t.Parallel()

	tests := []struct {
		raw  string
		want time.Duration
	}{
		{"PT60S", time.Minute},
		{"PT1M", time.Minute},
		{"PT1H", time.Hour},
		{"PT0.5S", 500 * time.Millisecond},
		{"", 10 * time.Second},      // fallback
		{"bogus", 10 * time.Second}, // fallback
		{"PT10H", time.Hour},        // ceiling
	}
	for _, tt := range tests {
		if got := parseXSDuration(tt.raw, 10*time.Second, time.Hour); got != tt.want {
			t.Fatalf("parseXSDuration(%q) = %v, want %v", tt.raw, got, tt.want)
		}
	}
}
