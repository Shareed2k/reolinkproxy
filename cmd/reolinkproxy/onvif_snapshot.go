package main

import (
	"context"
	"crypto/subtle"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/shareed2k/reolinkproxy/pkg/baichuan"
)

const snapshotTimeout = 10 * time.Second

// snapshotJPEG fetches one JPEG from the camera. Serialized per stream so
// thumbnail-hungry clients cannot hammer the camera with parallel snaps.
func (m *streamMetadata) snapshotJPEG(ctx context.Context) ([]byte, error) {
	m.snapMu.Lock()
	defer m.snapMu.Unlock()

	var jpeg []byte
	err := m.device.WithClient(ctx, func(bc *baichuan.Client) error {
		var err error
		jpeg, err = bc.Snap(ctx, m.channel, m.name)
		return err
	})
	return jpeg, err
}

// metaForSnapshotPath resolves the path portion of /api/snapshot/<path> to a
// stream: by RTSP path first, then by profile token or stream name.
func (s *onvifServer) metaForSnapshotPath(path string) *streamMetadata {
	path = strings.Trim(path, "/")
	if path == "" {
		return nil
	}
	for _, m := range s.metas {
		if m != nil && strings.Trim(m.path, "/") == path {
			return m
		}
	}
	for _, m := range s.metas {
		if m != nil && (m.token == path || m.name == path) {
			return m
		}
	}
	return nil
}

// authenticateSnapshot guards the plain-HTTP snapshot endpoint with Basic
// auth using the ONVIF credentials, since WS-Security does not apply here.
func (s *onvifServer) authenticateSnapshot(r *http.Request) bool {
	if s.cfg.Username == "" && s.cfg.Password == "" {
		return true
	}
	user, pass, ok := r.BasicAuth()
	if !ok {
		return false
	}
	userOK := subtle.ConstantTimeCompare([]byte(user), []byte(s.cfg.Username)) == 1
	passOK := subtle.ConstantTimeCompare([]byte(pass), []byte(s.cfg.Password)) == 1
	return userOK && passOK
}

func (s *onvifServer) handleSnapshot(w http.ResponseWriter, r *http.Request) {
	if !s.authenticateSnapshot(r) {
		w.Header().Set("WWW-Authenticate", `Basic realm="reolinkproxy"`)
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	meta := s.metaForSnapshotPath(strings.TrimPrefix(r.URL.Path, "/api/snapshot/"))
	if meta == nil || meta.device == nil {
		http.Error(w, "unknown snapshot path", http.StatusNotFound)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), snapshotTimeout)
	defer cancel()

	jpeg, err := meta.snapshotJPEG(ctx)
	if err != nil {
		log.Printf("snapshot %s/%s failed: %v", meta.cameraName, meta.name, err)
		http.Error(w, "snapshot unavailable", http.StatusBadGateway)
		return
	}

	w.Header().Set("Content-Type", "image/jpeg")
	w.Header().Set("Content-Length", strconv.Itoa(len(jpeg)))
	_, _ = w.Write(jpeg)
}
