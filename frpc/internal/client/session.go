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
	"github.com/zightch/frp/frps/pkg/testsupport"
)

type sessionState struct {
	heartbeatInterval time.Duration
	readTimeout       time.Duration

	writeMu sync.Mutex
	tcpWork *tcpWorkPoolState

	nextRequestID          atomic.Uint32
	lastAckedConfigVersion atomic.Uint64
	activeStreams          atomic.Uint32
	activeUDPSessions      atomic.Uint32
	sessionID              uint64
	connID                 string
	recoveryMode           testsupport.RecoveryMode
	workPoolTarget         uint16
	workSecret             [32]byte

	snapshotMu sync.RWMutex
	snapshot   protocol.ConfigPush
	lastReload configReloadSummary

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
		tcpWork:           newTCPWorkPoolState(),
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
	s.snapshotMu.Lock()
	s.lastReload = summary
	s.snapshot = snapshot
	s.snapshotMu.Unlock()
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
	return left.TunnelName == right.TunnelName &&
		left.Protocol == right.Protocol &&
		left.TunnelFlags == right.TunnelFlags &&
		left.ListenTLSMode == right.ListenTLSMode &&
		left.RemoteStart == right.RemoteStart &&
		left.RemoteEnd == right.RemoteEnd &&
		left.BackendTLSMode == right.BackendTLSMode &&
		left.BackendTLSLoadSystemCA == right.BackendTLSLoadSystemCA &&
		left.BackendTLSInsecureSkipVerify == right.BackendTLSInsecureSkipVerify &&
		left.BackendTLSServerName == right.BackendTLSServerName &&
		left.BackendTLSCAPEM == right.BackendTLSCAPEM &&
		left.BackendTLSClientCertPEM == right.BackendTLSClientCertPEM &&
		left.BackendTLSClientKeyPEM == right.BackendTLSClientKeyPEM &&
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
		frame, err := c.readMessageWithState(conn, state, state.readTimeout)
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
			if err := c.writeMessageWithState(conn, state, &state.writeMu, protocol.Frame{
				Type:      protocol.TypeHeartbeatPing,
				RequestID: state.nextClientRequestID(),
				Body:      body,
			}); err != nil {
				return err
			}
		}
	}
}

func (s *sessionState) setIdentity(sessionID uint64, connID string) {
	s.snapshotMu.Lock()
	defer s.snapshotMu.Unlock()
	s.sessionID = sessionID
	s.connID = connID
}

func (s *sessionState) setTCPWorkConfig(poolTarget uint16, secret [32]byte) {
	s.snapshotMu.Lock()
	defer s.snapshotMu.Unlock()
	s.workPoolTarget = poolTarget
	s.workSecret = secret
}

func (s *sessionState) tcpWorkConfig() (uint64, uint16, [32]byte) {
	s.snapshotMu.RLock()
	defer s.snapshotMu.RUnlock()
	return s.sessionID, s.workPoolTarget, s.workSecret
}

func (s *sessionState) tcpWorkPoolTargetValue() uint16 {
	s.snapshotMu.RLock()
	defer s.snapshotMu.RUnlock()
	return s.workPoolTarget
}

func (s *sessionState) registerTCPWorkConn(conn net.Conn) error {
	if s == nil || s.tcpWork == nil {
		return errTCPWorkPoolClosed
	}
	return s.tcpWork.Register(conn)
}

func (s *sessionState) removeTCPWorkConn(conn net.Conn) bool {
	if s == nil || s.tcpWork == nil {
		return false
	}
	return s.tcpWork.Remove(conn)
}

func (s *sessionState) markTCPWorkConnBusy(conn net.Conn) bool {
	if s == nil || s.tcpWork == nil {
		return false
	}
	return s.tcpWork.MarkBusy(conn)
}

func (s *sessionState) tcpWorkConnCount() int {
	if s == nil || s.tcpWork == nil {
		return 0
	}
	return s.tcpWork.IdleCount()
}

func (s *sessionState) closeAllTCPWorkConns() {
	if s == nil || s.tcpWork == nil {
		return
	}
	s.tcpWork.CloseAll()
}

func (s *sessionState) setRecoveryMode(mode testsupport.RecoveryMode) {
	s.snapshotMu.Lock()
	defer s.snapshotMu.Unlock()
	s.recoveryMode = mode
}

func (s *sessionState) observeState(attempt uint64) testsupport.ClientObservedState {
	s.snapshotMu.RLock()
	defer s.snapshotMu.RUnlock()
	return testsupport.ClientObservedState{
		Attempt:                attempt,
		ConnID:                 s.connID,
		SessionID:              s.sessionID,
		SnapshotVersion:        s.snapshot.ConfigVersion,
		SnapshotGeneratedAtMs:  s.snapshot.GeneratedAtMs,
		SnapshotTunnelCount:    len(s.snapshot.Tunnels),
		LastAckedConfigVersion: s.lastAckedConfigVersion.Load(),
		ActiveStreams:          s.activeStreams.Load(),
		ActiveUDPSessions:      s.activeUDPSessions.Load(),
		RecoveryMode:           s.recoveryMode,
		LastReload: testsupport.ReloadSummaryObservedState{
			AddedTunnels:      s.lastReload.addedTunnels,
			RemovedTunnels:    s.lastReload.removedTunnels,
			ReplacedTunnels:   s.lastReload.replacedTunnels,
			UnchangedTunnels:  s.lastReload.unchangedTunnels,
			ClosedStreams:     s.lastReload.closedStreams,
			ClosedUDPSessions: s.lastReload.closedUDPSessions,
		},
	}
}
