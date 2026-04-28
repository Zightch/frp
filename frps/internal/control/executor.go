package control

import (
	"context"
	"log/slog"
	"net"
	"sync"

	controlruntime "github.com/zightch/frp/frps/internal/control/runtime"
	controlsession "github.com/zightch/frp/frps/internal/control/session"
	controlsessionexecutor "github.com/zightch/frp/frps/internal/control/session/executor"
	"github.com/zightch/frp/frps/pkg/protocol"
)

type runtimeExecutor struct {
	groupID int64
	conn    net.Conn
	logger  *slog.Logger
	session *sessionState

	mu              sync.Mutex
	desiredGroup    GroupRuntime
	drainedStreams  map[uint32]*publicStream
	drainedSessions []*publicUDPSession
}

type serverActionExecutor struct {
	executor controlsession.Executor
}

func (s *Server) unregisterRuntimeExecutor(sessionID uint64) {
	if s == nil || s.supervisor == nil || sessionID == 0 {
		return
	}
	s.supervisor.DetachRuntime(sessionID)
}

func (s *Server) runtimeExecutor(sessionID uint64) *runtimeExecutor {
	if s == nil || s.supervisor == nil || sessionID == 0 {
		return nil
	}
	return s.supervisor.RuntimeExecutor(sessionID)
}

func (r *runtimeExecutor) desiredGroupRuntime() GroupRuntime {
	if r == nil {
		return GroupRuntime{}
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	return r.desiredGroup
}

func (r *runtimeExecutor) Conn() net.Conn {
	if r == nil {
		return nil
	}
	return r.conn
}

func (r *runtimeExecutor) Logger() controlsessionexecutor.Logger {
	if r == nil {
		return nil
	}
	return r.logger
}

func (r *runtimeExecutor) DesiredGroup() controlruntime.GroupRuntime {
	return r.desiredGroupRuntime()
}

func (r *runtimeExecutor) ActiveRuntimeTunnelIDs() map[uint32]struct{} {
	if r == nil || r.session == nil {
		return nil
	}
	return r.session.ActiveRuntimeTunnelIDs()
}

func (r *runtimeExecutor) FreezeTunnelRuntime() controlsessionexecutor.RuntimeDrain {
	if r == nil || r.session == nil {
		return controlsessionexecutor.RuntimeDrain{}
	}
	listeners, udpListeners, streams, udpSessions := r.session.freezeTunnelRuntime()
	return controlsessionexecutor.RuntimeDrain{
		TCPListeners: listeners,
		UDPListeners: udpListeners,
		Streams:      streams,
		UDPSessions:  udpSessions,
	}
}

func (r *runtimeExecutor) StoreDrained(drain controlsessionexecutor.RuntimeDrain) {
	r.storeDrained(drain.Streams, drain.UDPSessions)
}

func (r *runtimeExecutor) TakeDrainedStreams() map[uint32]*controlruntime.Stream {
	return r.takeDrainedStreams()
}

func (r *runtimeExecutor) TakeDrainedUDPSessions() []*controlruntime.UDPSession {
	return r.takeDrainedUDPSessions()
}

func (r *runtimeExecutor) AllowTunnelRuntimeStart() {
	if r == nil || r.session == nil {
		return
	}
	r.session.allowTunnelRuntimeStart()
}

func (r *runtimeExecutor) RuntimeGroupID() int64 {
	if r == nil {
		return 0
	}
	return r.groupID
}

func (r *runtimeExecutor) RuntimeConn() net.Conn {
	if r == nil {
		return nil
	}
	return r.conn
}

func (r *runtimeExecutor) RuntimeSession() controlruntime.SessionStateProjectionTarget {
	if r == nil || r.session == nil {
		return nil
	}
	return r.session
}

func (r *runtimeExecutor) RuntimeSnapshot(state controlsession.SessionState) controlruntime.SessionSnapshot {
	return r.snapshot(state)
}

func (r *runtimeExecutor) snapshot(state controlsession.SessionState) controlruntime.SessionSnapshot {
	if r == nil {
		return controlruntime.SessionSnapshot{}
	}

	_, runtimeState := r.session.observeState()
	return controlruntime.SessionSnapshot{
		GroupID:      r.groupID,
		SessionID:    state.SessionID,
		Conn:         r.conn,
		DesiredGroup: r.desiredGroupRuntime(),
		RecoveryMode: r.session.RecoveryModeValue(),
		State:        state,
		Runtime:      runtimeState,
	}
}

func (r *runtimeExecutor) setDesiredGroup(group GroupRuntime) {
	if r == nil {
		return
	}

	r.mu.Lock()
	r.desiredGroup = group
	r.mu.Unlock()
}

func (r *runtimeExecutor) storeDrained(streams map[uint32]*publicStream, udpSessions []*publicUDPSession) {
	if r == nil {
		return
	}

	r.mu.Lock()
	if len(streams) != 0 {
		if r.drainedStreams == nil {
			r.drainedStreams = make(map[uint32]*publicStream)
		}
		for streamID, stream := range streams {
			r.drainedStreams[streamID] = stream
		}
	}
	if len(udpSessions) != 0 {
		r.drainedSessions = append(r.drainedSessions, udpSessions...)
	}
	r.mu.Unlock()
}

func (r *runtimeExecutor) takeDrainedStreams() map[uint32]*publicStream {
	if r == nil {
		return nil
	}

	r.mu.Lock()
	streams := r.drainedStreams
	r.drainedStreams = nil
	r.mu.Unlock()
	return streams
}

func (r *runtimeExecutor) takeDrainedUDPSessions() []*publicUDPSession {
	if r == nil {
		return nil
	}

	r.mu.Lock()
	sessions := r.drainedSessions
	r.drainedSessions = nil
	r.mu.Unlock()
	return sessions
}

func (e serverActionExecutor) Execute(ctx context.Context, state controlsession.SessionState, action controlsession.Action) []controlsession.Event {
	if e.executor == nil {
		return nil
	}
	return e.executor.Execute(ctx, state, action)
}

func newServerActionExecutor(server *Server) serverActionExecutor {
	if server == nil {
		return serverActionExecutor{}
	}
	return serverActionExecutor{
		executor: controlsessionexecutor.New(controlsessionexecutor.Options{
			RuntimeProvider:   serverActionRuntimeProvider{server: server},
			Frames:            serverActionFrameSender{server: server},
			Resolver:          serverActionResolver{server: server},
			BindingStarter:    serverActionBindingStarter{server: server},
			StreamCloser:      serverActionStreamCloser{server: server},
			UDPCloser:         serverActionUDPCloser{server: server},
			Clock:             server.clock,
			HeartbeatInterval: server.options.HeartbeatInterval,
			Version:           server.version,
		}),
	}
}

type serverActionRuntimeProvider struct {
	server *Server
}

func (p serverActionRuntimeProvider) RuntimeForSession(sessionID uint64) controlsessionexecutor.Runtime {
	if p.server == nil {
		return nil
	}
	return p.server.runtimeExecutor(sessionID)
}

type serverActionFrameSender struct {
	server *Server
}

func (s serverActionFrameSender) WriteFrame(runtime controlsessionexecutor.Runtime, frame protocol.Frame) error {
	local, ok := localRuntimeExecutor(runtime)
	if !ok || s.server == nil {
		return net.ErrClosed
	}
	return s.server.writeFrameWithSession(local.conn, local.session, frame)
}

func (s serverActionFrameSender) WriteError(runtime controlsessionexecutor.Runtime, requestID, streamID uint32, code uint16, retryable bool, message string) error {
	local, ok := localRuntimeExecutor(runtime)
	if !ok || s.server == nil {
		return net.ErrClosed
	}
	return s.server.writeErrorWithSession(local.conn, local.session, requestID, streamID, code, retryable, message)
}

type serverActionResolver struct {
	server *Server
}

func (r serverActionResolver) ResolveGroupEffectiveIP(group controlruntime.GroupRuntime) (string, error) {
	if r.server == nil {
		return "", net.ErrClosed
	}
	return r.server.resolveGroupEffectiveIP(group)
}

type serverActionBindingStarter struct {
	server *Server
}

func (s serverActionBindingStarter) StartBindings(runtime controlsessionexecutor.Runtime) error {
	local, ok := localRuntimeExecutor(runtime)
	if !ok || s.server == nil {
		return net.ErrClosed
	}
	return s.server.ensureTunnelListeners(local.conn, local.logger, local.session)
}

func (s serverActionBindingStarter) RuntimeIssues() map[int64]string {
	if s.server == nil {
		return nil
	}
	return s.server.TunnelRuntimeIssues()
}

type serverActionStreamCloser struct {
	server *Server
}

func (s serverActionStreamCloser) SendStreamClose(runtime controlsessionexecutor.Runtime, streamID uint32, reasonCode uint16, message string) error {
	local, ok := localRuntimeExecutor(runtime)
	if !ok || s.server == nil {
		return net.ErrClosed
	}
	return s.server.sendStreamClose(local.conn, local.session, streamID, reasonCode, message)
}

type serverActionUDPCloser struct {
	server *Server
}

func (s serverActionUDPCloser) SendUDPClose(runtime controlsessionexecutor.Runtime, sessionID uint32, reasonCode uint16, message string) error {
	local, ok := localRuntimeExecutor(runtime)
	if !ok || s.server == nil {
		return net.ErrClosed
	}
	return s.server.sendUDPClose(local.conn, local.session, sessionID, reasonCode, message)
}

func localRuntimeExecutor(runtime controlsessionexecutor.Runtime) (*runtimeExecutor, bool) {
	local, ok := runtime.(*runtimeExecutor)
	if !ok || local == nil || local.session == nil || local.conn == nil {
		return nil, false
	}
	return local, true
}
