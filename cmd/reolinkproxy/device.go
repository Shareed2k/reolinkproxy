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

			timer := time.NewTimer(15 * time.Second)
		readLoop:
			for {
				select {
				case <-ctx.Done():
					timer.Stop()
					return
				case packet, ok := <-reader.Packets:
					if !ok {
						timer.Stop()
						break readLoop
					}
					select {
					case <-ctx.Done():
						timer.Stop()
						return
					case out <- packet:
					}
					if !timer.Stop() {
						select {
						case <-timer.C:
						default:
						}
					}
					timer.Reset(15 * time.Second)
				case <-timer.C:
					log.Printf("stream %s channel %d stalled for 15s, reconnecting", m.cameraName, channel)
					m.ResetIfCurrent(client, "stream stalled for 15s")
					break readLoop
				}
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

type ResilientTalkSession struct {
	device     *CameraDevice
	channel    uint8
	mu         sync.Mutex
	session    *baichuan.TalkSession
	sampleRate int
	samplesPB  int
	bytesPB    int
	closed     bool
}

func (s *ResilientTalkSession) SampleRate() int {
	return s.sampleRate
}

func (s *ResilientTalkSession) SamplesPerBlock() int {
	return s.samplesPB
}

func (s *ResilientTalkSession) BytesPerBlock() int {
	return s.bytesPB
}

func (s *ResilientTalkSession) WriteADPCMBlock(ctx context.Context, block []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.closed {
		return errors.New("talk session closed")
	}

	if s.session != nil {
		err := s.session.WriteADPCMBlock(ctx, block)
		if err == nil {
			return nil
		}
		// Failure, clear session and fall through to reconnect
		client, _ := s.device.Ensure(ctx)
		s.device.ResetIfCurrent(client, fmt.Sprintf("talk write error: %v", err))
		s.session = nil
	}

	// Try to reconnect once
	client, err := s.device.Ensure(ctx)
	if err != nil {
		return nil // Drop audio quietly while reconnecting
	}

	newSession, err := client.StartTalk(ctx, s.channel)
	if err != nil {
		s.device.ResetIfCurrent(client, fmt.Sprintf("talk restart error: %v", err))
		return nil // Drop audio quietly
	}

	s.session = newSession
	// Discard error on fresh write; if it fails, it'll retry next time
	_ = s.session.WriteADPCMBlock(ctx, block)
	return nil
}

func (s *ResilientTalkSession) Close(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true
	if s.session != nil {
		return s.session.Close(ctx)
	}
	return nil
}

func (m *CameraDevice) StartTalk(ctx context.Context, channel uint8) (*ResilientTalkSession, error) {
	client, err := m.Ensure(ctx)
	if err != nil {
		return nil, err
	}

	session, err := client.StartTalk(ctx, channel)
	if err != nil {
		m.ResetIfCurrent(client, fmt.Sprintf("initial talk start error: %v", err))
		return nil, err
	}

	return &ResilientTalkSession{
		device:     m,
		channel:    channel,
		session:    session,
		sampleRate: session.SampleRate(),
		samplesPB:  session.SamplesPerBlock(),
		bytesPB:    session.BytesPerBlock(),
	}, nil
}
