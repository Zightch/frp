package wiring

import (
	"context"

	controlruntime "github.com/zightch/frp/frps/internal/control/runtime"
	controlsession "github.com/zightch/frp/frps/internal/control/session"
	controlsupervisor "github.com/zightch/frp/frps/internal/control/session/supervisor"
)

type Supervisor struct {
	registry *controlsupervisor.Supervisor
}

type supervisorSnapshot struct {
	groupSlots map[int64]uint64
	sessions   []controlruntime.SessionSnapshot
}

func NewSupervisor(executor controlsession.Executor) *Supervisor {
	return &Supervisor{registry: controlsupervisor.New(executor)}
}

func (s *Supervisor) AttachSession(parent context.Context, initial controlsession.SessionState, runtime *runtimeExecutor) *controlsession.Agent {
	if s == nil || s.registry == nil {
		return nil
	}
	return s.registry.AttachSession(parent, initial, runtime)
}

// AttachRuntimeSession implements controlruntime.SupervisorOperator for projected runtime tests.
func (s *Supervisor) AttachRuntimeSession(parent context.Context, state controlsession.SessionState, runtime *controlruntime.RuntimeExecutor) *controlsession.Agent {
	if s == nil || runtime == nil {
		return nil
	}
	session, ok := runtime.Session.(*sessionState)
	if !ok || session == nil {
		return nil
	}
	localRuntime := &runtimeExecutor{
		groupID:      runtime.GroupID,
		conn:         runtime.Conn,
		logger:       runtime.Logger,
		session:      session,
		desiredGroup: runtime.DesiredGroup,
	}
	return s.AttachSession(parent, state, localRuntime)
}

func (s *Supervisor) DispatchBySessionID(sessionID uint64, event controlsession.Event) bool {
	if s == nil || s.registry == nil {
		return false
	}
	return s.registry.DispatchBySessionID(sessionID, event)
}

func (s *Supervisor) UpdateDesiredRuntime(groupID int64, snapshot controlsession.DesiredRuntimeSnapshot) bool {
	if s == nil || s.registry == nil {
		return false
	}
	return s.registry.UpdateDesiredRuntime(groupID, snapshot)
}

func (s *Supervisor) NotifyNetworkChange() {
	if s == nil || s.registry == nil {
		return
	}
	s.registry.NotifyNetworkChange()
}

func (s *Supervisor) SessionState(sessionID uint64) (controlsession.SessionState, bool) {
	if s == nil || s.registry == nil {
		return controlsession.SessionState{}, false
	}
	return s.registry.SessionState(sessionID)
}

func (s *Supervisor) RuntimeExecutor(sessionID uint64) *runtimeExecutor {
	if s == nil || s.registry == nil || sessionID == 0 {
		return nil
	}
	runtime, _ := s.registry.Runtime(sessionID).(*runtimeExecutor)
	return runtime
}

func (s *Supervisor) DetachRuntime(sessionID uint64) {
	if s == nil || s.registry == nil {
		return
	}
	s.registry.DetachRuntime(sessionID)
}

func (s *Supervisor) activeSession(groupID int64) (*activeSession, bool) {
	if s == nil || s.registry == nil || groupID <= 0 {
		return nil, false
	}

	active, ok := s.registry.ActiveRuntime(groupID)
	if !ok {
		return nil, false
	}
	session, ok := active.Session.(*sessionState)
	if !ok || session == nil {
		return nil, false
	}
	return &activeSession{
		conn:    active.Conn,
		session: session,
	}, true
}

func (s *Supervisor) ActiveSession(groupID int64) (*controlruntime.ActiveSession, bool) {
	if s == nil || s.registry == nil {
		return nil, false
	}
	return s.registry.ActiveSession(groupID)
}

func (s *Supervisor) ActiveRuntimeGroups(exclude controlruntime.SessionStateProjectionTarget) []controlruntime.RuntimeGroupSnapshot {
	if s == nil || s.registry == nil {
		return nil
	}
	return s.registry.ActiveRuntimeGroups(exclude)
}

func (s *Supervisor) ReserveGroupSlot(groupID int64, sessionID uint64) bool {
	if s == nil || s.registry == nil {
		return false
	}
	return s.registry.ReserveGroupSlot(groupID, sessionID)
}

func (s *Supervisor) ReleaseGroupSlot(groupID int64, sessionID uint64) {
	if s == nil || s.registry == nil {
		return
	}
	s.registry.ReleaseGroupSlot(groupID, sessionID)
}

func (s *Supervisor) Snapshot(exclude *sessionState) supervisorSnapshot {
	if s == nil || s.registry == nil {
		return supervisorSnapshot{}
	}

	var excludeTarget controlruntime.SessionStateProjectionTarget
	if exclude != nil {
		excludeTarget = exclude
	}
	snapshot := s.registry.Snapshot(excludeTarget)
	return supervisorSnapshot{
		groupSlots: snapshot.GroupSlots,
		sessions:   snapshot.Sessions,
	}
}

func (s *Supervisor) Shutdown() {
	if s == nil || s.registry == nil {
		return
	}
	s.registry.Shutdown()
}
