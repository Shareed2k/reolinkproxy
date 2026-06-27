package main

import (
	"context"
	"errors"
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

// WatchMotion establishes a persistent motion listener, automatically reconnecting on failures.
// It calls onActive when motion state changes, and onUnsupported if the camera doesn't support it.
func (m *CameraDevice) WatchMotion(ctx context.Context, channel uint8, onActive func(bool), onUnsupported func()) {
	go func() {
		for {
			if ctx.Err() != nil {
				return
			}

			client, err := m.Ensure(ctx)
			if err != nil {
				log.Warnf("motion: camera connect error for %s: %v. retrying in 10s...", m.cameraName, err)
				select {
				case <-ctx.Done():
					return
				case <-time.After(10 * time.Second):
				}
				continue
			}

			log.Printf("motion: establishing camera listener for %s...", m.cameraName)
			cancelMotion, err := client.ListenForMotion(ctx, channel, onActive)
			if err != nil {
				var missingAbility *baichuan.MissingAbilityError
				var statusErr *baichuan.StatusError
				if (errors.As(err, &missingAbility) && missingAbility.Name == "motion") ||
					(errors.As(err, &statusErr) && statusErr.MsgID == 31 && statusErr.Code == 400) {
					log.Warnf("motion: listener unsupported for %s: %v", m.cameraName, err)
					onUnsupported()
					return
				}

				m.ResetIfCurrent(client, fmt.Sprintf("motion listener error: %v", err))
				log.Warnf("motion: listener error for %s: %v. retrying in 10s...", m.cameraName, err)
				select {
				case <-ctx.Done():
					return
				case <-time.After(10 * time.Second):
				}
				continue
			}

			select {
			case <-ctx.Done():
				cancelMotion()
				return
			case <-client.Done():
				cancelMotion()
				if err := client.Err(); err != nil && ctx.Err() == nil {
					m.ResetIfCurrent(client, fmt.Sprintf("motion listener disconnected: %v", err))
					log.Warnf("motion: listener disconnected for %s: %v. reconnecting...", m.cameraName, err)
				}
			case <-time.After(5 * time.Minute):
				cancelMotion()
			}
		}
	}()
}
