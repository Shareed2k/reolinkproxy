package main

import (
	"context"
	"errors"
	"log"
	"sync"
	"time"

	"github.com/bluenviron/gortsplib/v4"
	"github.com/shareed2k/reolinkproxy/pkg/baichuan"
)

type rtspSessionState struct {
	stream  *rtspStreamHandler
	playing bool
}

type cameraMotionSnapshot struct {
	Known       bool
	Active      bool
	Unsupported bool
	ChangedAt   time.Time
}

type cameraMotionState struct {
	mu          sync.RWMutex
	snapshot    cameraMotionSnapshot
	subscribers map[chan cameraMotionSnapshot]struct{}
}

func newCameraMotionState() *cameraMotionState {
	return &cameraMotionState{
		snapshot:    cameraMotionSnapshot{ChangedAt: time.Now()},
		subscribers: make(map[chan cameraMotionSnapshot]struct{}),
	}
}

func (s *cameraMotionState) snapshotCopy() cameraMotionSnapshot {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.snapshot
}

func (s *cameraMotionState) setActive(active bool) {
	s.mu.Lock()
	if s.snapshot.Known && s.snapshot.Active == active {
		s.mu.Unlock()
		return
	}

	s.snapshot.Known = true
	s.snapshot.Active = active
	s.snapshot.ChangedAt = time.Now()
	snapshot := s.snapshot
	subscribers := make([]chan cameraMotionSnapshot, 0, len(s.subscribers))
	for ch := range s.subscribers {
		subscribers = append(subscribers, ch)
	}
	s.mu.Unlock()

	for _, ch := range subscribers {
		select {
		case ch <- snapshot:
		default:
		}
	}
}

func (s *cameraMotionState) markUnsupported() {
	s.mu.Lock()
	if s.snapshot.Unsupported {
		s.mu.Unlock()
		return
	}

	s.snapshot.Unsupported = true
	snapshot := s.snapshot
	subscribers := make([]chan cameraMotionSnapshot, 0, len(s.subscribers))
	for ch := range s.subscribers {
		subscribers = append(subscribers, ch)
	}
	s.mu.Unlock()

	for _, ch := range subscribers {
		select {
		case ch <- snapshot:
		default:
		}
	}
}

func (s *cameraMotionState) subscribe() (<-chan cameraMotionSnapshot, func()) {
	ch := make(chan cameraMotionSnapshot, 1)

	s.mu.Lock()
	s.subscribers[ch] = struct{}{}
	snapshot := s.snapshot
	s.mu.Unlock()

	ch <- snapshot

	var once sync.Once
	return ch, func() {
		once.Do(func() {
			s.mu.Lock()
			delete(s.subscribers, ch)
			s.mu.Unlock()
			close(ch)
		})
	}
}

func runCameraMotionListener(ctx context.Context, bc *baichuan.Client, camName string, channel uint8, state *cameraMotionState) {
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		log.Printf("motion: establishing camera listener for %s...", camName)
		cancelMotion, err := bc.ListenForMotion(ctx, channel, func(active bool) {
			state.setActive(active)
		})
		if err != nil {
			var missingAbility *baichuan.MissingAbilityError
			if errors.As(err, &missingAbility) && missingAbility.Name == "motion" {
				log.Printf("motion: listener unsupported for %s: %v", camName, err)
				state.markUnsupported()
				return
			}

			log.Printf("motion: listener error for %s: %v. retrying in 10s...", camName, err)
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
		case <-time.After(5 * time.Minute):
			cancelMotion()
		}
	}
}

type streamPauseConfig struct {
	OnMotion bool
	OnClient bool
	Timeout  time.Duration
	Motion   *cameraMotionState
}

type streamLifecycleConfig struct {
	IdleDisconnect bool
	IdleTimeout    time.Duration
	BatteryCamera  bool
}

func (c CameraConfig) streamPauseConfig(motion *cameraMotionState) streamPauseConfig {
	return streamPauseConfig{
		OnMotion: c.PauseOnMotion,
		OnClient: c.PauseOnClient,
		Timeout:  c.PauseTimeout,
		Motion:   motion,
	}
}

func (c CameraConfig) streamLifecycleConfig() streamLifecycleConfig {
	return streamLifecycleConfig{
		IdleDisconnect: c.IdleDisconnect,
		IdleTimeout:    c.IdleTimeout,
		BatteryCamera:  c.BatteryCamera,
	}
}

func (c streamLifecycleConfig) maxReconnectDelay() time.Duration {
	if c.BatteryCamera {
		return time.Hour
	}
	return 5 * time.Second
}

func (p streamPauseConfig) shouldPause(now time.Time, handler *rtspStreamHandler) (bool, string) {
	if p.OnClient && handler != nil && !handler.hasClients() {
		return true, "no rtsp client"
	}

	if !p.OnMotion || p.Motion == nil {
		return false, ""
	}

	snapshot := p.Motion.snapshotCopy()
	if snapshot.Unsupported || !snapshot.Known || snapshot.Active {
		return false, ""
	}

	timeout := p.Timeout
	if timeout <= 0 {
		timeout = time.Second
	}
	if now.Sub(snapshot.ChangedAt) >= timeout {
		return true, "no motion"
	}

	return false, ""
}

func attachSessionToStream(session *gortsplib.ServerSession, stream *rtspStreamHandler) *rtspSessionState {
	if session == nil {
		return nil
	}
	if state, ok := session.UserData().(*rtspSessionState); ok && state != nil {
		if stream != nil {
			state.stream = stream
		}
		return state
	}

	state := &rtspSessionState{stream: stream}
	session.SetUserData(state)
	return state
}
