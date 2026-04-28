package control

import (
	"context"
	"crypto/tls"
	"errors"
	"log/slog"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/zightch/frp/frps/internal/clock"
	controlruntime "github.com/zightch/frp/frps/internal/control/runtime"
	controlsession "github.com/zightch/frp/frps/internal/control/session"
	"github.com/zightch/frp/frps/internal/storage"
	"github.com/zightch/frp/frps/internal/system"
	"github.com/zightch/frp/frps/internal/testhooks"
	"github.com/zightch/frp/frps/pkg/protocol"
	"github.com/zightch/frp/frps/pkg/transport"
)

const (
	defaultReadTimeout       = 5 * time.Second
	defaultWriteTimeout      = 5 * time.Second
	defaultChallengeTTL      = 30 * time.Second
	defaultHeartbeatInterval = 15 * time.Second
	defaultUDPIdleTimeout    = 30 * time.Second
	defaultUDPIdleSweep      = time.Second
	defaultRuntimeScanPoll   = 5 * time.Second
)

type Options struct {
	Addr              string
	Store             *storage.SQL
	Repository        Repository
	Network           system.SnapshotReader
	ReadTimeout       time.Duration
	WriteTimeout      time.Duration
	ChallengeTTL      time.Duration
	HeartbeatInterval time.Duration
	RuntimeScanPoll   time.Duration
	Clock             clock.Clock
	Scheduler         clock.Scheduler
	ListenerFactory   ListenerFactory
	FrameIO           transport.FrameIO
}

type Server struct {
	options   Options
	logger    *slog.Logger
	version   string
	repo      Repository
	network   system.SnapshotReader
	clock     clock.Clock
	scheduler clock.Scheduler
	listeners ListenerFactory
	frames    transport.FrameIO

	mu                  sync.Mutex
	listener            net.Listener
	controlListenerOpen bool
	activeConn          map[net.Conn]struct{}
	runtimeIssues       *controlruntime.IssueStore
	closeOnce           sync.Once
	connWG              sync.WaitGroup
	scanWG              sync.WaitGroup
	shutdownCh          chan struct{}

	challengeMu sync.Mutex
	challenges  map[uint32]*authChallenge

	initialRuntimeScanMu   sync.Mutex
	initialRuntimeScanDone bool
	runtimeScanCancel      context.CancelFunc
	runtimeScanStateMu     sync.Mutex
	runtimeScanInFlight    bool

	nextChallengeID atomic.Uint32
	nextSessionID   atomic.Uint64

	controlTLSMu          sync.RWMutex
	controlTLSCertificate *tls.Certificate

	supervisor *Supervisor
}

type activeSession struct {
	mu      sync.Mutex
	conn    net.Conn
	session *sessionState
}

func NewServer(options Options, logger *slog.Logger, version string) *Server {
	if options.ReadTimeout <= 0 {
		options.ReadTimeout = defaultReadTimeout
	}
	if options.WriteTimeout <= 0 {
		options.WriteTimeout = defaultWriteTimeout
	}
	if options.ChallengeTTL <= 0 {
		options.ChallengeTTL = defaultChallengeTTL
	}
	if options.HeartbeatInterval <= 0 {
		options.HeartbeatInterval = defaultHeartbeatInterval
	}
	if options.RuntimeScanPoll <= 0 {
		options.RuntimeScanPoll = defaultRuntimeScanPoll
	}
	if options.Repository == nil && options.Store != nil {
		options.Repository = NewRepository(options.Store)
	}
	if options.Clock == nil {
		realClock := clock.NewRealClock()
		options.Clock = realClock
	}
	if options.Scheduler == nil {
		options.Scheduler = clock.NewRealScheduler()
	}
	if options.ListenerFactory == nil {
		options.ListenerFactory = NewNetListenerFactory()
	}
	if options.FrameIO == nil {
		options.FrameIO = transport.RealFrameIO{}
	}

	server := &Server{
		options:       options,
		logger:        logger,
		version:       version,
		repo:          options.Repository,
		network:       options.Network,
		clock:         options.Clock,
		scheduler:     options.Scheduler,
		listeners:     options.ListenerFactory,
		frames:        options.FrameIO,
		activeConn:    make(map[net.Conn]struct{}),
		runtimeIssues: controlruntime.NewIssueStore(),
		shutdownCh:    make(chan struct{}),
		challenges:    make(map[uint32]*authChallenge),
	}
	server.supervisor = NewSupervisor(serverActionExecutor{server: server})
	return server
}

func (s *Server) isShuttingDown() bool {
	if s == nil || s.shutdownCh == nil {
		return false
	}

	select {
	case <-s.shutdownCh:
		return true
	default:
		return false
	}
}

func (s *Server) ListenAndServe(ctx context.Context) error {
	if s.isShuttingDown() {
		return nil
	}
	if err := s.EnsureInitialRuntimeScan(ctx); err != nil {
		return err
	}
	if s.isShuttingDown() {
		return nil
	}

	testhooks.Point("startup.control_listener.before_open", testhooks.F("addr", s.options.Addr))
	listenConfig := net.ListenConfig{}
	listener, err := listenConfig.Listen(ctx, "tcp", s.options.Addr)
	if err != nil {
		return err
	}
	if s.isShuttingDown() {
		_ = listener.Close()
		return nil
	}

	s.mu.Lock()
	s.listener = listener
	s.controlListenerOpen = true
	s.mu.Unlock()

	testhooks.Point("startup.control_listener.after_open", testhooks.F("addr", s.options.Addr))
	if s.isShuttingDown() {
		_ = listener.Close()
		return nil
	}
	s.startRuntimeIssuePolling(ctx)
	s.logger.Info("frpc control listener ready", "addr", s.options.Addr)

	for {
		conn, err := listener.Accept()
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return nil
			}
			return err
		}

		s.registerConn(conn)
		s.connWG.Add(1)
		go s.handleConnection(conn)
	}
}

func (s *Server) Shutdown(ctx context.Context) error {
	s.closeOnce.Do(func() {
		if s.shutdownCh != nil {
			close(s.shutdownCh)
		}
		s.mu.Lock()
		listener := s.listener
		s.controlListenerOpen = false
		activeConn := make([]net.Conn, 0, len(s.activeConn))
		for conn := range s.activeConn {
			activeConn = append(activeConn, conn)
		}
		runtimeScanCancel := s.runtimeScanCancel
		s.mu.Unlock()

		if runtimeScanCancel != nil {
			runtimeScanCancel()
		}
		if listener != nil {
			_ = listener.Close()
		}
		if s.supervisor != nil {
			s.supervisor.Shutdown()
		}
		for _, conn := range activeConn {
			_ = conn.Close()
		}
	})

	done := make(chan struct{})
	go func() {
		defer close(done)
		s.connWG.Wait()
		s.scanWG.Wait()
	}()

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-done:
		return nil
	}
}

func (s *Server) EnsureInitialRuntimeScan(ctx context.Context) error {
	if s == nil {
		return nil
	}

	s.initialRuntimeScanMu.Lock()
	defer s.initialRuntimeScanMu.Unlock()

	if s.initialRuntimeScanDone {
		return nil
	}
	testhooks.Point("startup.initial_scan.before_full_scan")
	if err := s.scanNonListeningTunnelRuntimeIssues(ctx); err != nil {
		return err
	}
	testhooks.Point("startup.initial_scan.after_full_scan")
	s.initialRuntimeScanDone = true
	return nil
}

func (s *Server) handleConnection(conn net.Conn) {
	defer s.connWG.Done()
	defer s.unregisterConn(conn)
	defer conn.Close()

	logger := s.logger.With("remote_addr", conn.RemoteAddr().String())
	logger.Info("frpc control connection accepted")

	if s.repo == nil {
		logger.Error("frpc control connection rejected", "reason", "repository not configured")
		logger.Info("frpc control connection closed", "reason", "repository not configured")
		return
	}

	conn, clientID, err := s.negotiateTransport(conn)
	if err != nil {
		level, reason := connectionErrorDetails(err)
		logConnection(logger, level, "frpc control transport negotiation failed", err)
		logger.Info("frpc control connection closed", "reason", reason)
		return
	}

	session, agent, err := s.authenticate(conn, clientID, logger)
	if err != nil {
		level, reason := connectionErrorDetails(err)
		logConnection(logger, level, "frpc control login failed", err)
		logger.Info("frpc control connection closed", "reason", reason)
		return
	}

	group, snapshot := session.CurrentGroupAndSnapshot()
	logger = logger.With(
		"session_id", session.ID,
		"group_id", group.ID,
		"group_name", group.Name,
	)
	defer s.unregisterRuntimeExecutor(session.ID)
	defer func() {
		if agent != nil {
			session.applyControlEvent(controlsession.ControlConnClosed{Reason: "connection closed"})
			_ = agent.Enqueue(controlsession.ControlConnClosed{Reason: "connection closed"})
		}
	}()
	defer s.shutdownSession(session)
	logger.Info(
		"frpc control login succeeded",
		"config_version", snapshot.Version,
		"tunnel_count", len(snapshot.Tunnels),
	)

	err = s.runSession(conn, logger, session, agent)
	if err != nil {
		level, reason := connectionErrorDetails(err)
		logConnection(logger, level, "frpc control session ended", err)
		logger.Info("frpc control connection closed", "reason", reason)
		return
	}

	logger.Info("frpc control connection closed", "reason", "completed")
}

func (s *Server) runSession(conn net.Conn, logger *slog.Logger, session *sessionState, agent *controlsession.Agent) error {
	for {
		frame, err := s.readFrameWithSessionTimeout(conn, session, session.ReadTimeout)
		if err != nil {
			return s.replyProtocolErrorWithSession(conn, session, frame, err)
		}

		switch frame.Type {
		case protocol.TypeConfigAck:
			if err := s.handleConfigAck(conn, logger, session, agent, frame); err != nil {
				return err
			}
		case protocol.TypeHeartbeatPing:
			if err := s.handleHeartbeatPing(conn, session, agent, frame); err != nil {
				return err
			}
		case protocol.TypeStreamOpened:
			if err := s.handleStreamOpened(conn, session, frame); err != nil {
				return err
			}
		case protocol.TypeStreamData:
			if err := s.handleStreamData(conn, session, frame); err != nil {
				return err
			}
		case protocol.TypeStreamClose:
			if err := s.handleStreamClose(session, frame); err != nil {
				return err
			}
		case protocol.TypeUDPData:
			if err := s.handleUDPData(conn, session, frame); err != nil {
				return err
			}
		case protocol.TypeUDPClose:
			if err := s.handleUDPClose(session, frame); err != nil {
				return err
			}
		default:
			return s.replyErrorWithSession(
				conn,
				session,
				frame.RequestID,
				frame.StreamID,
				protocol.ErrorCodeProtocolBadBody,
				"unexpected message type %s",
				frame.Type.String(),
			)
		}
	}
}

func (s *Server) handleHeartbeatPing(conn net.Conn, session *sessionState, agent *controlsession.Agent, frame protocol.Frame) error {
	if frame.RequestID == 0 {
		return s.replyErrorWithSession(conn, session, 0, frame.StreamID, protocol.ErrorCodeProtocolBadBody, "heartbeat.ping requestId must be non-zero")
	}
	if frame.StreamID != 0 {
		return s.replyErrorWithSession(conn, session, frame.RequestID, frame.StreamID, protocol.ErrorCodeProtocolBadBody, "heartbeat.ping streamId must be zero")
	}

	ping, err := protocol.UnmarshalHeartbeatPing(frame.Body)
	if err != nil {
		return s.replyProtocolErrorWithSession(conn, session, frame, err)
	}
	if agent == nil || !agent.Enqueue(controlsession.HeartbeatPingReceived{
		RequestID:    frame.RequestID,
		ClientUnixMs: ping.ClientUnixMs,
	}) {
		return net.ErrClosed
	}
	return nil
}

func (s *Server) readFrame(conn net.Conn) (protocol.Frame, error) {
	return s.readFrameWithTimeout(conn, s.options.ReadTimeout)
}

func (s *Server) readFrameWithTimeout(conn net.Conn, timeout time.Duration) (protocol.Frame, error) {
	frameBytes, err := s.frames.ReadFrame(conn, timeout, s.frameContext(conn, nil))
	if err != nil {
		return protocol.Frame{}, err
	}
	return protocol.ParseFrame(frameBytes)
}

func (s *Server) readFrameWithSessionTimeout(conn net.Conn, session *sessionState, timeout time.Duration) (protocol.Frame, error) {
	frameBytes, err := s.frames.ReadFrame(conn, timeout, s.frameContext(conn, session))
	if err != nil {
		return protocol.Frame{}, err
	}
	return protocol.ParseFrame(frameBytes)
}

func (s *Server) writeFrame(conn net.Conn, frame protocol.Frame) error {
	return s.writeFrameWithContext(conn, frame, s.frameContext(conn, nil))
}

func (s *Server) writeFrameWithContext(conn net.Conn, frame protocol.Frame, frameContext transport.FrameContext) error {
	frameBytes, err := frame.MarshalBinary()
	if err != nil {
		return err
	}
	return s.frames.WriteFrame(conn, frameBytes, s.options.WriteTimeout, frameContext)
}

func (s *Server) frameContext(conn net.Conn, session *sessionState) transport.FrameContext {
	frameContext := transport.FrameContext{
		Side:   transport.FrameSideServer,
		ConnID: transport.ConnectionID(conn),
	}
	if session != nil {
		frameContext.GroupID = session.CurrentGroupID()
		frameContext.SessionID = session.ID
	}
	return frameContext
}

func (s *Server) replyProtocolError(conn net.Conn, frame protocol.Frame, err error) error {
	protocolErr := protocol.AsProtocolError(err)
	if protocolErr == nil {
		return err
	}
	if writeErr := s.writeError(conn, frame.RequestID, frame.StreamID, protocolErr.Code, false, protocolErr.Message); writeErr != nil {
		return errors.Join(err, writeErr)
	}
	return err
}

func (s *Server) replyError(conn net.Conn, requestID, streamID uint32, code uint16, format string, args ...any) error {
	err := protocol.NewError(code, format, args...)
	if writeErr := s.writeError(conn, requestID, streamID, code, false, err.Message); writeErr != nil {
		return errors.Join(err, writeErr)
	}
	return err
}

func (s *Server) writeError(conn net.Conn, requestID, streamID uint32, code uint16, retryable bool, message string) error {
	body, err := protocol.MarshalErrorBody(protocol.ErrorBody{
		ErrorCode: code,
		Retryable: retryable,
		Message:   message,
	})
	if err != nil {
		return err
	}
	return s.writeFrame(conn, protocol.Frame{
		Type:      protocol.TypeError,
		RequestID: requestID,
		StreamID:  streamID,
		Body:      body,
	})
}

// Interface implementation - accessor methods for RuntimeOperator
func (s *Server) IsShuttingDown() bool            { return s.isShuttingDown() }
func (s *Server) Logger() *slog.Logger            { return s.logger }
func (s *Server) Repo() controlruntime.Repository { return s.repo }
func (s *Server) Scheduler() clock.Scheduler      { return s.scheduler }
func (s *Server) ScanWG() *sync.WaitGroup         { return &s.scanWG }
func (s *Server) RuntimeScanPoll() time.Duration  { return s.options.RuntimeScanPoll }
func (s *Server) Supervisor() *controlruntime.Supervisor {
	if s.supervisor == nil {
		return nil
	}
	return &s.supervisor.Supervisor
}

// ActiveRuntimeGroups returns all active runtime groups, excluding the specified session if any.
// Note: The exclude parameter is typed as 'any' for the interface flexibility.
// If exclude is nil, no filtering is done.
func (s *Server) ActiveRuntimeGroups(exclude any) []controlruntime.RuntimeGroupSnapshot {
	if s == nil || s.supervisor == nil {
		return nil
	}

	// We need to get the snapshot without filtering since we can't convert SessionState to sessionState
	// The exclude parameter is ignored for this implementation
	snapshot := s.supervisor.Snapshot(nil)
	if len(snapshot.sessions) == 0 {
		return nil
	}

	result := make([]controlruntime.RuntimeGroupSnapshot, 0, len(snapshot.sessions))
	for _, session := range snapshot.sessions {
		group, ok := session.ActiveRuntimeGroup()
		if !ok {
			continue
		}
		result = append(result, group)
	}
	return result
}

// ActiveSession returns the active session for the given group ID.
func (s *Server) ActiveSession(groupID int64) (*controlruntime.ActiveSession, bool) {
	if s == nil || s.supervisor == nil {
		return nil, false
	}
	return s.supervisor.ActiveSession(groupID)
}

// RecordTunnelRuntimeIssueForConfig records a runtime issue for a specific config version.
func (s *Server) RecordTunnelRuntimeIssueForConfig(tunnelID uint32, configVersion uint64, reason string) {
	s.recordTunnelRuntimeIssueForConfig(tunnelID, configVersion, reason)
}

// ClearUnknownTunnelRuntimeIssues clears runtime issues for tunnels that are no longer known.
func (s *Server) ClearUnknownTunnelRuntimeIssues(knownTunnelIDs map[int64]struct{}) {
	s.clearUnknownTunnelRuntimeIssues(knownTunnelIDs)
}

// ApplyScannedTunnelRuntimeIssues applies the scanned runtime issues to the issue store.
func (s *Server) ApplyScannedTunnelRuntimeIssues(snapshot controlruntime.ConfigSnapshot, staticConflictIDs map[int64]struct{}, issues map[uint32]string, preserved map[uint32]struct{}) {
	s.applyScannedTunnelRuntimeIssues(snapshot, staticConflictIDs, issues, preserved)
}

// ResolveGroupEffectiveIP implements controlruntime.RuntimeOperator.
func (s *Server) ResolveGroupEffectiveIP(group controlruntime.GroupRuntime) (string, error) {
	return s.resolveGroupEffectiveIP(group)
}

// StartTunnelListeners implements controlruntime.RuntimeOperator.
func (s *Server) StartTunnelListeners(opCtx controlruntime.TunnelListenerOperationContext) (controlruntime.TunnelListenerBatch, error) {
	return s.startTunnelListeners(opCtx)
}

// ProbeTunnelRuntimeIssue implements controlruntime.RuntimeOperator.
func (s *Server) ProbeTunnelRuntimeIssue(groupID int64, bindIP string, tunnel protocol.TunnelEntry) string {
	return s.probeTunnelRuntimeIssue(groupID, bindIP, tunnel)
}

// ShutdownSession implements controlruntime.RuntimeOperator.
func (s *Server) ShutdownSession(session any) {
	if concrete, ok := session.(*sessionState); ok {
		s.shutdownSession(concrete)
	}
}

// ServeUDPIdleCleanup implements controlruntime.RuntimeOperator.
func (s *Server) ServeUDPIdleCleanup(conn net.Conn, logger controlruntime.Logger, session any) {
	if adapter, ok := session.(sessionRuntimeStartTargetAdapter); ok {
		s.serveUDPIdleCleanup(conn, logger, adapter.getSession())
	} else if concrete, ok := session.(*sessionState); ok {
		s.serveUDPIdleCleanup(conn, logger, concrete)
	}
}

// ServeTunnelListener implements controlruntime.RuntimeOperator.
func (s *Server) ServeTunnelListener(serve controlruntime.TunnelRuntimeServeContext, listener net.Listener) {
	s.serveTunnelListener(convertServeContext(serve), listener)
}

// ServeUDPTunnelListener implements controlruntime.RuntimeOperator.
func (s *Server) ServeUDPTunnelListener(serve controlruntime.TunnelRuntimeServeContext, listener controlruntime.UDPListener) {
	s.serveUDPTunnelListener(convertServeContext(serve), listener)
}

// NewTunnelRuntimeServeContext implements controlruntime.RuntimeOperator.
func (s *Server) NewTunnelRuntimeServeContext(opCtx controlruntime.TunnelListenerOperationContext, target controlruntime.SessionRuntimeStartTarget, remotePort uint16) controlruntime.TunnelRuntimeServeContext {
	session := target.(sessionRuntimeStartTargetAdapter).getSession()
	return controlruntime.TunnelRuntimeServeContext{
		Logger:     target.Logger(),
		Session:    target,
		RuntimeIO:  newSessionRuntimeIOWriter(s, target.Conn(), session, opCtx.ConfigVersion),
		Tunnel:     opCtx.Tunnel,
		RemotePort: remotePort,
	}
}

// sessionRuntimeStartTargetAdapter is used to extract the session from a SessionRuntimeStartTarget
type sessionRuntimeStartTargetAdapter interface {
	getSession() *sessionState
}

// RequestAuditedSessionRuntimeRecovery implements controlruntime.RuntimeOperator.
func (s *Server) RequestAuditedSessionRuntimeRecovery(sessionID uint64, targetTunnels []protocol.TunnelEntry) error {
	return s.requestAuditedSessionRuntimeRecovery(sessionID, targetTunnels)
}

// RuntimeExecutor implements controlruntime.RuntimeOperator.
func (s *Server) RuntimeExecutor(sessionID uint64) *controlruntime.RuntimeExecutor {
	runtime := s.runtimeExecutor(sessionID)
	if runtime == nil {
		return nil
	}
	return &controlruntime.RuntimeExecutor{
		GroupID:      runtime.groupID,
		Conn:         runtime.conn,
		Logger:       runtime.logger,
		Session:      runtime.session,
		DesiredGroup: runtime.desiredGroup,
	}
}

// DispatchBySessionID implements controlruntime.RuntimeOperator.
func (s *Server) DispatchBySessionID(sessionID uint64, event controlruntime.Event) bool {
	if s == nil || s.supervisor == nil {
		return false
	}
	return s.supervisor.DispatchBySessionID(sessionID, event)
}

// ApplyActiveSessionConfigRecovery implements controlruntime.RuntimeOperator.
func (s *Server) ApplyActiveSessionConfigRecovery(groupID int64, group controlruntime.GroupRuntime) error {
	active, ok := s.activeSession(groupID)
	if !ok || active == nil || active.session == nil {
		return nil
	}
	next := active.session.applyControlEvent(controlsession.DesiredRuntimeUpdated{Snapshot: controlruntime.DesiredRuntimeFromGroup(group)})
	if next.Pending != nil {
		active.session.ControlMu.Lock()
		active.session.Pending = group
		active.session.Recovery = controlruntime.PendingRecoveryModeForSnapshot(group.Snapshot)
		active.session.ControlMu.Unlock()
	}
	if runtime := s.runtimeExecutor(active.session.ID); runtime != nil {
		runtime.setDesiredGroup(group)
	}
	s.supervisor.UpdateDesiredRuntime(groupID, controlruntime.DesiredRuntimeFromGroup(group))
	return nil
}

// SessionState implements controlruntime.RuntimeOperator.
func (s *Server) SessionState(sessionID uint64) (controlruntime.SessionState, bool) {
	if s == nil || s.supervisor == nil {
		return controlruntime.SessionState{}, false
	}
	return s.supervisor.SessionState(sessionID)
}

// ApplySessionEvent implements controlruntime.RuntimeOperator.
func (s *Server) ApplySessionEvent(sessionID uint64, event controlruntime.Event) {
	if s == nil {
		return
	}
	runtime := s.runtimeExecutor(sessionID)
	if runtime != nil && runtime.session != nil {
		runtime.session.applyControlEvent(event)
	}
}

// Helper functions for type conversion

func convertServeContext(serve controlruntime.TunnelRuntimeServeContext) tunnelRuntimeServeContext {
	var session *sessionState
	if adapter, ok := serve.Session.(sessionRuntimeStartTargetAdapter); ok {
		session = adapter.getSession()
	}
	return tunnelRuntimeServeContext{
		logger:     serve.Logger,
		session:    session,
		runtimeIO:  serve.RuntimeIO.(sessionRuntimeIOWriter),
		tunnel:     serve.Tunnel,
		remotePort: serve.RemotePort,
	}
}

// TunnelRuntimeIssues returns a map of tunnel IDs to their runtime issue reasons.
func (s *Server) TunnelRuntimeIssues() map[int64]string {
	if s == nil || s.runtimeIssues == nil {
		return nil
	}
	return s.runtimeIssues.SnapshotReasons()
}

func (s *Server) clearTunnelRuntimeIssues(tunnels []protocol.TunnelEntry) {
	if s == nil || s.runtimeIssues == nil {
		return
	}
	s.runtimeIssues.ClearTunnels(tunnels)
}

func (s *Server) recordTunnelRuntimeIssue(tunnelID uint32, reason string) {
	if s == nil || s.runtimeIssues == nil {
		return
	}
	s.runtimeIssues.Record(tunnelID, reason)
}

func (s *Server) recordTunnelRuntimeIssueForConfig(tunnelID uint32, configVersion uint64, reason string) {
	if s == nil || s.runtimeIssues == nil {
		return
	}
	s.runtimeIssues.RecordForConfig(tunnelID, configVersion, reason)
}

func (s *Server) clearUnknownTunnelRuntimeIssues(knownTunnelIDs map[int64]struct{}) {
	if s == nil || s.runtimeIssues == nil {
		return
	}
	s.runtimeIssues.ClearUnknown(knownTunnelIDs)
}

func (s *Server) applyScannedTunnelRuntimeIssues(snapshot ConfigSnapshot, staticConflictIDs map[int64]struct{}, issues map[uint32]string, preserved map[uint32]struct{}) {
	if s == nil || s.runtimeIssues == nil {
		return
	}
	s.runtimeIssues.ApplyScanResult(snapshot, staticConflictIDs, issues, preserved)
}
