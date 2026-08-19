package baichuan

import (
	"net"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestUIDSession_ReadTimeout(t *testing.T) {
	// Setup a dummy UDP connection
	conn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4zero, Port: 0})
	require.NoError(t, err)
	defer conn.Close()

	session := &uidSession{
		conn:        conn,
		remoteAddr:  &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 1234},
		mtu:         int(defaultUIDMTU),
		clientID:    1234,
		cameraID:    5678,
		timeout:     50 * time.Millisecond,
		readQueue:   make(chan []byte, 128),
		writeQueue:  make(chan []byte, 128),
		closeCh:     make(chan struct{}),
		sentPackets: make(map[uint32][]byte),
		recvPackets: make(map[uint32][]byte),
	}

	session.wg.Add(1)
	go session.readLoop()

	// Wait for the session to close due to timeout
	select {
	case <-session.closeCh:
		err := session.closeState.get()
		require.Error(t, err)
		require.Contains(t, err.Error(), "uid session timed out")
	case <-time.After(2 * time.Second):
		t.Fatal("expected uid session to timeout and close, but it didn't")
	}

	session.wg.Wait()
}
