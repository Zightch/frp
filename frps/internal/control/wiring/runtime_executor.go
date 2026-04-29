package wiring

import (
	"log/slog"
	"net"
	"sync"

	controlruntime "github.com/zightch/frp/frps/internal/control/runtime"
	controlsession "github.com/zightch/frp/frps/internal/control/session"
	controlsessionexecutor "github.com/zightch/frp/frps/internal/control/session/executor"
)

type runtimeExecutor struct {
	groupID int64
	conn    net.Conn
	logger  *slog.Logger
	session *sessionState

	mu              sync.Mutex
	drainedStreams  map[uint32]*publicStream
	drainedSessions []*publicUDPSession
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
	if r == nil || r.session == nil {
		return controlruntime.GroupRuntime{}
	}
	return r.session.DesiredGroupRuntime()
}

func (r *runtimeExecutor) SyncControlState(state controlsession.SessionState) {
	if r == nil || r.session == nil {
		return
	}
	r.session.SyncControlState(state)
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
	listeners, udpListeners, streams, udpSessions := r.session.FreezeTunnelRuntime()
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
	r.session.AllowTunnelRuntimeStart()
}

func (r *runtimeExecutor) SessionID() uint64 {
	if r == nil || r.session == nil {
		return 0
	}
	return r.session.ID
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

func (r *runtimeExecutor) snapshot(_ controlsession.SessionState) controlruntime.SessionSnapshot {
	if r == nil {
		return controlruntime.SessionSnapshot{}
	}

	configState, runtimeState := r.session.ObserveState()
	projected := controlruntime.ProjectedSessionState(r.session, r.conn)
	return controlruntime.SessionSnapshot{
		GroupID:      r.groupID,
		SessionID:    projected.SessionID,
		Conn:         r.conn,
		Config:       configState,
		RecoveryMode: r.session.RecoveryModeValue(),
		State:        projected,
		Runtime:      runtimeState,
	}
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
