package baichuan

import (
	"context"
	"fmt"
	"net"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestClient_TCPReadTimeout(t *testing.T) {
	// Create a TCP listener on a random port
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer ln.Close()

	// Accept a connection but never send data
	go func() {
		conn, err := ln.Accept()
		if err == nil {
			// keep it open, but do not write to it
			defer conn.Close()
			time.Sleep(5 * time.Second)
		}
	}()

	host, portStr, err := net.SplitHostPort(ln.Addr().String())
	require.NoError(t, err)
	port := 0
	fmt.Sscanf(portStr, "%d", &port)

	cfg := Config{
		Host:    host,
		Port:    port,
		Timeout: 50 * time.Millisecond,
	}

	ctx := context.Background()
	client, err := Dial(ctx, cfg)
	require.NoError(t, err)

	// Dial creates the client and starts the readLoop.
	// Since the server doesn't send anything, readMessage should timeout after 50ms.
	// The client's readLoop will eventually call c.shutdown(err).

	select {
	case <-client.Done():
		err := client.Err()
		require.Error(t, err)
		require.Contains(t, err.Error(), "i/o timeout")
	case <-time.After(2 * time.Second):
		t.Fatal("expected client to timeout and close, but it didn't")
	}
}
