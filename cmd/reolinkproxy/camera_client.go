package main

import (
	"context"
	"fmt"
	"sync"

	"github.com/shareed2k/reolinkproxy/pkg/baichuan"
)

type cameraClientManager struct {
	cameraName string
	cfg        baichuan.Config

	mu     sync.Mutex
	client *baichuan.Client
}

func newCameraClientManager(cameraName string, cfg baichuan.Config) *cameraClientManager {
	return &cameraClientManager{
		cameraName: cameraName,
		cfg:        cfg,
	}
}

func (m *cameraClientManager) Ensure(ctx context.Context) (*baichuan.Client, error) {
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

func (m *cameraClientManager) WithClient(ctx context.Context, fn func(*baichuan.Client) error) error {
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

func (m *cameraClientManager) ResetIfCurrent(client *baichuan.Client, reason string) {
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

func (m *cameraClientManager) Close(reason string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.closeLocked(reason)
}

func (m *cameraClientManager) closeLocked(reason string) {
	if m.client == nil {
		return
	}
	if reason != "" {
		log.Printf("camera %s reconnecting: %s", m.cameraName, reason)
	}
	_ = m.client.Close()
	m.client = nil
}
