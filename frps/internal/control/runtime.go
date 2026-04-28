package control

import (
	"context"
	"crypto/tls"
	"errors"
	"log/slog"
	"net"
	"time"

	controlbind "github.com/zightch/frp/frps/internal/control/bind"
	controlconfigsync "github.com/zightch/frp/frps/internal/control/protocol/configsync"
	controlprotocolerrors "github.com/zightch/frp/frps/internal/control/protocol/errors"
	controlrepo "github.com/zightch/frp/frps/internal/control/repo"
	controlruntime "github.com/zightch/frp/frps/internal/control/runtime"
	controllistener "github.com/zightch/frp/frps/internal/control/runtime/listener"
	controllistenertls "github.com/zightch/frp/frps/internal/control/runtime/listener/tls"
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

func (s *Server) shutdownSession(session *sessionState) {
	session.CloseDone()
	listeners, udpListeners, streams, _ := session.freezeTunnelRuntime()
	controlruntime.CloseStartedTunnelListeners(listeners, udpListeners)

	for _, stream := range streams {
		stream.SignalReady(net.ErrClosed)
		stream.Close()
	}
}

type sessionFrameWriter struct {
	server  *Server
	conn    net.Conn
	session *sessionState
}

func (w sessionFrameWriter) WriteFrame(frame protocol.Frame) error {
	return w.server.writeFrameWithSession(w.conn, w.session, frame)
}

func (s *Server) sessionFrameWriter(conn net.Conn, session *sessionState) sessionFrameWriter {
	return sessionFrameWriter{
		server:  s,
		conn:    conn,
		session: session,
	}
}

func (s *Server) writeFrameWithSession(conn net.Conn, session *sessionState, frame protocol.Frame) error {
	session.WriteMu.Lock()
	defer session.WriteMu.Unlock()
	return s.frameWriter(conn, session).WriteFrame(frame)
}
func (s *Server) writeFramesWithSession(conn net.Conn, session *sessionState, frames ...protocol.Frame) error {
	session.WriteMu.Lock()
	defer session.WriteMu.Unlock()
	return s.frameWriter(conn, session).WriteFrames(frames...)
}
func (s *Server) writeRuntimeFrameWithSession(conn net.Conn, session *sessionState, configVersion uint64, frame protocol.Frame) error {
	if !session.lockRuntimeIOWrite(configVersion) {
		return errRuntimeIOStopped
	}
	defer session.unlockRuntimeIOWrite()
	return s.frameWriter(conn, session).WriteFrame(frame)
}

func (s *Server) writeRuntimeFramesWithSession(conn net.Conn, session *sessionState, configVersion uint64, frames ...protocol.Frame) error {
	if !session.lockRuntimeIOWrite(configVersion) {
		return errRuntimeIOStopped
	}
	defer session.unlockRuntimeIOWrite()
	return s.frameWriter(conn, session).WriteFrames(frames...)
}

func (s *Server) replyProtocolErrorWithSession(conn net.Conn, session *sessionState, frame protocol.Frame, err error) error {
	return controlprotocolerrors.ReplyProtocolError(s.sessionFrameWriter(conn, session), frame, err)
}
func (s *Server) replyErrorWithSession(conn net.Conn, session *sessionState, requestID, streamID uint32, code uint16, format string, args ...any) error {
	return controlprotocolerrors.ReplyError(s.sessionFrameWriter(conn, session), requestID, streamID, code, format, args...)
}

func (s *Server) writeErrorWithSession(conn net.Conn, session *sessionState, requestID, streamID uint32, code uint16, retryable bool, message string) error {
	return controlprotocolerrors.WriteError(s.sessionFrameWriter(conn, session), requestID, streamID, code, retryable, message)
}

var (
	errConfigUpdateInFlight  = errors.New("config update already in flight")
	errUnexpectedConfigAck   = controlconfigsync.ErrUnexpectedAck
	errConfigVersionMismatch = controlconfigsync.ErrVersionMismatch
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
	pendingRequestID, expectedVersion := session.ConfigAckState()
	ack, err := controlconfigsync.DecodeAckFrame(frame, controlconfigsync.AckState{
		PendingRequestID: pendingRequestID,
		ExpectedVersion:  expectedVersion,
	})
	if err != nil {
		return s.replyProtocolErrorWithSession(conn, session, frame, err)
	}

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

	if isInitialStartup {
		err := controlconfigsync.ValidateInitialRuntime(controlconfigsync.InitialRuntimeValidationOptions{
			Group:                 appliedConfig.Group,
			Snapshot:              appliedConfig.Snapshot,
			ResolveEffectiveIP:    s.resolveGroupEffectiveIP,
			RecordRuntimeIssue:    s.recordTunnelRuntimeIssueForConfig,
			RuntimeReason:         controlruntime.BuildGroupEffectiveIPRuntimeReason,
			StartupRejectedReason: controlruntime.BuildInitialStartupRejectedReason,
		})
		if err != nil {
			return s.replyProtocolErrorWithSession(conn, session, frame, err)
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
	_, expectedVersion := session.ConfigAckState()
	if mapped, ok := controlconfigsync.MapAcceptError(err, frame.RequestID, ack, expectedVersion); ok {
		return s.replyProtocolErrorWithSession(conn, session, frame, mapped)
	}
	return err
}

func (s *Server) pushConfig(conn net.Conn, session *sessionState) error {
	group, snapshot := session.CurrentGroupAndSnapshot()
	return s.pushReloadConfig(conn, session, group, snapshot)
}

func (s *Server) pushReloadConfig(conn net.Conn, session *sessionState, group GroupRuntime, snapshot ConfigSnapshot) error {
	body, err := controlconfigsync.BuildPushBody(snapshot)
	if err != nil {
		return err
	}
	pushOp, err := session.prepareConfigPush(group, snapshot)
	if err != nil {
		return err
	}
	frame := controlconfigsync.NewPushFrame(pushOp.RequestID, body)
	testhooks.Point(
		"control.config_push.before_write",
		testhooks.F("group_id", pushOp.Group.ID),
		testhooks.F("session_id", session.ID),
		testhooks.F("request_id", pushOp.RequestID),
		testhooks.F("config_version", pushOp.Snapshot.Version),
		testhooks.F("tunnel_count", len(pushOp.Snapshot.Tunnels)),
	)
	if err := s.writeFrameWithSession(conn, session, frame); err != nil {
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

	active, ok := s.refreshGroupActiveSession(groupID)
	if !ok {
		return
	}
	group, ok := s.loadLatestRefreshGroup(groupID, active)
	if !ok {
		return
	}

	group = s.computeRuntimeRefreshGroup(group)
	s.updateActiveSessionDesiredRuntime(active, group)
	s.notifyDesiredRuntimeRefresh(groupID, group)
}

func (s *Server) refreshGroupActiveSession(groupID int64) (*activeSession, bool) {
	active, ok := s.activeSession(groupID)
	if !ok || active == nil || active.session == nil {
		return nil, false
	}
	return active, true
}

func (s *Server) loadLatestRefreshGroup(groupID int64, active *activeSession) (GroupRuntime, bool) {
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
		return GroupRuntime{}, false
	}
	return group, true
}

func (s *Server) computeRuntimeRefreshGroup(group GroupRuntime) GroupRuntime {
	snapshot, _, _ := s.runtimeRefreshSnapshot(group, controlruntime.RuntimeSnapshotForGroup(group))
	group.Snapshot = snapshot
	return group
}

func (s *Server) updateActiveSessionDesiredRuntime(active *activeSession, group GroupRuntime) {
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
}

func (s *Server) notifyDesiredRuntimeRefresh(groupID int64, group GroupRuntime) {
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
	starter := controllistener.NewStarter(controllistener.StarterOptions{
		Factory: s.listeners,
		TLSLoader: func(ctx context.Context, tunnelID uint32) (*tls.Config, error) {
			return controllistenertls.LoadTunnelListenerTLSConfig(ctx, s.options.Store, tunnelID)
		},
	})
	return starter.StartTunnelListeners(opCtx)
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
	s.tcpHandler().ServeTunnelListener(tcpServeContext(serve), listener)
}

func (s *Server) serveUDPTunnelListener(serve tunnelRuntimeServeContext, listener UDPListener) {
	s.udpHandler().ServeUDPTunnelListener(udpServeContext(serve), listener)
}

func (s *Server) runtimeSnapshotIndex() controlruntime.RuntimeSnapshotIndex {
	if s == nil || s.supervisor == nil {
		return controlruntime.RuntimeSnapshotIndex{}
	}
	return controlruntime.NewRuntimeSnapshotIndex(s.supervisor.Snapshot(nil).sessions)
}

// RuntimeSnapshotIndex implements the runtime snapshot provider seam.
func (s *Server) RuntimeSnapshotIndex() controlruntime.RuntimeSnapshotIndex {
	return s.runtimeSnapshotIndex()
}

func (s *Server) startRuntimeIssuePolling(parent context.Context) {
	controlruntime.StartRuntimeIssuePolling(s, parent)
}

func (s *Server) scanNonListeningTunnelRuntimeIssues(ctx context.Context) error {
	return controlruntime.ScanNonListeningTunnelRuntimeIssues(s, ctx)
}

// BeginRuntimeScanRound implements the runtime scan coordinator seam.
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

// FinishRuntimeScanRound implements the runtime scan coordinator seam.
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

// SetRuntimeScanCancel implements the runtime scan coordinator seam.
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
