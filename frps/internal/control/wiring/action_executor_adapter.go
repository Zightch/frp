package wiring

import (
	"context"
	"net"

	controlruntime "github.com/zightch/frp/frps/internal/control/runtime"
	controlsession "github.com/zightch/frp/frps/internal/control/session"
	controlsessionexecutor "github.com/zightch/frp/frps/internal/control/session/executor"
	"github.com/zightch/frp/frps/pkg/protocol"
)

type serverActionExecutor struct {
	executor controlsession.Executor
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
			Events:            serverActionEventDispatcher{server: server},
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

type serverActionEventDispatcher struct {
	server *Server
}

func (d serverActionEventDispatcher) DispatchBySessionID(sessionID uint64, event controlsession.Event) bool {
	if d.server == nil || d.server.supervisor == nil {
		return false
	}
	return d.server.supervisor.DispatchBySessionID(sessionID, event)
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
