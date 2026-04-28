package control

import (
	"context"
	"crypto/tls"
	"errors"
	"log/slog"
	"net"
	"strings"
	"time"

	controlbind "github.com/zightch/frp/frps/internal/control/bind"
	controlrepo "github.com/zightch/frp/frps/internal/control/repo"
	controlruntime "github.com/zightch/frp/frps/internal/control/runtime"
	controlsession "github.com/zightch/frp/frps/internal/control/session"
	"github.com/zightch/frp/frps/internal/storage"
	"github.com/zightch/frp/frps/internal/system"
	"github.com/zightch/frp/frps/internal/testhooks"
	"github.com/zightch/frp/frps/pkg/protocol"
	"github.com/zightch/frp/frps/pkg/testsupport"
)

var errRuntimeIOStopped = errors.New("runtime io no longer allowed")

type (
	Repository              = controlrepo.Repository
	SQLRepository           = controlrepo.SQLRepository
	GroupRuntime            = controlrepo.GroupRuntime
	ConfigSnapshot          = controlrepo.ConfigSnapshot
	UDPListener             = controlbind.UDPListener
	ListenKey               = controlbind.ListenKey
	BindKind                = controlbind.BindKind
	ListenerBind            = controlbind.ListenerBind
	ListenerFactory         = controlbind.ListenerFactory
	ScriptedListenerFactory = controlbind.ScriptedListenerFactory
	ListenerCall            = controlbind.ListenerCall
	ScriptedListenerFailure = controlbind.ScriptedListenerFailure
)

var ErrGroupNotFound = controlrepo.ErrGroupNotFound

const (
	BindKindRuntimeProbe = controlbind.BindKindRuntimeProbe
	BindKindRuntimeStart = controlbind.BindKindRuntimeStart
)

func NewRepository(store *storage.SQL) *SQLRepository {
	return controlrepo.NewSQLRepository(store)
}
func NewNetListenerFactory() ListenerFactory {
	return controlbind.NewNetListenerFactory()
}
func NewScriptedListenerFactory() *ScriptedListenerFactory {
	return controlbind.NewScriptedListenerFactory()
}

func (s *Server) listenTCP(ctx context.Context, bind ListenerBind) (net.Listener, error) {
	testhooks.Point("control.listener.before_bind",
		testhooks.F("protocol", bind.Key.Protocol),
		testhooks.F("kind", string(bind.Kind)),
		testhooks.F("group_id", bind.GroupID),
		testhooks.F("tunnel_id", bind.TunnelID),
		testhooks.F("port", bind.Key.Port))
	listener, err := s.listeners.ListenTCP(ctx, bind)
	testhooks.Point("control.listener.after_bind",
		testhooks.F("protocol", bind.Key.Protocol),
		testhooks.F("kind", string(bind.Kind)),
		testhooks.F("group_id", bind.GroupID),
		testhooks.F("tunnel_id", bind.TunnelID),
		testhooks.F("port", bind.Key.Port),
		testhooks.F("ok", err == nil))
	return listener, err
}

func (s *Server) resolveUDPAddr(ctx context.Context, bind ListenerBind) (*net.UDPAddr, error) {
	testhooks.Point("control.listener.before_resolve_udp",
		testhooks.F("kind", string(bind.Kind)),
		testhooks.F("group_id", bind.GroupID),
		testhooks.F("tunnel_id", bind.TunnelID),
		testhooks.F("port", bind.Key.Port),
	)
	addr, err := s.listeners.ResolveUDP(ctx, bind)
	testhooks.Point("control.listener.after_resolve_udp",
		testhooks.F("kind", string(bind.Kind)),
		testhooks.F("group_id", bind.GroupID),
		testhooks.F("tunnel_id", bind.TunnelID),
		testhooks.F("port", bind.Key.Port),
		testhooks.F("ok", err == nil),
	)
	return addr, err
}

func (s *Server) listenUDP(ctx context.Context, bind ListenerBind, addr *net.UDPAddr) (UDPListener, error) {
	testhooks.Point("control.listener.before_bind",
		testhooks.F("protocol", bind.Key.Protocol),
		testhooks.F("kind", string(bind.Kind)),
		testhooks.F("group_id", bind.GroupID),
		testhooks.F("tunnel_id", bind.TunnelID),
		testhooks.F("port", bind.Key.Port))
	listener, err := s.listeners.ListenUDP(ctx, bind, addr)
	testhooks.Point("control.listener.after_bind",
		testhooks.F("protocol", bind.Key.Protocol),
		testhooks.F("kind", string(bind.Kind)),
		testhooks.F("group_id", bind.GroupID),
		testhooks.F("tunnel_id", bind.TunnelID),
		testhooks.F("port", bind.Key.Port),
		testhooks.F("ok", err == nil))
	return listener, err
}

type (
	sessionConfigPushOperation = controlruntime.ConfigPushOperation
	sessionConfigApplyResult   = controlruntime.ConfigApplyResult
	observedSessionConfigState = controlruntime.ObservedConfigState
)

// sessionState holds the runtime state for a control session.
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
	s.RuntimeMu.Lock()
	defer s.RuntimeMu.Unlock()
	if s.HasRuntimeListenersLocked() {
		return
	}
	s.Runtime.Listeners.Started = false
	s.Runtime.Generation = 0
}

func (s *sessionState) attachTunnelListeners(configVersion uint64, tunnelID uint32, tcpListeners []net.Listener, udpListeners []UDPListener) (bool, bool) {
	s.RuntimeMu.Lock()
	defer s.RuntimeMu.Unlock()
	if s.Runtime.Frozen {
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

func (s *sessionState) addPublicStream(streamID uint32, stream *publicStream, configVersion uint64) bool {
	s.RuntimeMu.Lock()
	defer s.RuntimeMu.Unlock()
	if s.Runtime.Frozen || !s.Runtime.Listeners.Started || s.Runtime.Generation != configVersion {
		return false
	}
	s.Runtime.Streams[streamID] = stream
	return true
}

func (s *sessionState) canServeRuntimeIO(configVersion uint64) bool {
	s.RuntimeMu.Lock()
	defer s.RuntimeMu.Unlock()
	return !s.Runtime.Frozen && s.Runtime.Listeners.Started && s.Runtime.Generation == configVersion
}

func (s *sessionState) lockRuntimeIOWrite(configVersion uint64) bool {
	s.WriteMu.Lock()
	s.RuntimeIOMu.RLock()
	if !s.canServeRuntimeIO(configVersion) {
		s.RuntimeIOMu.RUnlock()
		s.WriteMu.Unlock()
		return false
	}
	return true
}

func (s *sessionState) unlockRuntimeIOWrite() {
	s.RuntimeIOMu.RUnlock()
	s.WriteMu.Unlock()
}

func (s *sessionState) publicStream(streamID uint32) *publicStream {
	s.RuntimeMu.Lock()
	defer s.RuntimeMu.Unlock()
	return s.Runtime.Streams[streamID]
}
func (s *sessionState) closePublicStream(streamID uint32) bool {
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

// SessionID returns the session ID. This implements SessionStateProjectionTarget.
func (s *sessionState) SessionID() uint64 { return s.ID }

// CurrentGroupAndSnapshot returns the current group and snapshot. This implements SessionStateProjectionTarget.
func (s *sessionState) CurrentGroupAndSnapshot() (GroupRuntime, ConfigSnapshot) {
	return s.ConcreteSessionState.CurrentGroupAndSnapshot()
}

// ObserveState returns the observed config state and runtime state. This implements SessionStateProjectionTarget.
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
	udpListeners := make([]UDPListener, 0, len(s.Runtime.Listeners.UDP))
	for tunnelID, tunnelListeners := range s.Runtime.Listeners.UDP {
		delete(s.Runtime.Listeners.UDP, tunnelID)
		udpListeners = append(udpListeners, tunnelListeners...)
	}
	streams := make(map[uint32]*publicStream, len(s.Runtime.Streams))
	for streamID, stream := range s.Runtime.Streams {
		delete(s.Runtime.Streams, streamID)
		streams[streamID] = stream
	}
	udpSessions := make([]*publicUDPSession, 0, len(s.Runtime.UDP.Sessions))
	for sessionID, udpSession := range s.Runtime.UDP.Sessions {
		delete(s.Runtime.UDP.Sessions, sessionID)
		udpSessions = append(udpSessions, udpSession)
	}
	for key := range s.Runtime.UDP.Keys {
		delete(s.Runtime.UDP.Keys, key)
	}

	return listeners, udpListeners, streams, udpSessions
}

func (s *sessionState) allowTunnelRuntimeStart() {
	s.RuntimeMu.Lock()
	defer s.RuntimeMu.Unlock()
	s.Runtime.Frozen = false
}

func (s *sessionState) resetTunnelRuntime() {
	listeners, udpListeners, _, _ := s.freezeTunnelRuntime()
	controlruntime.CloseStartedTunnelListeners(listeners, udpListeners)
	s.allowTunnelRuntimeStart()
}

func (s *sessionState) observeState() (observedSessionConfigState, controlruntime.ObservedState) {
	s.ControlMu.Lock()
	configState := observedSessionConfigState{
		State:        controlsession.Clone(s.Control),
		Group:        s.Group,
		PendingGroup: s.Pending,
		RecoveryMode: s.Recovery,
	}
	s.ControlMu.Unlock()

	s.RuntimeMu.Lock()
	runtimeConnections := observeRuntimeConnections(s.Runtime.Streams, s.Runtime.UDP.Sessions)
	runtimeState := controlruntime.ObservedState{
		Frozen:                s.Runtime.Frozen,
		ListenersStarted:      s.Runtime.Listeners.Started,
		Generation:            s.Runtime.Generation,
		ActiveTunnelIDs:       s.ActiveRuntimeTunnelIDsLocked(),
		AttachedListeners:     controlruntime.ObserveRuntimeListeners(s.Runtime.Listeners.TCP, s.Runtime.Listeners.UDP),
		ActiveStreamCount:     uint32(len(s.Runtime.Streams)),
		ActiveUDPSessionCount: uint32(len(s.Runtime.UDP.Sessions)),
		Connections:           runtimeConnections,
	}
	s.RuntimeMu.Unlock()

	return configState, runtimeState
}

func (s *Server) shutdownSession(session *sessionState) {
	session.CloseDone()
	listeners, udpListeners, streams, _ := session.freezeTunnelRuntime()
	controlruntime.CloseStartedTunnelListeners(listeners, udpListeners)

	for _, stream := range streams {
		stream.SignalReady(net.ErrClosed)
		stream.Close()
	}
}

func (s *Server) writeFrameWithSession(conn net.Conn, session *sessionState, frame protocol.Frame) error {
	session.WriteMu.Lock()
	defer session.WriteMu.Unlock()
	return s.writeFrameWithContext(conn, frame, s.frameContext(conn, session))
}
func (s *Server) writeFramesWithSession(conn net.Conn, session *sessionState, frames ...protocol.Frame) error {
	session.WriteMu.Lock()
	defer session.WriteMu.Unlock()
	for _, frame := range frames {
		if err := s.writeFrameWithContext(conn, frame, s.frameContext(conn, session)); err != nil {
			return err
		}
	}
	return nil
}
func (s *Server) writeRuntimeFrameWithSession(conn net.Conn, session *sessionState, configVersion uint64, frame protocol.Frame) error {
	if !session.lockRuntimeIOWrite(configVersion) {
		return errRuntimeIOStopped
	}
	defer session.unlockRuntimeIOWrite()
	return s.writeFrameWithContext(conn, frame, s.frameContext(conn, session))
}

func (s *Server) writeRuntimeFramesWithSession(conn net.Conn, session *sessionState, configVersion uint64, frames ...protocol.Frame) error {
	if !session.lockRuntimeIOWrite(configVersion) {
		return errRuntimeIOStopped
	}
	defer session.unlockRuntimeIOWrite()
	for _, frame := range frames {
		if err := s.writeFrameWithContext(conn, frame, s.frameContext(conn, session)); err != nil {
			return err
		}
	}
	return nil
}

func (s *Server) replyProtocolErrorWithSession(conn net.Conn, session *sessionState, frame protocol.Frame, err error) error {
	protocolErr := protocol.AsProtocolError(err)
	if protocolErr == nil {
		return err
	}
	if writeErr := s.writeErrorWithSession(conn, session, frame.RequestID, frame.StreamID, protocolErr.Code, false, protocolErr.Message); writeErr != nil {
		return errors.Join(err, writeErr)
	}
	return err
}
func (s *Server) replyErrorWithSession(conn net.Conn, session *sessionState, requestID, streamID uint32, code uint16, format string, args ...any) error {
	err := protocol.NewError(code, format, args...)
	if writeErr := s.writeErrorWithSession(conn, session, requestID, streamID, code, false, err.Message); writeErr != nil {
		return errors.Join(err, writeErr)
	}
	return err
}

func (s *Server) writeErrorWithSession(conn net.Conn, session *sessionState, requestID, streamID uint32, code uint16, retryable bool, message string) error {
	body, err := protocol.MarshalErrorBody(protocol.ErrorBody{
		ErrorCode: code,
		Retryable: retryable,
		Message:   message,
	})
	if err != nil {
		return err
	}
	return s.writeFrameWithSession(conn, session, protocol.Frame{
		Type:      protocol.TypeError,
		RequestID: requestID,
		StreamID:  streamID,
		Body:      body,
	})
}

var (
	errConfigUpdateInFlight  = errors.New("config update already in flight")
	errUnexpectedConfigAck   = errors.New("unexpected config ack")
	errConfigVersionMismatch = errors.New("config version mismatch")
)

type sessionRuntimeStartTarget struct {
	conn     net.Conn
	logger   Logger
	session  *sessionState
	group    GroupRuntime
	snapshot ConfigSnapshot
}

// SessionRuntimeStartTarget interface implementation
func (t sessionRuntimeStartTarget) SessionIsDone() bool { return t.session.IsDone() }
func (t sessionRuntimeStartTarget) SessionCanStartTunnelRuntime() bool {
	return t.session.CanStartTunnelRuntime()
}
func (t sessionRuntimeStartTarget) SessionActiveRuntimeTunnelIDs() map[uint32]struct{} {
	return t.session.ActiveRuntimeTunnelIDs()
}
func (t sessionRuntimeStartTarget) SessionResetRuntimeGenerationIfIdle() {
	t.session.resetRuntimeGenerationIfIdle()
}
func (t sessionRuntimeStartTarget) SessionSetRecoveryMode(mode testsupport.RecoveryMode) {
	t.session.SetRecoveryMode(mode)
}
func (t sessionRuntimeStartTarget) SessionAttachTunnelListeners(configVersion uint64, tunnelID uint32, tcpListeners []net.Listener, udpListeners []UDPListener) (bool, bool) {
	return t.session.attachTunnelListeners(configVersion, tunnelID, tcpListeners, udpListeners)
}
func (t sessionRuntimeStartTarget) SessionHasActiveRuntimeListeners() bool {
	return t.session.HasActiveRuntimeListeners()
}
func (t sessionRuntimeStartTarget) Conn() net.Conn                          { return t.conn }
func (t sessionRuntimeStartTarget) Logger() controlruntime.Logger           { return t.logger }
func (t sessionRuntimeStartTarget) Group() controlruntime.GroupRuntime      { return t.group }
func (t sessionRuntimeStartTarget) Snapshot() controlruntime.ConfigSnapshot { return t.snapshot }
func (t sessionRuntimeStartTarget) getSession() *sessionState               { return t.session }

// sessionRuntimeStartPlan is now defined in controlruntime package as SessionRuntimeStartPlan
type sessionRuntimeStartPlan = controlruntime.SessionRuntimeStartPlan

type tunnelRuntimeServeContext struct {
	logger     Logger
	session    *sessionState
	runtimeIO  sessionRuntimeIOWriter
	tunnel     protocol.TunnelEntry
	remotePort uint16
}

func (s *Server) handleConfigAck(conn net.Conn, logger *slog.Logger, session *sessionState, agent *controlsession.Agent, frame protocol.Frame) error {
	// Validate frame fields
	if frame.RequestID == 0 {
		return s.replyErrorWithSession(conn, session, 0, frame.StreamID, protocol.ErrorCodeProtocolBadBody, "config.ack requestId must be non-zero")
	}
	if frame.StreamID != 0 {
		return s.replyErrorWithSession(conn, session, frame.RequestID, frame.StreamID, protocol.ErrorCodeProtocolBadBody, "config.ack streamId must be zero")
	}
	pendingRequestID, expectedVersion := session.ConfigAckState()
	if pendingRequestID == 0 || frame.RequestID != pendingRequestID {
		return s.replyErrorWithSession(conn, session, frame.RequestID, frame.StreamID, protocol.ErrorCodeProtocolBadBody, "unexpected config.ack requestId %d", frame.RequestID)
	}

	// Parse and validate payload
	ack, err := protocol.UnmarshalConfigAck(frame.Body)
	if err != nil {
		return s.replyProtocolErrorWithSession(conn, session, frame, err)
	}
	if ack.ConfigVersion != expectedVersion {
		return s.replyErrorWithSession(conn, session, frame.RequestID, 0, protocol.ErrorCodeProtocolBadBody, "config.ack version mismatch: got %d want %d", ack.ConfigVersion, expectedVersion)
	}
	if ack.Status == protocol.StatusError {
		return s.replyErrorWithSession(conn, session, frame.RequestID, 0, protocol.ErrorCodeConfigApplyFailed, "client rejected config version %d: %s", ack.ConfigVersion, strings.TrimSpace(ack.Message))
	}
	if ack.Status != protocol.StatusOK {
		return s.replyErrorWithSession(conn, session, frame.RequestID, 0, protocol.ErrorCodeProtocolBadBody, "unsupported config.ack status %d", ack.Status)
	}

	// Accept the ack
	currentGroup := session.CurrentGroup()
	isInitialStartup := session.LastAckedConfigVersion() == 0
	testhooks.Point("control.config_ack.before_accept",
		testhooks.F("group_id", currentGroup.ID),
		testhooks.F("session_id", session.ID),
		testhooks.F("request_id", frame.RequestID),
		testhooks.F("config_version", ack.ConfigVersion))

	appliedConfig, err := session.acceptConfigAck(frame.RequestID, ack.ConfigVersion)
	if err != nil {
		return s.handleConfigAckAcceptError(conn, session, frame, ack, err)
	}

	// Validate initial startup
	if isInitialStartup {
		if _, err := s.resolveGroupEffectiveIP(appliedConfig.Group); err != nil {
			reason := controlruntime.BuildGroupEffectiveIPRuntimeReason(appliedConfig.Group, err)
			for _, tunnel := range controlruntime.EnabledTunnels(appliedConfig.Snapshot) {
				s.recordTunnelRuntimeIssueForConfig(tunnel.TunnelID, appliedConfig.Snapshot.Version, reason)
			}
			if reason, ok := controlruntime.BuildInitialStartupRejectedReason(appliedConfig.Group, err); ok {
				return s.replyErrorWithSession(conn, session, frame.RequestID, 0, protocol.ErrorCodeConfigApplyFailed, "%s", reason)
			}
			return err
		}
	}

	testhooks.Point("control.config_ack.after_accept",
		testhooks.F("group_id", appliedConfig.Group.ID),
		testhooks.F("session_id", session.ID),
		testhooks.F("request_id", frame.RequestID),
		testhooks.F("config_version", ack.ConfigVersion))

	logger.Info("config acknowledged", "config_version", ack.ConfigVersion, "applied_at_ms", ack.AppliedAtMs)
	if agent == nil || !agent.Enqueue(controlsession.ConfigAckReceived{RequestID: frame.RequestID, ConfigVersion: ack.ConfigVersion}) {
		return net.ErrClosed
	}
	return nil
}

func (s *Server) handleConfigAckAcceptError(conn net.Conn, session *sessionState, frame protocol.Frame, ack protocol.ConfigAck, err error) error {
	switch {
	case errors.Is(err, errUnexpectedConfigAck):
		return s.replyErrorWithSession(conn, session, frame.RequestID, frame.StreamID, protocol.ErrorCodeProtocolBadBody, "unexpected config.ack requestId %d", frame.RequestID)
	case errors.Is(err, errConfigVersionMismatch):
		_, expectedVersion := session.ConfigAckState()
		return s.replyErrorWithSession(conn, session, frame.RequestID, 0, protocol.ErrorCodeProtocolBadBody, "config.ack version mismatch: got %d want %d", ack.ConfigVersion, expectedVersion)
	default:
		return err
	}
}

func (s *Server) pushConfig(conn net.Conn, session *sessionState) error {
	group, snapshot := session.CurrentGroupAndSnapshot()
	return s.pushReloadConfig(conn, session, group, snapshot)
}

func (s *Server) pushReloadConfig(conn net.Conn, session *sessionState, group GroupRuntime, snapshot ConfigSnapshot) error {
	body, err := protocol.MarshalConfigPush(protocol.ConfigPush{
		ConfigVersion: snapshot.Version,
		GeneratedAtMs: snapshot.GeneratedAtMs,
		Tunnels:       snapshot.Tunnels,
	})
	if err != nil {
		return err
	}
	pushOp, err := session.prepareConfigPush(group, snapshot)
	if err != nil {
		return err
	}
	testhooks.Point(
		"control.config_push.before_write",
		testhooks.F("group_id", pushOp.Group.ID),
		testhooks.F("session_id", session.ID),
		testhooks.F("request_id", pushOp.RequestID),
		testhooks.F("config_version", pushOp.Snapshot.Version),
		testhooks.F("tunnel_count", len(pushOp.Snapshot.Tunnels)),
	)
	if err := s.writeFrameWithSession(conn, session, protocol.Frame{
		Type:      protocol.TypeConfigPush,
		RequestID: pushOp.RequestID,
		Body:      body,
	}); err != nil {
		return err
	}
	testhooks.Point(
		"control.config_push.after_write",
		testhooks.F("group_id", pushOp.Group.ID),
		testhooks.F("session_id", session.ID),
		testhooks.F("request_id", pushOp.RequestID),
		testhooks.F("config_version", pushOp.Snapshot.Version),
		testhooks.F("tunnel_count", len(pushOp.Snapshot.Tunnels)),
	)
	return nil
}

func (s *Server) applyAcceptedConfig(conn net.Conn, logger Logger, session *sessionState, _ sessionConfigApplyResult) error {
	session.allowTunnelRuntimeStart()
	return s.ensureTunnelListeners(conn, logger, session)
}

func (s *Server) RefreshGroup(groupID int64) {
	if s == nil || groupID <= 0 {
		return
	}

	active, ok := s.activeSession(groupID)
	if !ok || active == nil || active.session == nil {
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), s.options.ReadTimeout)
	group, err := s.repo.LoadGroupRuntimeByID(ctx, groupID)
	cancel()
	if err != nil {
		switch {
		case errors.Is(err, ErrGroupNotFound):
			s.logger.Info("closing active session for deleted proxy group", "group_id", groupID, "session_id", active.session.ID)
			_ = active.conn.Close()
		default:
			s.logger.Warn("refresh proxy group runtime failed", "group_id", groupID, "session_id", active.session.ID, "error", err)
		}
		return
	}

	snapshot, _, _ := s.runtimeRefreshSnapshot(group, controlruntime.RuntimeSnapshotForGroup(group))
	group.Snapshot = snapshot
	currentGroup, currentSnapshot := active.session.CurrentGroupAndSnapshot()
	if currentGroup.EffectiveIP == group.EffectiveIP && controlruntime.SamePushedConfigSnapshot(currentSnapshot, group.Snapshot) {
		active.session.ReplaceGroupRuntime(group)
	}
	if runtime := s.runtimeExecutor(active.session.ID); runtime != nil {
		runtime.setDesiredGroup(group)
	}
	next := active.session.applyControlEvent(controlsession.DesiredRuntimeUpdated{Snapshot: controlruntime.DesiredRuntimeFromGroup(group)})
	if next.Pending != nil {
		active.session.ControlMu.Lock()
		active.session.Pending = group
		active.session.Recovery = controlruntime.PendingRecoveryModeForSnapshot(group.Snapshot)
		active.session.ControlMu.Unlock()
	}
	s.supervisor.UpdateDesiredRuntime(groupID, controlruntime.DesiredRuntimeFromGroup(group))
}

func (s *Server) freezeGroupRuntime(conn net.Conn, session *sessionState) error {
	listeners, udpListeners, streams, udpSessions := session.freezeTunnelRuntime()
	controlruntime.CloseStartedTunnelListeners(listeners, udpListeners)

	var freezeErr error
	for streamID, stream := range streams {
		if err := s.sendStreamClose(conn, session, streamID, protocol.CloseReasonAdminTerminated, "reload in progress"); err != nil && freezeErr == nil {
			freezeErr = err
		}
		stream.SignalReady(net.ErrClosed)
		stream.Close()
	}
	for _, udpSession := range udpSessions {
		if err := s.sendUDPClose(conn, session, udpSession.SessionID, protocol.CloseReasonAdminTerminated, "reload in progress"); err != nil && freezeErr == nil {
			freezeErr = err
		}
	}
	return freezeErr
}

func (s *Server) rebindGroupRuntime(conn net.Conn, session *sessionState, group GroupRuntime) error {
	if err := s.freezeGroupRuntime(conn, session); err != nil {
		return err
	}
	session.ReplaceGroupRuntime(group)
	session.allowTunnelRuntimeStart()
	logger := s.logger.With(
		"session_id", session.ID,
		"group_id", group.ID,
		"group_name", group.Name,
	)
	return s.ensureTunnelListeners(conn, logger, session)
}

func (s *Server) runtimeRefreshSnapshot(group GroupRuntime, snapshot ConfigSnapshot) (ConfigSnapshot, error, bool) {
	enabled := controlruntime.EnabledTunnels(snapshot)
	if len(enabled) == 0 {
		return snapshot, nil, false
	}

	if _, err := s.resolveGroupEffectiveIP(group); err != nil {
		s.clearTunnelRuntimeIssues(group.Snapshot.Tunnels)
		reason := controlruntime.BuildGroupEffectiveIPRuntimeReason(group, err)
		for _, tunnel := range enabled {
			s.recordTunnelRuntimeIssueForConfig(tunnel.TunnelID, snapshot.Version, reason)
		}
		return controlruntime.EmptyConfigSnapshot(snapshot), err, true
	}

	return snapshot, nil, false
}

func newSessionRuntimeStartTarget(conn net.Conn, logger Logger, session *sessionState) sessionRuntimeStartTarget {
	group, snapshot := session.CurrentGroupAndSnapshot()
	return sessionRuntimeStartTarget{
		conn:     conn,
		logger:   logger,
		session:  session,
		group:    group,
		snapshot: snapshot,
	}
}

func (s *Server) activeSession(groupID int64) (*activeSession, bool) {
	return s.supervisor.activeSession(groupID)
}

func (s *Server) ensureTunnelListeners(conn net.Conn, logger Logger, session *sessionState) error {
	target := newSessionRuntimeStartTarget(conn, logger, session)
	plan := controlruntime.PlanSessionRuntimeStart(s, target)
	return controlruntime.ApplySessionRuntimeStartPlan(s, target, plan)
}

func (s *Server) startTunnelListeners(opCtx controlruntime.TunnelListenerOperationContext) (controlruntime.TunnelListenerBatch, error) {
	started := controlruntime.TunnelListenerBatch{}
	switch opCtx.Tunnel.Protocol {
	case protocol.ProtocolTCP:
		tlsConfig, err := controlbind.LoadTunnelListenerTLSConfig(context.Background(), s.options.Store, opCtx.Tunnel.TunnelID)
		if err != nil {
			return controlruntime.TunnelListenerBatch{}, opCtx.ListenerStartError(opCtx.Tunnel.RemoteStart, err)
		}
		started.TCPListeners = make([]net.Listener, 0, opCtx.RemotePortCount())
		started.TCPRuntimes = make([]controlruntime.TCPTunnelListener, 0, opCtx.RemotePortCount())
		for remotePort := int(opCtx.Tunnel.RemoteStart); remotePort <= int(opCtx.Tunnel.RemoteEnd); remotePort++ {
			bind := opCtx.ListenerBind(uint16(remotePort))
			listener, err := s.listenTCP(context.Background(), bind)
			if err != nil {
				controlruntime.CloseStartedTunnelListeners(started.TCPListeners, started.UDPListeners)
				return controlruntime.TunnelListenerBatch{}, opCtx.ListenerStartError(uint16(remotePort), err)
			}
			if tlsConfig != nil {
				listener = tls.NewListener(listener, tlsConfig.Clone())
			}
			started.TCPListeners = append(started.TCPListeners, listener)
			started.TCPRuntimes = append(started.TCPRuntimes, controlruntime.TCPTunnelListener{
				ConfigVersion: opCtx.ConfigVersion,
				Tunnel:        opCtx.Tunnel,
				RemotePort:    uint16(remotePort),
				Listener:      listener,
			})
		}
	case protocol.ProtocolUDP:
		started.UDPListeners = make([]UDPListener, 0, opCtx.RemotePortCount())
		started.UDPRuntimes = make([]controlruntime.UDPTunnelListener, 0, opCtx.RemotePortCount())
		for remotePort := int(opCtx.Tunnel.RemoteStart); remotePort <= int(opCtx.Tunnel.RemoteEnd); remotePort++ {
			bind := opCtx.ListenerBind(uint16(remotePort))
			udpAddr, err := s.resolveUDPAddr(context.Background(), bind)
			if err != nil {
				controlruntime.CloseStartedTunnelListeners(started.TCPListeners, started.UDPListeners)
				return controlruntime.TunnelListenerBatch{}, opCtx.ListenerStartError(uint16(remotePort), err)
			}
			listener, err := s.listenUDP(context.Background(), bind, udpAddr)
			if err != nil {
				controlruntime.CloseStartedTunnelListeners(started.TCPListeners, started.UDPListeners)
				return controlruntime.TunnelListenerBatch{}, opCtx.ListenerStartError(uint16(remotePort), err)
			}
			started.UDPListeners = append(started.UDPListeners, listener)
			started.UDPRuntimes = append(started.UDPRuntimes, controlruntime.UDPTunnelListener{
				ConfigVersion: opCtx.ConfigVersion,
				Tunnel:        opCtx.Tunnel,
				RemotePort:    uint16(remotePort),
				Listener:      listener,
			})
		}
	}
	return started, nil
}

func (s *Server) resolveGroupEffectiveIP(group GroupRuntime) (string, error) {
	effectiveIP, err := system.NormalizeListenIP(group.EffectiveIP)
	if err != nil {
		return "", &controlruntime.GroupEffectiveIPStartError{
			EffectiveIP: group.EffectiveIP,
			Kind:        controlruntime.GroupEffectiveIPStartErrorInvalid,
			Cause:       err,
		}
	}
	if system.IsSpecialListenIP(effectiveIP) {
		return effectiveIP, nil
	}
	if s.network != nil && !s.network.Current().HasIP(effectiveIP) {
		return "", &controlruntime.GroupEffectiveIPStartError{
			EffectiveIP: effectiveIP,
			Kind:        controlruntime.GroupEffectiveIPStartErrorNotLocal,
		}
	}
	return effectiveIP, nil
}

func (s *Server) serveTunnelListener(serve tunnelRuntimeServeContext, listener net.Listener) {
	for {
		publicConn, err := listener.Accept()
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return
			}
			serve.logger.Warn("tcp tunnel accept failed", "tunnel_id", serve.tunnel.TunnelID, "error", err)
			continue
		}

		go s.handlePublicConnection(serve, publicConn)
	}
}

func (s *Server) serveUDPTunnelListener(serve tunnelRuntimeServeContext, listener UDPListener) {
	buffer := make([]byte, protocol.MaxDataBodyLen)
	for {
		n, clientAddr, err := listener.ReadFromUDP(buffer)
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return
			}
			serve.logger.Warn("udp tunnel read failed", "tunnel_id", serve.tunnel.TunnelID, "error", err)
			continue
		}
		payload := append([]byte(nil), buffer[:n]...)
		if err := s.handlePublicUDPDatagram(serve, listener, clientAddr, payload); err != nil {
			serve.logger.Warn(
				"udp tunnel forward failed",
				"tunnel_id", serve.tunnel.TunnelID,
				"remote_port", serve.remotePort,
				"client_addr", clientAddr.String(),
				"error", err,
			)
		}
	}
}

func (s *Server) runtimeSnapshotIndex() controlruntime.RuntimeSnapshotIndex {
	if s == nil || s.supervisor == nil {
		return controlruntime.RuntimeSnapshotIndex{}
	}
	return controlruntime.NewRuntimeSnapshotIndex(s.supervisor.Snapshot(nil).sessions)
}

// RuntimeSnapshotIndex implements controlruntime.RuntimeOperator.
func (s *Server) RuntimeSnapshotIndex() controlruntime.RuntimeSnapshotIndex {
	return s.runtimeSnapshotIndex()
}

func (s *Server) startRuntimeIssuePolling(parent context.Context) {
	controlruntime.StartRuntimeIssuePolling(s, parent)
}

func (s *Server) scanNonListeningTunnelRuntimeIssues(ctx context.Context) error {
	return controlruntime.ScanNonListeningTunnelRuntimeIssues(s, ctx)
}

// BeginRuntimeScanRound implements controlruntime.RuntimeOperator.
func (s *Server) BeginRuntimeScanRound() bool {
	if s == nil {
		return false
	}

	s.runtimeScanStateMu.Lock()
	defer s.runtimeScanStateMu.Unlock()
	if s.runtimeScanInFlight {
		return false
	}
	s.runtimeScanInFlight = true
	return true
}

// FinishRuntimeScanRound implements controlruntime.RuntimeOperator.
func (s *Server) FinishRuntimeScanRound() {
	if s == nil {
		return
	}

	s.runtimeScanStateMu.Lock()
	s.runtimeScanInFlight = false
	s.runtimeScanStateMu.Unlock()
}

func (s *Server) recoverScannedActiveSessionTunnels(viewIndex controlruntime.RuntimeSnapshotIndex, group GroupRuntime, targetTunnels []protocol.TunnelEntry, staticConflictIDs map[int64]struct{}, issues map[uint32]string) error {
	return controlruntime.RecoverScannedActiveSessionTunnels(s, viewIndex, group, targetTunnels, staticConflictIDs, issues)
}

func (s *Server) requestAuditedSessionRuntimeRecovery(sessionID uint64, targetTunnels []protocol.TunnelEntry) error {
	return controlruntime.RequestAuditedSessionRuntimeRecovery(s, sessionID, targetTunnels)
}

func (s *Server) probeTunnelRuntimeIssue(groupID int64, bindIP string, tunnel protocol.TunnelEntry) string {
	return controlruntime.ProbeTunnelRuntimeIssue(s, groupID, bindIP, tunnel)
}

// SetRuntimeScanCancel implements controlruntime.RuntimeOperator.
func (s *Server) SetRuntimeScanCancel(cancel context.CancelFunc) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.isShuttingDown() {
		if cancel != nil {
			cancel()
		}
		return
	}
	s.runtimeScanCancel = cancel
}
