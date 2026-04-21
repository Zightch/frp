package client

import (
	"context"
	"fmt"
	"net"
	"net/netip"
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

type configReloadSummary struct {
	addedTunnels      int
	removedTunnels    int
	replacedTunnels   int
	unchangedTunnels  int
	closedStreams     int
	closedUDPSessions int
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

func (s *sessionState) applyReloadedSnapshot(snapshot protocol.ConfigPush) configReloadSummary {
	previous := s.snapshotValue()
	summary := summarizeConfigReload(previous, snapshot)
	summary.closedStreams = s.closeAllStreams()
	summary.closedUDPSessions = s.closeAllUDPSessions()
	s.setSnapshot(snapshot)
	return summary
}

func summarizeConfigReload(previous, next protocol.ConfigPush) configReloadSummary {
	summary := configReloadSummary{}
	previousByID := make(map[uint32]protocol.TunnelEntry, len(previous.Tunnels))
	for _, tunnel := range previous.Tunnels {
		previousByID[tunnel.TunnelID] = tunnel
	}

	for _, tunnel := range next.Tunnels {
		previousTunnel, ok := previousByID[tunnel.TunnelID]
		if !ok {
			summary.addedTunnels++
			continue
		}
		if sameTunnelExecution(previousTunnel, tunnel) {
			summary.unchangedTunnels++
		} else {
			summary.replacedTunnels++
		}
		delete(previousByID, tunnel.TunnelID)
	}

	summary.removedTunnels = len(previousByID)
	return summary
}

func sameTunnelExecution(left, right protocol.TunnelEntry) bool {
	return left.Protocol == right.Protocol &&
		left.TunnelFlags == right.TunnelFlags &&
		left.RemoteStart == right.RemoteStart &&
		left.RemoteEnd == right.RemoteEnd &&
		left.LocalStart == right.LocalStart &&
		left.LocalEnd == right.LocalEnd &&
		sameHost(left.LocalHost, right.LocalHost)
}

func sameHost(left, right protocol.Host) bool {
	if left.Type != right.Type || left.Name != right.Name {
		return false
	}

	leftAddr, leftOK := hostAddr(left)
	rightAddr, rightOK := hostAddr(right)
	if leftOK != rightOK {
		return false
	}
	if !leftOK {
		return true
	}
	return leftAddr == rightAddr
}

func hostAddr(host protocol.Host) (netip.Addr, bool) {
	if len(host.IP) == 0 {
		return netip.Addr{}, false
	}

	addr, ok := netip.AddrFromSlice(net.IP(host.IP))
	if !ok {
		return netip.Addr{}, false
	}
	return addr.Unmap(), true
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
