package state

import (
	"errors"
	"net"
	"sort"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	controlbind "github.com/zightch/frp/frps/internal/control/bind"
	controldomainruntime "github.com/zightch/frp/frps/internal/control/domain/runtime"
	controlconfigsync "github.com/zightch/frp/frps/internal/control/protocol/configsync"
	controlsession "github.com/zightch/frp/frps/internal/control/session"
	"github.com/zightch/frp/frps/pkg/protocol"
	"github.com/zightch/frp/frps/pkg/testsupport"
	"github.com/zightch/frp/frps/pkg/transport"
)

const InitialServerRequestID = uint32(1 << 31)

type GroupRuntime = controldomainruntime.GroupRuntime
type ConfigSnapshot = controldomainruntime.ConfigSnapshot

var (
	ErrConfigUpdateInFlight  = errors.New("config update already in flight")
	ErrUnexpectedConfigAck   = controlconfigsync.ErrUnexpectedAck
	ErrConfigVersionMismatch = controlconfigsync.ErrVersionMismatch
)

type ConfigPushOperation struct {
	RequestID    uint32
	Group        GroupRuntime
	Snapshot     ConfigSnapshot
	RecoveryMode testsupport.RecoveryMode
}

type ConfigApplyResult struct {
	Group        GroupRuntime
	Snapshot     ConfigSnapshot
	RecoveryMode testsupport.RecoveryMode
}

type ListenerState struct {
	TCP     map[uint32][]net.Listener
	UDP     map[uint32][]controlbind.UDPListener
	Started bool
}

type UDPState struct {
	Sessions       map[uint32]*UDPSession
	Keys           map[string]uint32
	CleanupStarted bool
}

type RuntimeState struct {
	Done       chan struct{}
	Frozen     bool
	Generation uint64
	Listeners  ListenerState
	Streams    map[uint32]*Stream
	UDP        UDPState
}

type ConcreteSessionState struct {
	ID                  uint64
	NextServerRequestID atomic.Uint32
	NextStreamID        atomic.Uint32
	ReadTimeout         time.Duration

	ControlMu    sync.Mutex
	Control      controlsession.SessionState
	DesiredGroup GroupRuntime
	Group        GroupRuntime
	Pending      GroupRuntime
	Recovery     testsupport.RecoveryMode

	WriteMu     sync.Mutex
	RuntimeMu   sync.Mutex
	RuntimeIOMu sync.RWMutex
	Runtime     RuntimeState

	ShutdownOnce sync.Once
}

type ObservedConfigState struct {
	State        controlsession.SessionState
	Group        GroupRuntime
	PendingGroup GroupRuntime
	RecoveryMode testsupport.RecoveryMode
}

type RuntimePendingConfig struct {
	RequestID   uint32
	Snapshot    ConfigSnapshot
	EffectiveIP string
}

type RuntimeObservedConfig struct {
	EffectiveIP            string
	Snapshot               ConfigSnapshot
	LastAckedConfigVersion uint64
	Pending                *RuntimePendingConfig
}

type ObservedListener struct {
	TunnelID uint32
	Protocol string
	BindIP   string
	Port     uint16
}

type ObservedConnection struct {
	ConnectionID   uint32
	Kind           string
	Protocol       string
	TunnelID       uint32
	RemotePort     uint16
	ClientAddr     string
	OpenedAtMs     uint64
	LastActiveAtMs uint64
	IdleTimeoutMs  uint32
}

type ObservedState struct {
	Frozen                bool
	ListenersStarted      bool
	Generation            uint64
	ActiveTunnelIDs       map[uint32]struct{}
	AttachedListeners     []ObservedListener
	ActiveStreamCount     uint32
	ActiveUDPSessionCount uint32
	Connections           []ObservedConnection
}

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

func mergeObservedGroupRuntime(current GroupRuntime, desired GroupRuntime, snapshot ConfigSnapshot, effectiveIP string) GroupRuntime {
	group := current
	if desired.ID != 0 {
		group = desired
	}
	if group.ID == 0 {
		group = current
	}
	group.Snapshot = snapshot
	if effectiveIP != "" {
		group.EffectiveIP = effectiveIP
	}
	return group
}

func shouldMirrorObservedGroup(current GroupRuntime, next GroupRuntime) bool {
	return current.EffectiveIP == next.EffectiveIP &&
		controldomainruntime.SamePushedConfigSnapshot(current.Snapshot, next.Snapshot)
}

func projectSessionBindings(snapshot controlsession.DesiredRuntimeSnapshot, activeTunnelIDs map[uint32]struct{}) map[controlsession.BindingKey]controlsession.BindingState {
	bindings := make(map[controlsession.BindingKey]controlsession.BindingState)
	for _, tunnel := range snapshot.Tunnels {
		if !tunnel.Enabled {
			continue
		}

		phase := controlsession.BindingPhaseClosed
		if _, ok := activeTunnelIDs[tunnel.TunnelID]; ok {
			phase = controlsession.BindingPhaseActive
		}

		for port := tunnel.RemoteStart; port <= tunnel.RemoteEnd; port++ {
			key := controlsession.BindingKey{
				Protocol:    tunnel.Protocol,
				EffectiveIP: snapshot.EffectiveIP,
				Port:        port,
			}
			bindings[key] = controlsession.BindingState{
				Key:   key,
				Phase: phase,
			}
			if port == tunnel.RemoteEnd {
				break
			}
		}
	}
	return bindings
}

func projectedRuntimePhase(snapshot controlsession.DesiredRuntimeSnapshot, bindings map[controlsession.BindingKey]controlsession.BindingState, runtime ObservedState) controlsession.RuntimePhase {
	if !controldomainruntime.DesiredSnapshotHasEnabledTunnels(snapshot) {
		return controlsession.RuntimePhaseEmpty
	}
	if runtime.Frozen {
		return controlsession.RuntimePhaseBlocked
	}
	if len(bindings) == 0 {
		return controlsession.RuntimePhaseBinding
	}
	for _, binding := range bindings {
		if binding.Phase != controlsession.BindingPhaseActive {
			if runtime.ListenersStarted {
				return controlsession.RuntimePhaseRecovering
			}
			return controlsession.RuntimePhaseBinding
		}
	}
	return controlsession.RuntimePhaseActive
}

func (s *Stream) SignalReady(err error) {
	if s == nil {
		return
	}
	s.ReadyOnce.Do(func() {
		s.Ready <- err
	})
}

func (s *Stream) Touch(now time.Time) {
	if s == nil {
		return
	}
	s.LastActiveUnixMs.Store(now.UTC().UnixMilli())
}

func (s *Stream) Close() {
	if s == nil {
		return
	}
	s.CloseOnce.Do(func() {
		_ = s.Conn.Close()
	})
}

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

func (s *UDPSession) Touch(now time.Time) {
	if s == nil {
		return
	}
	s.LastActiveUnixMs.Store(now.UTC().UnixMilli())
}

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

func SockAddrString(addr protocol.SockAddr) string {
	if addr.IP == nil {
		return ""
	}
	return net.JoinHostPort(addr.IP.String(), strconv.Itoa(int(addr.Port)))
}

func NonNegativeUnixMilli(ms int64) uint64 {
	if ms < 0 {
		return 0
	}
	return uint64(ms)
}

func ListenerAddr(addr net.Addr) (string, uint16) {
	if addr == nil {
		return "", 0
	}
	host, portText, err := net.SplitHostPort(addr.String())
	if err != nil {
		return addr.String(), 0
	}
	port, _ := strconv.Atoi(portText)
	return host, uint16(port)
}

func ObserveRuntimeListeners(tcp map[uint32][]net.Listener, udp map[uint32][]controlbind.UDPListener) []ObservedListener {
	listeners := make([]ObservedListener, 0, len(tcp)+len(udp))
	for tunnelID, tunnelListeners := range tcp {
		for _, listener := range tunnelListeners {
			bindIP, port := ListenerAddr(listener.Addr())
			listeners = append(listeners, ObservedListener{
				TunnelID: tunnelID,
				Protocol: "tcp",
				BindIP:   bindIP,
				Port:     port,
			})
		}
	}
	for tunnelID, tunnelListeners := range udp {
		for _, listener := range tunnelListeners {
			bindIP, port := ListenerAddr(listener.LocalAddr())
			listeners = append(listeners, ObservedListener{
				TunnelID: tunnelID,
				Protocol: "udp",
				BindIP:   bindIP,
				Port:     port,
			})
		}
	}
	return listeners
}

func observeRuntimeConnections(streams map[uint32]*Stream, udpSessions map[uint32]*UDPSession) []ObservedConnection {
	connections := make([]ObservedConnection, 0, len(streams)+len(udpSessions))
	for streamID, stream := range streams {
		if stream == nil {
			continue
		}
		connections = append(connections, stream.ObservedConnection(streamID))
	}
	for _, udpSession := range udpSessions {
		if udpSession == nil {
			continue
		}
		connections = append(connections, udpSession.ObservedConnection())
	}

	sort.Slice(connections, func(i, j int) bool {
		if connections[i].Kind == connections[j].Kind {
			if connections[i].TunnelID == connections[j].TunnelID {
				return connections[i].ConnectionID < connections[j].ConnectionID
			}
			return connections[i].TunnelID < connections[j].TunnelID
		}
		return connections[i].Kind < connections[j].Kind
	})

	return connections
}

func (s *ConcreteSessionState) DoneCh() <-chan struct{} {
	if s == nil {
		return nil
	}
	return s.Runtime.Done
}

func (s *ConcreteSessionState) CloseDone() {
	if s == nil || s.Runtime.Done == nil {
		return
	}
	s.ShutdownOnce.Do(func() {
		close(s.Runtime.Done)
	})
}

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

func (s *ConcreteSessionState) SessionID() uint64 {
	if s == nil {
		return 0
	}
	return s.ID
}

func (s *ConcreteSessionState) ControlState() controlsession.SessionState {
	if s == nil {
		return controlsession.SessionState{}
	}
	s.ControlMu.Lock()
	defer s.ControlMu.Unlock()
	return controlsession.Clone(s.Control)
}

func (s *ConcreteSessionState) syncControlStateLocked(state controlsession.SessionState) {
	s.Control = controlsession.Clone(state)

	desiredGroup := s.DesiredGroup
	currentGroup := s.Group
	if currentGroup.ID == 0 && desiredGroup.ID != 0 {
		currentGroup = desiredGroup
	}
	if state.Applied != nil {
		s.Group = mergeObservedGroupRuntime(
			currentGroup,
			desiredGroup,
			controldomainruntime.ConfigSnapshotFromDesired(state.Applied.Snapshot),
			state.Applied.Snapshot.EffectiveIP,
		)
	} else if state.Desired != nil {
		s.Group = mergeObservedGroupRuntime(
			currentGroup,
			desiredGroup,
			controldomainruntime.ConfigSnapshotFromDesired(*state.Desired),
			state.Desired.EffectiveIP,
		)
	}
	if s.Group.ID == 0 {
		s.Group = currentGroup
	}

	if state.Pending != nil {
		s.Pending = mergeObservedGroupRuntime(
			s.Group,
			desiredGroup,
			controldomainruntime.ConfigSnapshotFromDesired(state.Pending.Snapshot),
			state.Pending.Snapshot.EffectiveIP,
		)
		s.Recovery = controldomainruntime.PendingRecoveryModeForSnapshot(s.Pending.Snapshot)
		return
	}

	s.Pending = GroupRuntime{}
	s.Recovery = controldomainruntime.AppliedRecoveryModeForSnapshot(s.Group.Snapshot)
}

func (s *ConcreteSessionState) SyncControlState(state controlsession.SessionState) {
	if s == nil {
		return
	}
	s.ControlMu.Lock()
	defer s.ControlMu.Unlock()
	s.syncControlStateLocked(state)
}

func (s *ConcreteSessionState) SetControlState(state controlsession.SessionState) {
	s.SyncControlState(state)
}

func (s *ConcreteSessionState) ApplyControlEvent(event controlsession.Event) controlsession.SessionState {
	state := s.ControlState()
	next := controlsession.Advance(state, event)
	s.SyncControlState(next)
	return controlsession.Clone(next)
}

func (s *ConcreteSessionState) DesiredGroupRuntime() GroupRuntime {
	if s == nil {
		return GroupRuntime{}
	}
	s.ControlMu.Lock()
	defer s.ControlMu.Unlock()
	return s.DesiredGroup
}

func (s *ConcreteSessionState) SetDesiredGroupRuntime(group GroupRuntime) {
	if s == nil {
		return
	}
	s.ControlMu.Lock()
	defer s.ControlMu.Unlock()
	s.DesiredGroup = group
}

func (s *ConcreteSessionState) PrepareDesiredGroupUpdate(group GroupRuntime) controlsession.DesiredRuntimeUpdated {
	if s == nil {
		return controlsession.DesiredRuntimeUpdated{}
	}

	s.ControlMu.Lock()
	defer s.ControlMu.Unlock()

	s.DesiredGroup = group
	if shouldMirrorObservedGroup(s.Group, group) {
		s.Group = group
		if s.Control.Pending == nil {
			s.Recovery = controldomainruntime.AppliedRecoveryModeForSnapshot(s.Group.Snapshot)
		}
	}
	if s.Control.Pending != nil && controldomainruntime.SamePushedConfigSnapshot(
		controldomainruntime.ConfigSnapshotFromDesired(s.Control.Pending.Snapshot),
		group.Snapshot,
	) {
		s.Pending = group
		s.Recovery = controldomainruntime.PendingRecoveryModeForSnapshot(s.Pending.Snapshot)
	}

	return controlsession.DesiredRuntimeUpdated{
		Snapshot: controldomainruntime.DesiredRuntimeFromGroup(group),
	}
}

func (s *ConcreteSessionState) PrepareConfigPush(group GroupRuntime, snapshot ConfigSnapshot) (ConfigPushOperation, error) {
	state := s.ControlState()
	if state.Pending != nil {
		return ConfigPushOperation{}, ErrConfigUpdateInFlight
	}

	group.Snapshot = snapshot
	s.SetDesiredGroupRuntime(group)
	next := s.ApplyControlEvent(controlsession.DesiredRuntimeUpdated{
		Snapshot: controldomainruntime.DesiredRuntimeFromGroup(group),
	})
	if next.Pending == nil {
		return ConfigPushOperation{}, ErrConfigUpdateInFlight
	}

	recoveryMode := controldomainruntime.PendingRecoveryModeForSnapshot(snapshot)
	return ConfigPushOperation{
		RequestID:    next.Pending.RequestID,
		Group:        group,
		Snapshot:     snapshot,
		RecoveryMode: recoveryMode,
	}, nil
}

func (s *ConcreteSessionState) PreviewAcceptedConfig(requestID uint32, version uint64) (ConfigApplyResult, error) {
	state := s.ControlState()
	if state.Pending == nil || requestID != state.Pending.RequestID {
		return ConfigApplyResult{}, ErrUnexpectedConfigAck
	}
	if version != state.Pending.Snapshot.Version {
		return ConfigApplyResult{}, ErrConfigVersionMismatch
	}

	configState, _ := s.ObserveState()
	group := configState.PendingGroup
	if group.ID == 0 {
		group = configState.Group
		group.Snapshot = controldomainruntime.ConfigSnapshotFromDesired(state.Pending.Snapshot)
		group.EffectiveIP = state.Pending.Snapshot.EffectiveIP
	}
	return ConfigApplyResult{
		Group:        group,
		Snapshot:     group.Snapshot,
		RecoveryMode: configState.RecoveryMode,
	}, nil
}

func (s *ConcreteSessionState) AcceptConfigAck(requestID uint32, version uint64) (ConfigApplyResult, error) {
	if _, err := s.PreviewAcceptedConfig(requestID, version); err != nil {
		return ConfigApplyResult{}, err
	}
	s.ApplyControlEvent(controlsession.ConfigAckReceived{
		RequestID:     requestID,
		ConfigVersion: version,
	})

	configState, _ := s.ObserveState()
	return ConfigApplyResult{
		Group:        configState.Group,
		Snapshot:     configState.Group.Snapshot,
		RecoveryMode: configState.RecoveryMode,
	}, nil
}

func (s *ConcreteSessionState) NextRequestID() uint32 {
	requestID := s.NextServerRequestID.Add(1)
	if requestID == 0 {
		s.NextServerRequestID.Store(InitialServerRequestID - 1)
		requestID = s.NextServerRequestID.Add(1)
	}
	return requestID
}

func (s *ConcreteSessionState) NextTunnelStreamID() uint32 {
	streamID := s.NextStreamID.Add(1)
	if streamID == 0 {
		streamID = s.NextStreamID.Add(1)
	}
	return streamID
}

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

func (s *ConcreteSessionState) ResetRuntimeGenerationIfIdle() {
	s.RuntimeMu.Lock()
	defer s.RuntimeMu.Unlock()
	if s.HasRuntimeListenersLocked() {
		return
	}
	s.Runtime.Listeners.Started = false
	s.Runtime.Generation = 0
}

func (s *ConcreteSessionState) AttachTunnelListeners(configVersion uint64, tunnelID uint32, tcpListeners []net.Listener, udpListeners []controlbind.UDPListener) (bool, bool) {
	s.ControlMu.Lock()
	desiredVersion := s.DesiredGroup.Snapshot.Version
	if desiredVersion == 0 {
		desiredVersion = s.Group.Snapshot.Version
	}
	s.ControlMu.Unlock()

	s.RuntimeMu.Lock()
	defer s.RuntimeMu.Unlock()
	if s.Runtime.Frozen {
		return false, false
	}
	if desiredVersion != 0 && desiredVersion != configVersion {
		return false, false
	}
	if s.Runtime.Generation != 0 && s.Runtime.Generation != configVersion {
		return false, false
	}
	if len(s.Runtime.Listeners.TCP[tunnelID]) > 0 || len(s.Runtime.Listeners.UDP[tunnelID]) > 0 {
		return false, false
	}

	if len(tcpListeners) > 0 {
		s.Runtime.Listeners.TCP[tunnelID] = append(s.Runtime.Listeners.TCP[tunnelID], tcpListeners...)
	}
	if len(udpListeners) > 0 {
		s.Runtime.Listeners.UDP[tunnelID] = append(s.Runtime.Listeners.UDP[tunnelID], udpListeners...)
	}

	if s.HasRuntimeListenersLocked() {
		s.Runtime.Listeners.Started = true
		s.Runtime.Generation = configVersion
	} else {
		s.Runtime.Listeners.Started = false
		s.Runtime.Generation = 0
	}

	startUDPCleanup := len(udpListeners) > 0 && !s.Runtime.UDP.CleanupStarted
	if startUDPCleanup {
		s.Runtime.UDP.CleanupStarted = true
	}
	return startUDPCleanup, true
}

func (s *ConcreteSessionState) AddPublicStream(streamID uint32, stream *Stream, configVersion uint64) bool {
	s.RuntimeMu.Lock()
	defer s.RuntimeMu.Unlock()
	if s.Runtime.Frozen || !s.Runtime.Listeners.Started || s.Runtime.Generation != configVersion {
		return false
	}
	s.Runtime.Streams[streamID] = stream
	return true
}

func (s *ConcreteSessionState) CanServeRuntimeIO(configVersion uint64) bool {
	s.RuntimeMu.Lock()
	defer s.RuntimeMu.Unlock()
	return !s.Runtime.Frozen && s.Runtime.Listeners.Started && s.Runtime.Generation == configVersion
}

func (s *ConcreteSessionState) LockRuntimeIOWrite(configVersion uint64) bool {
	s.WriteMu.Lock()
	s.RuntimeIOMu.RLock()
	if !s.CanServeRuntimeIO(configVersion) {
		s.RuntimeIOMu.RUnlock()
		s.WriteMu.Unlock()
		return false
	}
	return true
}

func (s *ConcreteSessionState) UnlockRuntimeIOWrite() {
	s.RuntimeIOMu.RUnlock()
	s.WriteMu.Unlock()
}

func (s *ConcreteSessionState) PublicStream(streamID uint32) *Stream {
	s.RuntimeMu.Lock()
	defer s.RuntimeMu.Unlock()
	return s.Runtime.Streams[streamID]
}

func (s *ConcreteSessionState) ClosePublicStream(streamID uint32) bool {
	s.RuntimeMu.Lock()
	stream, ok := s.Runtime.Streams[streamID]
	if ok {
		delete(s.Runtime.Streams, streamID)
	}
	s.RuntimeMu.Unlock()
	if !ok {
		return false
	}

	stream.SignalReady(net.ErrClosed)
	stream.Close()
	return true
}

func (s *ConcreteSessionState) FreezeTunnelRuntime() ([]net.Listener, []controlbind.UDPListener, map[uint32]*Stream, []*UDPSession) {
	s.RuntimeIOMu.Lock()
	defer s.RuntimeIOMu.Unlock()
	s.RuntimeMu.Lock()
	defer s.RuntimeMu.Unlock()

	s.Runtime.Frozen = true
	s.Runtime.Listeners.Started = false
	s.Runtime.Generation = 0

	listeners := make([]net.Listener, 0, len(s.Runtime.Listeners.TCP))
	for tunnelID, tunnelListeners := range s.Runtime.Listeners.TCP {
		delete(s.Runtime.Listeners.TCP, tunnelID)
		listeners = append(listeners, tunnelListeners...)
	}
	udpListeners := make([]controlbind.UDPListener, 0, len(s.Runtime.Listeners.UDP))
	for tunnelID, tunnelListeners := range s.Runtime.Listeners.UDP {
		delete(s.Runtime.Listeners.UDP, tunnelID)
		udpListeners = append(udpListeners, tunnelListeners...)
	}
	streams := make(map[uint32]*Stream, len(s.Runtime.Streams))
	for streamID, stream := range s.Runtime.Streams {
		delete(s.Runtime.Streams, streamID)
		streams[streamID] = stream
	}
	udpSessions := make([]*UDPSession, 0, len(s.Runtime.UDP.Sessions))
	for sessionID, udpSession := range s.Runtime.UDP.Sessions {
		delete(s.Runtime.UDP.Sessions, sessionID)
		udpSessions = append(udpSessions, udpSession)
	}
	for key := range s.Runtime.UDP.Keys {
		delete(s.Runtime.UDP.Keys, key)
	}

	return listeners, udpListeners, streams, udpSessions
}

func (s *ConcreteSessionState) AllowTunnelRuntimeStart() {
	s.RuntimeMu.Lock()
	defer s.RuntimeMu.Unlock()
	s.Runtime.Frozen = false
}

func (s *ConcreteSessionState) BindPublicUDPSession(udpSession *UDPSession, configVersion uint64) (*UDPSession, bool) {
	s.RuntimeMu.Lock()
	defer s.RuntimeMu.Unlock()

	if s.Runtime.Frozen || !s.Runtime.Listeners.Started || s.Runtime.Generation != configVersion {
		return nil, false
	}

	key := udpSession.Key()
	if sessionID, exists := s.Runtime.UDP.Keys[key]; exists {
		if existing := s.Runtime.UDP.Sessions[sessionID]; existing != nil {
			return existing, false
		}
		delete(s.Runtime.UDP.Keys, key)
	}
	if _, exists := s.Runtime.UDP.Sessions[udpSession.SessionID]; exists {
		return s.Runtime.UDP.Sessions[udpSession.SessionID], false
	}
	s.Runtime.UDP.Sessions[udpSession.SessionID] = udpSession
	s.Runtime.UDP.Keys[key] = udpSession.SessionID
	return udpSession, true
}

func (s *ConcreteSessionState) PublicUDPSession(sessionID uint32) *UDPSession {
	s.RuntimeMu.Lock()
	defer s.RuntimeMu.Unlock()
	return s.Runtime.UDP.Sessions[sessionID]
}

func (s *ConcreteSessionState) ClosePublicUDPSession(sessionID uint32) bool {
	s.RuntimeMu.Lock()
	udpSession, ok := s.Runtime.UDP.Sessions[sessionID]
	if ok {
		delete(s.Runtime.UDP.Sessions, sessionID)
		delete(s.Runtime.UDP.Keys, udpSession.Key())
	}
	s.RuntimeMu.Unlock()
	return ok
}

func (s *ConcreteSessionState) TakeIdlePublicUDPSessions(now time.Time) []*UDPSession {
	s.RuntimeMu.Lock()
	defer s.RuntimeMu.Unlock()

	idleSessions := make([]*UDPSession, 0)
	for sessionID, udpSession := range s.Runtime.UDP.Sessions {
		lastActiveUnixMs := udpSession.LastActiveUnixMs.Load()
		if lastActiveUnixMs == 0 {
			continue
		}
		lastActive := time.UnixMilli(lastActiveUnixMs).UTC()
		if now.Before(lastActive) || now.Sub(lastActive) < udpSession.IdleTimeout {
			continue
		}
		delete(s.Runtime.UDP.Sessions, sessionID)
		delete(s.Runtime.UDP.Keys, udpSession.Key())
		idleSessions = append(idleSessions, udpSession)
	}
	return idleSessions
}

func (s *ConcreteSessionState) ObserveState() (ObservedConfigState, ObservedState) {
	s.ControlMu.Lock()
	configState := ObservedConfigState{
		State:        controlsession.Clone(s.Control),
		Group:        s.Group,
		PendingGroup: s.Pending,
		RecoveryMode: s.Recovery,
	}
	s.ControlMu.Unlock()

	s.RuntimeMu.Lock()
	runtimeConnections := observeRuntimeConnections(s.Runtime.Streams, s.Runtime.UDP.Sessions)
	runtimeState := ObservedState{
		Frozen:                s.Runtime.Frozen,
		ListenersStarted:      s.Runtime.Listeners.Started,
		Generation:            s.Runtime.Generation,
		ActiveTunnelIDs:       s.ActiveRuntimeTunnelIDsLocked(),
		AttachedListeners:     ObserveRuntimeListeners(s.Runtime.Listeners.TCP, s.Runtime.Listeners.UDP),
		ActiveStreamCount:     uint32(len(s.Runtime.Streams)),
		ActiveUDPSessionCount: uint32(len(s.Runtime.UDP.Sessions)),
		Connections:           runtimeConnections,
	}
	s.RuntimeMu.Unlock()

	return configState, runtimeState
}

func (s ObservedConfigState) RuntimeObservedConfig() RuntimeObservedConfig {
	config := RuntimeObservedConfig{
		EffectiveIP: s.Group.EffectiveIP,
		Snapshot:    s.Group.Snapshot,
	}

	if s.State.Applied != nil {
		config.LastAckedConfigVersion = s.State.Applied.Snapshot.Version
	}

	if s.State.Pending != nil {
		pending := s.PendingGroup
		if pending.ID == 0 {
			pending.Snapshot = controldomainruntime.ConfigSnapshotFromDesired(s.State.Pending.Snapshot)
			pending.EffectiveIP = s.State.Pending.Snapshot.EffectiveIP
		}
		config.Pending = &RuntimePendingConfig{
			RequestID:   s.State.Pending.RequestID,
			Snapshot:    pending.Snapshot,
			EffectiveIP: pending.EffectiveIP,
		}
	}

	return config
}

func (s *ConcreteSessionState) ProjectedSessionState(conn net.Conn) controlsession.SessionState {
	if s == nil {
		return controlsession.SessionState{}
	}

	configState, runtimeState := s.ObserveState()
	state := controlsession.Clone(configState.State)
	if state.GroupID == 0 {
		state = controlsession.NewState(configState.Group.ID, s.SessionID())
	}
	state.GroupID = configState.Group.ID
	state.SessionID = s.SessionID()
	state.Conn = controlsession.ControlConnState{
		Attached: conn != nil,
		ConnID:   transport.ConnectionID(conn),
	}
	if state.Phase == controlsession.SessionPhaseUnknown {
		state.Phase = controlsession.SessionPhaseOnline
	}

	if state.Desired == nil {
		desired := controldomainruntime.DesiredRuntimeFromSnapshot(configState.Group.EffectiveIP, configState.Group.Snapshot)
		state.Desired = &desired
	}
	desired := *state.Desired
	state.Desired = &desired
	state.Bindings = projectSessionBindings(desired, runtimeState.ActiveTunnelIDs)
	state.RuntimePhase = projectedRuntimePhase(desired, state.Bindings, runtimeState)

	return state
}

func (s *ConcreteSessionState) ActiveRuntimeTunnelIDs() map[uint32]struct{} {
	s.RuntimeMu.Lock()
	defer s.RuntimeMu.Unlock()
	if s.Runtime.Frozen {
		return nil
	}
	return s.ActiveRuntimeTunnelIDsLocked()
}

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

func (s *ConcreteSessionState) HasActiveRuntimeListeners() bool {
	s.RuntimeMu.Lock()
	defer s.RuntimeMu.Unlock()
	if s.Runtime.Frozen {
		return false
	}
	return s.HasRuntimeListenersLocked()
}

func (s *ConcreteSessionState) CanStartTunnelRuntime() bool {
	s.RuntimeMu.Lock()
	defer s.RuntimeMu.Unlock()
	return !s.Runtime.Frozen
}

func (s *ConcreteSessionState) CurrentGroupID() int64 {
	s.ControlMu.Lock()
	defer s.ControlMu.Unlock()
	return s.Group.ID
}

func (s *ConcreteSessionState) CurrentGroup() GroupRuntime {
	s.ControlMu.Lock()
	defer s.ControlMu.Unlock()
	return s.Group
}

func (s *ConcreteSessionState) CurrentGroupAndSnapshot() (GroupRuntime, ConfigSnapshot) {
	s.ControlMu.Lock()
	defer s.ControlMu.Unlock()
	return s.Group, s.Group.Snapshot
}

func (s *ConcreteSessionState) CurrentSnapshot() ConfigSnapshot {
	s.ControlMu.Lock()
	defer s.ControlMu.Unlock()
	return s.Group.Snapshot
}

func (s *ConcreteSessionState) ReplaceGroupRuntime(group GroupRuntime) {
	s.ControlMu.Lock()
	defer s.ControlMu.Unlock()
	s.DesiredGroup = group
	s.Group = group
	if s.Control.Pending != nil && controldomainruntime.SamePushedConfigSnapshot(
		controldomainruntime.ConfigSnapshotFromDesired(s.Control.Pending.Snapshot),
		group.Snapshot,
	) {
		s.Pending = group
		s.Recovery = controldomainruntime.PendingRecoveryModeForSnapshot(s.Pending.Snapshot)
		return
	}
	s.Recovery = controldomainruntime.AppliedRecoveryModeForSnapshot(s.Group.Snapshot)
}

func (s *ConcreteSessionState) RecoveryModeValue() testsupport.RecoveryMode {
	s.ControlMu.Lock()
	defer s.ControlMu.Unlock()
	return s.Recovery
}

func (s *ConcreteSessionState) ConfigAckState() (uint32, uint64) {
	s.ControlMu.Lock()
	defer s.ControlMu.Unlock()
	if s.Control.Pending == nil {
		return 0, s.Group.Snapshot.Version
	}
	return s.Control.Pending.RequestID, s.Control.Pending.Snapshot.Version
}

func (s *ConcreteSessionState) LastAckedConfigVersion() uint64 {
	s.ControlMu.Lock()
	defer s.ControlMu.Unlock()
	if s.Control.Applied == nil {
		return 0
	}
	return s.Control.Applied.Snapshot.Version
}

func (s *ConcreteSessionState) HasPendingConfig() bool {
	s.ControlMu.Lock()
	defer s.ControlMu.Unlock()
	return s.Control.Pending != nil
}

func (s *ConcreteSessionState) RefreshPendingConfig(group GroupRuntime, snapshot ConfigSnapshot) bool {
	s.ControlMu.Lock()
	defer s.ControlMu.Unlock()
	if s.Control.Pending == nil {
		return false
	}
	if !controldomainruntime.SamePushedConfigSnapshot(controldomainruntime.ConfigSnapshotFromDesired(s.Control.Pending.Snapshot), snapshot) {
		return false
	}
	group.Snapshot = snapshot
	s.DesiredGroup = group
	s.Pending = group
	return true
}

func NewConcreteSessionState(id uint64, group GroupRuntime, snapshot ConfigSnapshot, readTimeout time.Duration) *ConcreteSessionState {
	session := &ConcreteSessionState{
		ID:           id,
		ReadTimeout:  readTimeout,
		DesiredGroup: group,
		Group:        group,
	}
	session.DesiredGroup.Snapshot = snapshot
	session.Group.Snapshot = snapshot
	session.Control = controlsession.NewState(group.ID, id)
	desiredGroup := group
	desiredGroup.Snapshot = snapshot
	desired := controldomainruntime.DesiredRuntimeFromGroup(desiredGroup)
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
