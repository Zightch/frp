package wiring

import (
	"net"
	"time"

	controlruntime "github.com/zightch/frp/frps/internal/control/runtime"
	controlsession "github.com/zightch/frp/frps/internal/control/session"
)

type (
	sessionConfigPushOperation = controlruntime.ConfigPushOperation
	sessionConfigApplyResult   = controlruntime.ConfigApplyResult
	observedSessionConfigState = controlruntime.ObservedConfigState
)

// sessionState is the root assembly wrapper around concrete session runtime state.
type sessionState struct {
	*controlruntime.ConcreteSessionState
}

func newSessionState(id uint64, group GroupRuntime, snapshot ConfigSnapshot, readTimeout time.Duration) *sessionState {
	concrete := controlruntime.NewConcreteSessionState(id, group, snapshot, readTimeout)
	return &sessionState{ConcreteSessionState: concrete}
}

func (s *sessionState) controlState() controlsession.SessionState {
	s.ControlMu.Lock()
	defer s.ControlMu.Unlock()
	return controlsession.Clone(s.Control)
}

func (s *sessionState) setControlState(state controlsession.SessionState) {
	s.ControlMu.Lock()
	s.Control = controlsession.Clone(state)
	s.ControlMu.Unlock()
}

func (s *sessionState) applyControlEvent(event controlsession.Event) controlsession.SessionState {
	s.ControlMu.Lock()
	defer s.ControlMu.Unlock()
	s.Control = controlsession.Advance(s.Control, event)
	return controlsession.Clone(s.Control)
}

func (s *sessionState) resetRuntimeGenerationIfIdle() {
	s.ResetRuntimeGenerationIfIdle()
}

func (s *sessionState) attachTunnelListeners(configVersion uint64, tunnelID uint32, tcpListeners []net.Listener, udpListeners []UDPListener) (bool, bool) {
	return s.AttachTunnelListeners(configVersion, tunnelID, tcpListeners, udpListeners)
}

func (s *sessionState) addPublicStream(streamID uint32, stream *publicStream, configVersion uint64) bool {
	return s.AddPublicStream(streamID, stream, configVersion)
}

func (s *sessionState) canServeRuntimeIO(configVersion uint64) bool {
	return s.CanServeRuntimeIO(configVersion)
}

func (s *sessionState) lockRuntimeIOWrite(configVersion uint64) bool {
	return s.LockRuntimeIOWrite(configVersion)
}

func (s *sessionState) unlockRuntimeIOWrite() {
	s.UnlockRuntimeIOWrite()
}

func (s *sessionState) publicStream(streamID uint32) *publicStream {
	return s.PublicStream(streamID)
}

func (s *sessionState) closePublicStream(streamID uint32) bool {
	return s.ClosePublicStream(streamID)
}

func (s *sessionState) SessionID() uint64 { return s.ID }

func (s *sessionState) CurrentGroupAndSnapshot() (GroupRuntime, ConfigSnapshot) {
	return s.ConcreteSessionState.CurrentGroupAndSnapshot()
}

func (s *sessionState) ObserveState() (controlruntime.ObservedConfigState, controlruntime.ObservedState) {
	configState, runtimeState := s.observeState()
	return controlruntime.ObservedConfigState{
		State:        configState.State,
		Group:        configState.Group,
		PendingGroup: configState.PendingGroup,
		RecoveryMode: configState.RecoveryMode,
	}, runtimeState
}

func (s *sessionState) prepareConfigPush(group GroupRuntime, snapshot ConfigSnapshot) (sessionConfigPushOperation, error) {
	state := s.controlState()
	if state.Pending != nil {
		return sessionConfigPushOperation{}, errConfigUpdateInFlight
	}

	group.Snapshot = snapshot
	next := s.applyControlEvent(controlsession.DesiredRuntimeUpdated{Snapshot: controlruntime.DesiredRuntimeFromGroup(group)})
	if next.Pending == nil {
		return sessionConfigPushOperation{}, errConfigUpdateInFlight
	}
	recoveryMode := controlruntime.PendingRecoveryModeForSnapshot(snapshot)
	s.ControlMu.Lock()
	s.Pending = group
	s.Recovery = recoveryMode
	s.ControlMu.Unlock()
	return sessionConfigPushOperation{
		RequestID:    next.Pending.RequestID,
		Group:        group,
		Snapshot:     snapshot,
		RecoveryMode: recoveryMode,
	}, nil
}

func (s *sessionState) reconfigure(group GroupRuntime, snapshot ConfigSnapshot, requestID uint32) error {
	group.Snapshot = snapshot
	desired := controlruntime.DesiredRuntimeFromGroup(group)
	state := s.controlState()
	if state.Pending != nil && state.Pending.RequestID != requestID {
		return errConfigUpdateInFlight
	}
	state.Desired = &desired
	state.Pending = &controlsession.PendingConfigPush{
		RequestID: requestID,
		Snapshot:  desired,
	}
	state.Phase = controlsession.SessionPhaseSyncingConfig
	s.setControlState(state)

	s.ControlMu.Lock()
	group.Snapshot = snapshot
	s.Pending = group
	s.Recovery = controlruntime.PendingRecoveryModeForSnapshot(snapshot)
	s.ControlMu.Unlock()
	return nil
}

func (s *sessionState) acceptConfigAck(requestID uint32, version uint64) (sessionConfigApplyResult, error) {
	state := s.controlState()
	if state.Pending == nil || requestID != state.Pending.RequestID {
		return sessionConfigApplyResult{}, errUnexpectedConfigAck
	}
	if version != state.Pending.Snapshot.Version {
		return sessionConfigApplyResult{}, errConfigVersionMismatch
	}
	next := s.applyControlEvent(controlsession.ConfigAckReceived{
		RequestID:     requestID,
		ConfigVersion: version,
	})
	s.ControlMu.Lock()
	s.Group = s.Pending
	s.Group.Snapshot = controlruntime.ConfigSnapshotFromDesired(next.Applied.Snapshot)
	s.Pending = GroupRuntime{}
	s.Recovery = controlruntime.AppliedRecoveryModeForSnapshot(s.Group.Snapshot)
	group := s.Group
	recovery := s.Recovery
	s.ControlMu.Unlock()
	return sessionConfigApplyResult{
		Group:        group,
		Snapshot:     group.Snapshot,
		RecoveryMode: recovery,
	}, nil
}

func (s *sessionState) hasPendingConfig() bool {
	return s.controlState().Pending != nil
}

func (s *sessionState) refreshPendingConfig(group GroupRuntime, snapshot ConfigSnapshot) bool {
	state := s.controlState()
	if state.Pending == nil {
		return false
	}
	if !controlruntime.SamePushedConfigSnapshot(controlruntime.ConfigSnapshotFromDesired(state.Pending.Snapshot), snapshot) {
		return false
	}
	s.ControlMu.Lock()
	group.Snapshot = snapshot
	s.Pending = group
	s.ControlMu.Unlock()
	return true
}

func (s *sessionState) freezeTunnelRuntime() ([]net.Listener, []UDPListener, map[uint32]*publicStream, []*publicUDPSession) {
	return s.FreezeTunnelRuntime()
}

func (s *sessionState) allowTunnelRuntimeStart() {
	s.AllowTunnelRuntimeStart()
}

func (s *sessionState) resetTunnelRuntime() {
	listeners, udpListeners, _, _ := s.freezeTunnelRuntime()
	controlruntime.CloseStartedTunnelListeners(listeners, udpListeners)
	s.allowTunnelRuntimeStart()
}

func (s *sessionState) observeState() (observedSessionConfigState, controlruntime.ObservedState) {
	return s.ConcreteSessionState.ObserveState()
}

func (s *sessionState) bindPublicUDPSession(udpSession *publicUDPSession, configVersion uint64) (*publicUDPSession, bool) {
	return s.BindPublicUDPSession(udpSession, configVersion)
}

func (s *sessionState) publicUDPSession(sessionID uint32) *publicUDPSession {
	return s.PublicUDPSession(sessionID)
}

func (s *sessionState) closePublicUDPSession(sessionID uint32) bool {
	return s.ClosePublicUDPSession(sessionID)
}

func (s *sessionState) takeIdlePublicUDPSessions(now time.Time) []*publicUDPSession {
	return s.TakeIdlePublicUDPSessions(now)
}
