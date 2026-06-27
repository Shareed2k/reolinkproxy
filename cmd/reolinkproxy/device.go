package main

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/shareed2k/reolinkproxy/pkg/baichuan"
)

type CameraDevice struct {
	cameraName string
	cfg        baichuan.Config

	mu     sync.Mutex
	client *baichuan.Client
}

func NewCameraDevice(cameraName string, cfg baichuan.Config) *CameraDevice {
	return &CameraDevice{
		cameraName: cameraName,
		cfg:        cfg,
	}
}

func (m *CameraDevice) Ensure(ctx context.Context) (*baichuan.Client, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.client != nil {
		if err := m.client.Err(); err == nil {
			return m.client, nil
		}
		m.closeLocked("")
	}

	client, err := baichuan.Dial(ctx, m.cfg)
	if err != nil {
		return nil, err
	}
	if err := client.Login(ctx); err != nil {
		_ = client.Close()
		return nil, err
	}

	m.client = client
	return client, nil
}

func (m *CameraDevice) WithClient(ctx context.Context, fn func(*baichuan.Client) error) error {
	client, err := m.Ensure(ctx)
	if err != nil {
		return err
	}

	err = fn(client)
	if err != nil {
		if closeErr := client.Err(); closeErr != nil {
			m.ResetIfCurrent(client, fmt.Sprintf("client closed: %v", closeErr))
		}
	}
	return err
}

func (m *CameraDevice) ResetIfCurrent(client *baichuan.Client, reason string) {
	if client == nil {
		return
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	if m.client != client {
		return
	}
	m.closeLocked(reason)
}

func (m *CameraDevice) Close(reason string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.closeLocked(reason)
}

func (m *CameraDevice) closeLocked(reason string) {
	if m.client == nil {
		return
	}
	if reason != "" {
		log.Printf("camera %s reconnecting: %s", m.cameraName, reason)
	}
	_ = m.client.Close()
	m.client = nil
}

func (m *CameraDevice) StreamPackets(ctx context.Context, channel uint8, stream baichuan.Stream) <-chan baichuan.MediaPacket {
	out := make(chan baichuan.MediaPacket, 50)

	go func() {
		defer close(out)

		for ctx.Err() == nil {
			client, err := m.Ensure(ctx)
			if err != nil {
				time.Sleep(2 * time.Second) // backoff
				continue
			}

			reader, err := client.StartPreview(ctx, channel, stream)
			if err != nil {
				m.ResetIfCurrent(client, fmt.Sprintf("start preview failed: %v", err))
				time.Sleep(2 * time.Second) // backoff
				continue
			}

			for packet := range reader.Packets {
				if ctx.Err() != nil {
					return
				}
				out <- packet
			}

			m.ResetIfCurrent(client, "preview stream ended")
			time.Sleep(100 * time.Millisecond) // brief wait before reconnect
		}
	}()

	return out
}
