package main

import (
	"context"
	"fmt"
	"net"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/shareed2k/reolinkproxy/pkg/baichuan"
)

func TestCameraDevice_ReconnectOnError(t *testing.T) {
	// Setup a dummy TCP server that accepts connections and just hangs, simulating a camera.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer ln.Close()

	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				time.Sleep(30 * time.Second)
			}(conn)
		}
	}()

	host, portStr, err := net.SplitHostPort(ln.Addr().String())
	require.NoError(t, err)
	port := 0
	fmt.Sscanf(portStr, "%d", &port)

	cfg := baichuan.Config{
		Host:    host,
		Port:    port,
		Timeout: 50 * time.Millisecond,
	}

	device := NewCameraDevice("test-cam", cfg)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	// Initial connection (will time out on login, but Ensure only checks if dial + login succeeds)
	// Since our dummy server doesn't respond to Login, Ensure will fail with a timeout or EOF.
	_, err = device.Ensure(ctx)
	require.Error(t, err)

	// Since Ensure fails, client won't be cached
	device.mu.Lock()
	require.Nil(t, device.client)
	device.mu.Unlock()
}

func TestMotionUnsupportedError(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		err  error
		want bool
	}{
		{name: "cmd 31 status 400", err: &baichuan.StatusError{MsgID: 31, Code: 400}, want: true},
		{name: "cmd 31 status 402 (Elite Floodlight)", err: &baichuan.StatusError{MsgID: 31, Code: 402}, want: true},
		{name: "cmd 31 status 405", err: &baichuan.StatusError{MsgID: 31, Code: 405}, want: true},
		{name: "cmd 31 server-side 500 retries", err: &baichuan.StatusError{MsgID: 31, Code: 500}, want: false},
		{name: "other command status is not motion-specific", err: &baichuan.StatusError{MsgID: 33, Code: 400}, want: false},
		{name: "missing motion ability", err: &baichuan.MissingAbilityError{Name: "motion"}, want: true},
		{name: "other missing ability", err: &baichuan.MissingAbilityError{Name: "ptz"}, want: false},
		{name: "wrapped status error", err: fmt.Errorf("motion listener: %w", &baichuan.StatusError{MsgID: 31, Code: 402}), want: true},
		{name: "plain error retries", err: fmt.Errorf("dial tcp: timeout"), want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := motionUnsupportedError(tt.err); got != tt.want {
				t.Fatalf("motionUnsupportedError(%v) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}
