package main

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestHealthcheckPathsForCameras(t *testing.T) {
	t.Parallel()

	cameras := []CameraConfig{
		{
			Name:        "front",
			Host:        "192.168.1.10",
			Stream:      "main,sub",
			RTSPPath:    "front/stream",
			TalkProfile: "sub",
		},
		{
			Name:     "garage",
			Host:     "192.168.1.11",
			Stream:   "main",
			RTSPPath: "garage/stream",
		},
	}

	got := healthcheckPathsForCameras(cameras)
	want := []string{"front/stream_main", "front/stream_sub", "front/stream", "garage/stream"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("healthcheckPathsForCameras() = %#v, want %#v", got, want)
	}
}

func TestRunRTSPHealthcheckDescribeOK(t *testing.T) {
	t.Parallel()

	addr, requestLine := startTestRTSPServer(t, "RTSP/1.0 200 OK")

	if err := runRTSPHealthcheck(context.Background(), addr, []string{"front/stream"}, time.Second); err != nil {
		t.Fatalf("runRTSPHealthcheck() returned error: %v", err)
	}

	if got, want := <-requestLine, "DESCRIBE rtsp://"+addr+"/front/stream RTSP/1.0"; got != want {
		t.Fatalf("request line = %q, want %q", got, want)
	}
}

func TestRunRTSPHealthcheckDescribeNotFound(t *testing.T) {
	t.Parallel()

	addr, _ := startTestRTSPServer(t, "RTSP/1.0 404 Not Found")

	err := runRTSPHealthcheck(context.Background(), addr, []string{"front/missing"}, time.Second)
	if err == nil {
		t.Fatal("expected RTSP DESCRIBE error")
	}
	if !strings.Contains(err.Error(), "404 Not Found") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func startTestRTSPServer(t *testing.T, statusLine string) (string, <-chan string) {
	t.Helper()

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("net.Listen() error = %v", err)
	}
	t.Cleanup(func() { _ = listener.Close() })

	requestLine := make(chan string, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()

		reader := bufio.NewReader(conn)
		line, err := reader.ReadString('\n')
		if err != nil {
			requestLine <- fmt.Sprintf("read error: %v", err)
			return
		}
		requestLine <- strings.TrimSpace(line)

		for {
			line, err := reader.ReadString('\n')
			if err != nil || line == "\r\n" {
				break
			}
		}

		_, _ = io.WriteString(conn, statusLine+"\r\nCSeq: 1\r\nContent-Length: 0\r\n\r\n")
	}()

	return listener.Addr().String(), requestLine
}

func TestCheckPacketAge(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		status     int
		body       string
		wantErr    bool
		wantSubstr string
	}{
		{
			name:   "healthy",
			status: http.StatusOK,
			body:   "camera=cam stream=sub clients=true video_age=1s state=ok\n",
		},
		{
			name:       "stale stream",
			status:     http.StatusServiceUnavailable,
			body:       "camera=cam stream=sub clients=true video_age=1m0s state=stale\n",
			wantErr:    true,
			wantSubstr: "state=stale",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var gotQuery string
			ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/healthz" {
					t.Errorf("path = %q, want /healthz", r.URL.Path)
				}
				gotQuery = r.URL.Query().Get("max_video_age")
				w.WriteHeader(tt.status)
				_, _ = io.WriteString(w, tt.body)
			}))
			defer ts.Close()

			addr := ts.Listener.Addr().String()
			err := checkPacketAge(context.Background(), addr, 30*time.Second, time.Second)

			if tt.wantErr {
				if err == nil {
					t.Fatal("checkPacketAge() error = nil, want error")
				}
				if !strings.Contains(err.Error(), tt.wantSubstr) {
					t.Fatalf("error %q does not contain %q", err.Error(), tt.wantSubstr)
				}
			} else if err != nil {
				t.Fatalf("checkPacketAge() error = %v, want nil", err)
			}

			if gotQuery != "30s" {
				t.Fatalf("max_video_age query = %q, want %q", gotQuery, "30s")
			}
		})
	}
}
