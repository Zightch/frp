package client

import (
	"context"
	"fmt"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/zightch/frp/frps/pkg/protocol"
)

type sessionState struct {
	heartbeatInterval time.Duration
	readTimeout       time.Duration

	writeMu sync.Mutex

	nextRequestID          atomic.Uint32
	lastAckedConfigVersion atomic.Uint64
	activeStreams          atomic.Uint32
	activeUDPSessions      atomic.Uint32

	snapshotMu sync.RWMutex
	snapshot   protocol.ConfigPush

	streamMu sync.Mutex
	streams  map[uint32]*localStream

	udpMu       sync.Mutex
	udpSessions map[uint32]*localUDPSession
}

func newSessionState(heartbeatIntervalMs uint32) *sessionState {
	heartbeatInterval := time.Duration(heartbeatIntervalMs) * time.Millisecond
	if heartbeatInterval <= 0 {
		heartbeatInterval = 15 * time.Second
	}

	state := &sessionState{
		heartbeatInterval: heartbeatInterval,
		readTimeout:       sessionReadTimeout(heartbeatInterval),
		streams:           make(map[uint32]*localStream),
		udpSessions:       make(map[uint32]*localUDPSession),
	}
	state.nextRequestID.Store(2)
	return state
}

func sessionReadTimeout(heartbeatInterval time.Duration) time.Duration {
	timeout := heartbeatInterval * 3
	if timeout < defaultReadTimeout {
		return defaultReadTimeout
	}
	return timeout
}

func (s *sessionState) nextClientRequestID() uint32 {
	requestID := s.nextRequestID.Add(1)
	if requestID == 0 {
		s.nextRequestID.Store(1)
		requestID = s.nextRequestID.Add(1)
	}
	return requestID
}

func (s *sessionState) setSnapshot(snapshot protocol.ConfigPush) {
	s.snapshotMu.Lock()
	defer s.snapshotMu.Unlock()
	s.snapshot = snapshot
}

func (s *sessionState) snapshotValue() protocol.ConfigPush {
	s.snapshotMu.RLock()
	defer s.snapshotMu.RUnlock()
	return s.snapshot
}

func (c *Client) readLoop(ctx context.Context, conn net.Conn, state *sessionState) error {
	for {
		frame, err := c.readMessage(conn, state.readTimeout)
		if err != nil {
			return err
		}

		switch frame.Type {
		case protocol.TypeHeartbeatPong:
			if _, err := protocol.UnmarshalHeartbeatPong(frame.Body); err != nil {
				return err
			}
		case protocol.TypeConfigPush:
			if err := c.applyConfigPush(conn, state, frame); err != nil {
				return err
			}
		case protocol.TypeStreamOpen:
			if err := c.handleStreamOpen(conn, state, frame); err != nil {
				return err
			}
		case protocol.TypeStreamData:
			if err := c.handleStreamData(conn, state, frame); err != nil {
				return err
			}
		case protocol.TypeStreamClose:
			if err := c.handleStreamClose(state, frame); err != nil {
				return err
			}
		case protocol.TypeUDPOpen:
			if err := c.handleUDPOpen(conn, state, frame); err != nil {
				return err
			}
		case protocol.TypeUDPData:
			if err := c.handleUDPData(conn, state, frame); err != nil {
				return err
			}
		case protocol.TypeUDPClose:
			if err := c.handleUDPClose(state, frame); err != nil {
				return err
			}
		case protocol.TypeError:
			return c.remoteError(frame)
		default:
			return fmt.Errorf("unsupported server message type %s", frame.Type.String())
		}

		select {
		case <-ctx.Done():
			return nil
		default:
		}
	}
}

func (c *Client) heartbeatLoop(ctx context.Context, conn net.Conn, state *sessionState) error {
	ticker := time.NewTicker(state.heartbeatInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			body, err := protocol.MarshalHeartbeatPing(protocol.HeartbeatPing{
				ClientUnixMs:           uint64(time.Now().UTC().UnixMilli()),
				ActiveStreams:          state.activeStreams.Load(),
				ActiveUDPSessions:      state.activeUDPSessions.Load(),
				LastAckedConfigVersion: state.lastAckedConfigVersion.Load(),
			})
			if err != nil {
				return err
			}
			if err := c.writeMessage(conn, &state.writeMu, protocol.Frame{
				Type:      protocol.TypeHeartbeatPing,
				RequestID: state.nextClientRequestID(),
				Body:      body,
			}); err != nil {
				return err
			}
		}
	}
}
