package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHandleSnapshot(t *testing.T) {
	t.Parallel()

	newServer := func(user, pass string) *onvifServer {
		return &onvifServer{
			cfg: onvifConfig{Username: user, Password: pass},
			metas: []*streamMetadata{
				{cameraName: "cam", name: "sub", token: "cam_sub", path: "cam/stream"},
			},
		}
	}

	t.Run("unknown path is 404", func(t *testing.T) {
		t.Parallel()
		rec := httptest.NewRecorder()
		newServer("", "").handleSnapshot(rec, httptest.NewRequest("GET", "/api/snapshot/other/stream", nil))
		if rec.Code != http.StatusNotFound {
			t.Fatalf("status = %d, want 404", rec.Code)
		}
	})

	t.Run("stream without device is 404", func(t *testing.T) {
		t.Parallel()
		rec := httptest.NewRecorder()
		newServer("", "").handleSnapshot(rec, httptest.NewRequest("GET", "/api/snapshot/cam/stream", nil))
		if rec.Code != http.StatusNotFound {
			t.Fatalf("status = %d, want 404", rec.Code)
		}
	})

	t.Run("credentials required when configured", func(t *testing.T) {
		t.Parallel()
		server := newServer("admin", "secret")

		rec := httptest.NewRecorder()
		server.handleSnapshot(rec, httptest.NewRequest("GET", "/api/snapshot/cam/stream", nil))
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("status without auth = %d, want 401", rec.Code)
		}

		req := httptest.NewRequest("GET", "/api/snapshot/cam/stream", nil)
		req.SetBasicAuth("admin", "secret")
		rec = httptest.NewRecorder()
		server.handleSnapshot(rec, req)
		// device is nil in this test, so authenticated requests reach the 404 path
		if rec.Code != http.StatusNotFound {
			t.Fatalf("status with auth = %d, want 404 (no device wired)", rec.Code)
		}
	})

	t.Run("token also resolves", func(t *testing.T) {
		t.Parallel()
		server := newServer("", "")
		rec := httptest.NewRecorder()
		server.handleSnapshot(rec, httptest.NewRequest("GET", "/api/snapshot/cam_sub", nil))
		if rec.Code != http.StatusNotFound { // resolves, then 404s on nil device
			t.Fatalf("status = %d, want 404 (no device wired)", rec.Code)
		}
	})
}
