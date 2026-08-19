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
