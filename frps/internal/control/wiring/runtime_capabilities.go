package wiring

import (
	"log/slog"
	"net"
	"sync"
	"time"

	"github.com/zightch/frp/frps/internal/clock"
	controlruntime "github.com/zightch/frp/frps/internal/control/runtime"
	"github.com/zightch/frp/frps/pkg/protocol"
)

var _ controlruntime.RuntimeScannerDeps = (*Server)(nil)
var _ controlruntime.RuntimeStartDeps = (*Server)(nil)
var _ controlruntime.RuntimeServeDeps = (*Server)(nil)
var _ controlruntime.RuntimeAuditedSessionRecoveryDeps = (*Server)(nil)

func (s *Server) IsShuttingDown() bool            { return s.isShuttingDown() }
func (s *Server) Logger() *slog.Logger            { return s.logger }
func (s *Server) Repo() controlruntime.Repository { return s.repo }
func (s *Server) Clock() clock.Clock              { return s.clock }
func (s *Server) Scheduler() clock.Scheduler      { return s.scheduler }
func (s *Server) ScanWG() *sync.WaitGroup         { return &s.scanWG }
func (s *Server) RuntimeScanPoll() time.Duration  { return s.options.RuntimeScanPoll }

func (s *Server) ActiveRuntimeGroups(exclude controlruntime.SessionStateProjectionTarget) []controlruntime.RuntimeGroupSnapshot {
	if s == nil || s.supervisor == nil {
		return nil
	}
	return s.supervisor.ActiveRuntimeGroups(exclude)
}

func (s *Server) ActiveSession(groupID int64) (*controlruntime.ActiveSession, bool) {
	if s == nil || s.supervisor == nil {
		return nil, false
	}
	return s.supervisor.ActiveSession(groupID)
}

func (s *Server) ResolveGroupEffectiveIP(group controlruntime.GroupRuntime) (string, error) {
	return s.resolveGroupEffectiveIP(group)
}

func (s *Server) StartTunnelListeners(opCtx controlruntime.TunnelListenerOperationContext) (controlruntime.TunnelListenerBatch, error) {
	return s.startTunnelListeners(opCtx)
}

func (s *Server) ProbeTunnelRuntimeIssue(groupID int64, bindIP string, tunnel protocol.TunnelEntry) string {
	return s.probeTunnelRuntimeIssue(groupID, bindIP, tunnel)
}

func (s *Server) ShutdownSession(session controlruntime.SessionRuntimeStartTarget) {
	if adapter, ok := session.(sessionRuntimeStartTargetAdapter); ok {
		s.shutdownSession(adapter.getSession())
	}
}

func (s *Server) ServeUDPIdleCleanup(conn net.Conn, logger controlruntime.Logger, session controlruntime.SessionRuntimeStartTarget) {
	if adapter, ok := session.(sessionRuntimeStartTargetAdapter); ok {
		s.serveUDPIdleCleanup(conn, logger, adapter.getSession())
	}
}

func (s *Server) ServeTunnelListener(serve controlruntime.TunnelRuntimeServeContext, listener net.Listener) {
	s.serveTunnelListener(convertServeContext(serve), listener)
}

func (s *Server) ServeUDPTunnelListener(serve controlruntime.TunnelRuntimeServeContext, listener controlruntime.UDPListener) {
	s.serveUDPTunnelListener(convertServeContext(serve), listener)
}

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

type sessionRuntimeStartTargetAdapter interface {
	getSession() *sessionState
}

func (s *Server) RequestAuditedSessionRuntimeRecovery(sessionID uint64, targetTunnels []protocol.TunnelEntry) error {
	return s.requestAuditedSessionRuntimeRecovery(sessionID, targetTunnels)
}

func (s *Server) RuntimeExecutor(sessionID uint64) *controlruntime.RuntimeExecutor {
	runtime := s.runtimeExecutor(sessionID)
	if runtime == nil {
		return nil
	}
	return &controlruntime.RuntimeExecutor{
		GroupID: runtime.groupID,
		Conn:    runtime.conn,
		Logger:  runtime.logger,
		Session: runtime.session,
	}
}

func (s *Server) DispatchBySessionID(sessionID uint64, event controlruntime.Event) bool {
	if s == nil || s.supervisor == nil {
		return false
	}
	return s.supervisor.DispatchBySessionID(sessionID, event)
}

func (s *Server) ApplyActiveSessionConfigRecovery(groupID int64, group controlruntime.GroupRuntime) error {
	active, ok := s.activeSession(groupID)
	if !ok || active == nil || active.session == nil {
		return nil
	}
	if s.supervisor == nil {
		return nil
	}
	event := active.session.PrepareDesiredGroupUpdate(group)
	s.supervisor.DispatchBySessionID(active.session.ID, event)
	return nil
}

func (s *Server) SessionState(sessionID uint64) (controlruntime.SessionState, bool) {
	if s == nil || s.supervisor == nil {
		return controlruntime.SessionState{}, false
	}
	return s.supervisor.SessionState(sessionID)
}

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
