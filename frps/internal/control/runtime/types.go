package runtime

import (
	"net"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	controlbind "github.com/zightch/frp/frps/internal/control/bind"
	controlsession "github.com/zightch/frp/frps/internal/control/session"
	"github.com/zightch/frp/frps/pkg/protocol"
	"github.com/zightch/frp/frps/pkg/testsupport"
)

// InitialServerRequestID is the initial server request ID constant.
const InitialServerRequestID = uint32(1 << 31)

// ConfigPushOperation represents a pending config push operation.
type ConfigPushOperation struct {
	RequestID    uint32
	Group        GroupRuntime
	Snapshot     ConfigSnapshot
	RecoveryMode testsupport.RecoveryMode
}

// ConfigApplyResult represents the result of applying a config.
type ConfigApplyResult struct {
	Group        GroupRuntime
	Snapshot     ConfigSnapshot
	RecoveryMode testsupport.RecoveryMode
}

// ListenerState holds the listener state for a session.
type ListenerState struct {
	TCP     map[uint32][]net.Listener
	UDP     map[uint32][]controlbind.UDPListener
	Started bool
}

// UDPState holds UDP session state.
type UDPState struct {
	Sessions       map[uint32]*UDPSession
	Keys           map[string]uint32
	CleanupStarted bool
}

// RuntimeState holds the runtime state for a session.
type RuntimeState struct {
	Done       chan struct{}
	Frozen     bool
	Generation uint64
	Listeners  ListenerState
	Streams    map[uint32]*Stream
	UDP        UDPState
}

// ConcreteSessionState is the concrete session state type.
type ConcreteSessionState struct {
	ID                  uint64
	NextServerRequestID atomic.Uint32
	NextStreamID        atomic.Uint32
	ReadTimeout         time.Duration

	ControlMu sync.Mutex
	Control   controlsession.SessionState
	Group     GroupRuntime
	Pending   GroupRuntime
	Recovery  testsupport.RecoveryMode

	WriteMu     sync.Mutex
	RuntimeMu   sync.Mutex
	RuntimeIOMu sync.RWMutex
	Runtime     RuntimeState

	ShutdownOnce sync.Once
}

// ObservedConfigState represents an observed session config state.
type ObservedConfigState struct {
	State        controlsession.SessionState
	Group        GroupRuntime
	PendingGroup GroupRuntime
	RecoveryMode testsupport.RecoveryMode
}

// Stream represents a public TCP stream connection.
type Stream struct {
	ConfigVersion    uint64
	Conn             net.Conn
	Tunnel           protocol.TunnelEntry
	RemotePort       uint16
	ClientAddr       protocol.SockAddr
	OpenedAtMs       uint64
	OpenRequestID    uint32
	LastActiveUnixMs atomic.Int64
	Ready            chan error
	ReadyOnce        sync.Once
	CloseOnce        sync.Once
}

// UDPSession represents a public UDP session.
type UDPSession struct {
	SessionID        uint32
	TunnelID         uint32
	RemotePort       uint16
	ClientAddr       protocol.SockAddr
	PublicAddr       *net.UDPAddr
	Listener         controlbind.UDPListener
	OpenedAtMs       uint64
	IdleTimeout      time.Duration
	LastActiveUnixMs atomic.Int64
}

// SignalReady signals that the stream is ready. It is safe to call multiple times.
func (s *Stream) SignalReady(err error) {
	if s == nil {
		return
	}
	s.ReadyOnce.Do(func() {
		s.Ready <- err
	})
}

// Touch updates the last active time to now.
func (s *Stream) Touch(now time.Time) {
	if s == nil {
		return
	}
	s.LastActiveUnixMs.Store(now.UTC().UnixMilli())
}

// Close closes the stream connection. It is safe to call multiple times.
func (s *Stream) Close() {
	if s == nil {
		return
	}
	s.CloseOnce.Do(func() {
		_ = s.Conn.Close()
	})
}

// ObservedConnection returns an observed connection for the stream.
func (s *Stream) ObservedConnection(streamID uint32) ObservedConnection {
	if s == nil {
		return ObservedConnection{}
	}
	return ObservedConnection{
		ConnectionID:   streamID,
		Kind:           "tcp_stream",
		Protocol:       "tcp",
		TunnelID:       s.Tunnel.TunnelID,
		RemotePort:     s.RemotePort,
		ClientAddr:     SockAddrString(s.ClientAddr),
		OpenedAtMs:     s.OpenedAtMs,
		LastActiveAtMs: NonNegativeUnixMilli(s.LastActiveUnixMs.Load()),
	}
}

// Touch updates the last active time for the UDP session.
func (s *UDPSession) Touch(now time.Time) {
	if s == nil {
		return
	}
	s.LastActiveUnixMs.Store(now.UTC().UnixMilli())
}

// Key returns a unique key for the UDP session based on tunnelID, remotePort, and clientAddr.
func (s *UDPSession) Key() string {
	if s == nil {
		return ""
	}
	ip := s.ClientAddr.IP
	if ip4 := ip.To4(); ip4 != nil {
		ip = ip4
	} else {
		ip = ip.To16()
	}
	return strconv.FormatUint(uint64(s.TunnelID), 10) +
		"|" + strconv.FormatUint(uint64(s.RemotePort), 10) +
		"|" + ip.String() +
		"|" + strconv.FormatUint(uint64(s.ClientAddr.Port), 10)
}

// ObservedConnection returns an observed connection for the UDP session.
func (s *UDPSession) ObservedConnection() ObservedConnection {
	if s == nil {
		return ObservedConnection{}
	}
	return ObservedConnection{
		ConnectionID:   s.SessionID,
		Kind:           "udp_session",
		Protocol:       "udp",
		TunnelID:       s.TunnelID,
		RemotePort:     s.RemotePort,
		ClientAddr:     SockAddrString(s.ClientAddr),
		OpenedAtMs:     s.OpenedAtMs,
		LastActiveAtMs: NonNegativeUnixMilli(s.LastActiveUnixMs.Load()),
		IdleTimeoutMs:  uint32(s.IdleTimeout / time.Millisecond),
	}
}

// SockAddrString returns the string representation of a SockAddr.
func SockAddrString(addr protocol.SockAddr) string {
	if addr.IP == nil {
		return ""
	}
	return net.JoinHostPort(addr.IP.String(), strconv.Itoa(int(addr.Port)))
}

// NonNegativeUnixMilli returns the unix milliseconds, or 0 if negative.
func NonNegativeUnixMilli(ms int64) uint64 {
	if ms < 0 {
		return 0
	}
	return uint64(ms)
}

// DoneCh returns the done channel for the session.
func (s *ConcreteSessionState) DoneCh() <-chan struct{} {
	if s == nil {
		return nil
	}
	return s.Runtime.Done
}

// CloseDone closes the done channel. It is safe to call multiple times.
func (s *ConcreteSessionState) CloseDone() {
	if s == nil || s.Runtime.Done == nil {
		return
	}
	s.ShutdownOnce.Do(func() {
		close(s.Runtime.Done)
	})
}

// IsDone returns true if the session is done.
func (s *ConcreteSessionState) IsDone() bool {
	if s == nil || s.Runtime.Done == nil {
		return false
	}
	select {
	case <-s.Runtime.Done:
		return true
	default:
		return false
	}
}

// NextRequestID generates and returns the next request ID.
func (s *ConcreteSessionState) NextRequestID() uint32 {
	requestID := s.NextServerRequestID.Add(1)
	if requestID == 0 {
		s.NextServerRequestID.Store(InitialServerRequestID - 1)
		requestID = s.NextServerRequestID.Add(1)
	}
	return requestID
}

// NextTunnelStreamID generates and returns the next tunnel stream ID.
func (s *ConcreteSessionState) NextTunnelStreamID() uint32 {
	streamID := s.NextStreamID.Add(1)
	if streamID == 0 {
		streamID = s.NextStreamID.Add(1)
	}
	return streamID
}

// HasRuntimeListenersLocked returns true if there are any runtime listeners.
// Must be called with RuntimeMu held.
func (s *ConcreteSessionState) HasRuntimeListenersLocked() bool {
	for _, listeners := range s.Runtime.Listeners.TCP {
		if len(listeners) > 0 {
			return true
		}
	}
	for _, listeners := range s.Runtime.Listeners.UDP {
		if len(listeners) > 0 {
			return true
		}
	}
	return false
}

// ActiveRuntimeTunnelIDs returns the set of active tunnel IDs.
func (s *ConcreteSessionState) ActiveRuntimeTunnelIDs() map[uint32]struct{} {
	s.RuntimeMu.Lock()
	defer s.RuntimeMu.Unlock()
	if s.Runtime.Frozen {
		return nil
	}
	return s.ActiveRuntimeTunnelIDsLocked()
}

// ActiveRuntimeTunnelIDsLocked returns the set of active tunnel IDs.
// Must be called with RuntimeMu held.
func (s *ConcreteSessionState) ActiveRuntimeTunnelIDsLocked() map[uint32]struct{} {
	active := make(map[uint32]struct{})
	for tunnelID, listeners := range s.Runtime.Listeners.TCP {
		if len(listeners) == 0 {
			continue
		}
		active[tunnelID] = struct{}{}
	}
	for tunnelID, listeners := range s.Runtime.Listeners.UDP {
		if len(listeners) == 0 {
			continue
		}
		active[tunnelID] = struct{}{}
	}
	if len(active) == 0 {
		return nil
	}
	return active
}

// HasActiveRuntimeListeners returns true if there are active runtime listeners.
func (s *ConcreteSessionState) HasActiveRuntimeListeners() bool {
	s.RuntimeMu.Lock()
	defer s.RuntimeMu.Unlock()
	if s.Runtime.Frozen {
		return false
	}
	return s.HasRuntimeListenersLocked()
}

// CanStartTunnelRuntime returns true if tunnel runtime can be started.
func (s *ConcreteSessionState) CanStartTunnelRuntime() bool {
	s.RuntimeMu.Lock()
	defer s.RuntimeMu.Unlock()
	return !s.Runtime.Frozen
}

// CurrentGroupID returns the current group ID.
func (s *ConcreteSessionState) CurrentGroupID() int64 {
	s.ControlMu.Lock()
	defer s.ControlMu.Unlock()
	return s.Group.ID
}

// CurrentGroup returns the current group runtime.
func (s *ConcreteSessionState) CurrentGroup() GroupRuntime {
	s.ControlMu.Lock()
	defer s.ControlMu.Unlock()
	return s.Group
}

// CurrentGroupAndSnapshot returns the current group and snapshot.
func (s *ConcreteSessionState) CurrentGroupAndSnapshot() (GroupRuntime, ConfigSnapshot) {
	s.ControlMu.Lock()
	defer s.ControlMu.Unlock()
	return s.Group, s.Group.Snapshot
}

// CurrentSnapshot returns the current config snapshot.
func (s *ConcreteSessionState) CurrentSnapshot() ConfigSnapshot {
	s.ControlMu.Lock()
	defer s.ControlMu.Unlock()
	return s.Group.Snapshot
}

// ReplaceGroupRuntime replaces the current group runtime.
func (s *ConcreteSessionState) ReplaceGroupRuntime(group GroupRuntime) {
	s.ControlMu.Lock()
	defer s.ControlMu.Unlock()
	s.Group = group
}

// SetRecoveryMode sets the recovery mode.
func (s *ConcreteSessionState) SetRecoveryMode(mode testsupport.RecoveryMode) {
	s.ControlMu.Lock()
	defer s.ControlMu.Unlock()
	s.Recovery = mode
}

// RecoveryModeValue returns the current recovery mode.
func (s *ConcreteSessionState) RecoveryModeValue() testsupport.RecoveryMode {
	s.ControlMu.Lock()
	defer s.ControlMu.Unlock()
	return s.Recovery
}

// ConfigAckState returns the pending request ID and expected config version.
func (s *ConcreteSessionState) ConfigAckState() (uint32, uint64) {
	s.ControlMu.Lock()
	defer s.ControlMu.Unlock()
	if s.Control.Pending == nil {
		return 0, s.Group.Snapshot.Version
	}
	return s.Control.Pending.RequestID, s.Control.Pending.Snapshot.Version
}

// LastAckedConfigVersion returns the last acknowledged config version.
func (s *ConcreteSessionState) LastAckedConfigVersion() uint64 {
	s.ControlMu.Lock()
	defer s.ControlMu.Unlock()
	if s.Control.Applied == nil {
		return 0
	}
	return s.Control.Applied.Snapshot.Version
}

// HasPendingConfig returns true if there is a pending config push.
func (s *ConcreteSessionState) HasPendingConfig() bool {
	s.ControlMu.Lock()
	defer s.ControlMu.Unlock()
	return s.Control.Pending != nil
}

// RefreshPendingConfig refreshes the pending group if the snapshot matches.
func (s *ConcreteSessionState) RefreshPendingConfig(group GroupRuntime, snapshot ConfigSnapshot) bool {
	s.ControlMu.Lock()
	defer s.ControlMu.Unlock()
	if s.Control.Pending == nil {
		return false
	}
	if !SamePushedConfigSnapshot(ConfigSnapshotFromDesired(s.Control.Pending.Snapshot), snapshot) {
		return false
	}
	group.Snapshot = snapshot
	s.Pending = group
	return true
}

// NewConcreteSessionState creates a new concrete session state.
func NewConcreteSessionState(id uint64, group GroupRuntime, snapshot ConfigSnapshot, readTimeout time.Duration) *ConcreteSessionState {
	session := &ConcreteSessionState{
		ID:          id,
		ReadTimeout: readTimeout,
		Group:       group,
	}
	session.Group.Snapshot = snapshot
	session.Control = controlsession.NewState(group.ID, id)
	desiredGroup := group
	desiredGroup.Snapshot = snapshot
	desired := DesiredRuntimeFromGroup(desiredGroup)
	session.Control.Desired = &desired
	session.Runtime.Done = make(chan struct{})
	session.Runtime.Listeners.TCP = make(map[uint32][]net.Listener)
	session.Runtime.Listeners.UDP = make(map[uint32][]controlbind.UDPListener)
	session.Runtime.Streams = make(map[uint32]*Stream)
	session.Runtime.UDP.Sessions = make(map[uint32]*UDPSession)
	session.Runtime.UDP.Keys = make(map[string]uint32)
	session.NextServerRequestID.Store(InitialServerRequestID - 1)
	return session
}
