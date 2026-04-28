package control

import (
	"context"
	"sync"

	controlruntime "github.com/zightch/frp/frps/internal/control/runtime"
	controlsession "github.com/zightch/frp/frps/internal/control/session"
)

type Supervisor struct {
	controlruntime.Supervisor // embed runtime.Supervisor for interface satisfaction

	executor controlsession.Executor

	mu               sync.RWMutex
	byGroup          map[int64]*controlsession.Agent
	bySession        map[uint64]*controlsession.Agent
	runtimeByGroup   map[int64]*runtimeExecutor
	runtimeBySession map[uint64]*runtimeExecutor
	stateBySession   map[uint64]controlsession.SessionState
	groupSlots       map[int64]uint64
	cancelByID       map[uint64]context.CancelFunc
}

type supervisorSnapshot struct {
	groupSlots map[int64]uint64
	sessions   []controlruntime.SessionSnapshot
}

func NewSupervisor(executor controlsession.Executor) *Supervisor {
	return &Supervisor{
		executor:         executor,
		byGroup:          make(map[int64]*controlsession.Agent),
		bySession:        make(map[uint64]*controlsession.Agent),
		runtimeByGroup:   make(map[int64]*runtimeExecutor),
		runtimeBySession: make(map[uint64]*runtimeExecutor),
		stateBySession:   make(map[uint64]controlsession.SessionState),
		groupSlots:       make(map[int64]uint64),
		cancelByID:       make(map[uint64]context.CancelFunc),
	}
}

func (s *Supervisor) AttachSession(parent context.Context, initial controlsession.SessionState, runtime *runtimeExecutor) *controlsession.Agent {
	if s == nil {
		return nil
	}
	if parent == nil {
		parent = context.Background()
	}

	agent := controlsession.NewAgent(initial, s.executor)
	ctx, cancel := context.WithCancel(parent)

	s.mu.Lock()
	if existing := s.byGroup[initial.GroupID]; existing != nil {
		_ = existing.Enqueue(controlsession.SessionTakeoverRequested{ReplacementSessionID: initial.SessionID})
	}
	s.byGroup[initial.GroupID] = agent
	s.bySession[initial.SessionID] = agent
	if runtime != nil {
		s.runtimeByGroup[initial.GroupID] = runtime
		s.runtimeBySession[initial.SessionID] = runtime
	}
	s.stateBySession[initial.SessionID] = initial
	s.groupSlots[initial.GroupID] = initial.SessionID
	s.cancelByID[initial.SessionID] = cancel
	s.mu.Unlock()

	go func() {
		agent.Run(ctx)
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.byGroup[initial.GroupID] == agent {
			delete(s.byGroup, initial.GroupID)
		}
		if s.bySession[initial.SessionID] == agent {
			delete(s.bySession, initial.SessionID)
		}
		if runtime != nil {
			if s.runtimeByGroup[initial.GroupID] == runtime {
				delete(s.runtimeByGroup, initial.GroupID)
			}
			if s.runtimeBySession[initial.SessionID] == runtime {
				delete(s.runtimeBySession, initial.SessionID)
			}
		}
		delete(s.stateBySession, initial.SessionID)
		if s.groupSlots[initial.GroupID] == initial.SessionID {
			delete(s.groupSlots, initial.GroupID)
		}
		if _, ok := s.cancelByID[initial.SessionID]; ok {
			delete(s.cancelByID, initial.SessionID)
		}
	}()
	return agent
}

// AttachRuntimeSession implements controlruntime.SupervisorOperator.
// It converts a RuntimeExecutor to a runtimeExecutor and attaches the session.
func (s *Supervisor) AttachRuntimeSession(parent context.Context, state controlsession.SessionState, runtime *controlruntime.RuntimeExecutor) *controlsession.Agent {
	if s == nil || runtime == nil {
		return nil
	}
	// Convert RuntimeExecutor to runtimeExecutor
	localRuntime := &runtimeExecutor{
		groupID:      runtime.GroupID,
		conn:         runtime.Conn,
		logger:       runtime.Logger,
		session:      runtime.Session.(*sessionState),
		desiredGroup: runtime.DesiredGroup,
	}
	return s.AttachSession(parent, state, localRuntime)
}

func (s *Supervisor) DispatchBySessionID(sessionID uint64, event controlsession.Event) bool {
	if s == nil {
		return false
	}
	s.mu.RLock()
	agent := s.bySession[sessionID]
	s.mu.RUnlock()
	if agent == nil {
		return false
	}
	return agent.Enqueue(event)
}

func (s *Supervisor) UpdateDesiredRuntime(groupID int64, snapshot controlsession.DesiredRuntimeSnapshot) bool {
	if s == nil {
		return false
	}
	s.mu.RLock()
	agent := s.byGroup[groupID]
	s.mu.RUnlock()
	if agent == nil {
		return false
	}
	return agent.Enqueue(controlsession.DesiredRuntimeUpdated{Snapshot: snapshot})
}

func (s *Supervisor) NotifyNetworkChange() {
	if s == nil {
		return
	}

	s.mu.RLock()
	agents := make([]*controlsession.Agent, 0, len(s.bySession))
	for _, agent := range s.bySession {
		agents = append(agents, agent)
	}
	s.mu.RUnlock()

	for _, agent := range agents {
		_ = agent.Enqueue(controlsession.NetworkSnapshotChanged{})
	}
}

func (s *Supervisor) SessionState(sessionID uint64) (controlsession.SessionState, bool) {
	if s == nil {
		return controlsession.SessionState{}, false
	}
	s.mu.RLock()
	agent := s.bySession[sessionID]
	s.mu.RUnlock()
	if agent == nil {
		s.mu.RLock()
		state, ok := s.stateBySession[sessionID]
		s.mu.RUnlock()
		return state, ok
	}
	return agent.State(), true
}

func (s *Supervisor) RuntimeExecutor(sessionID uint64) *runtimeExecutor {
	if s == nil || sessionID == 0 {
		return nil
	}
	s.mu.RLock()
	runtime := s.runtimeBySession[sessionID]
	s.mu.RUnlock()
	return runtime
}

func (s *Supervisor) DetachRuntime(sessionID uint64) {
	if s == nil || sessionID == 0 {
		return
	}

	s.mu.Lock()
	runtime := s.runtimeBySession[sessionID]
	if runtime != nil {
		delete(s.runtimeBySession, sessionID)
		if s.runtimeByGroup[runtime.groupID] == runtime {
			delete(s.runtimeByGroup, runtime.groupID)
		}
		if s.groupSlots[runtime.groupID] == sessionID {
			delete(s.groupSlots, runtime.groupID)
		}
	}
	delete(s.stateBySession, sessionID)
	s.mu.Unlock()
}

func (s *Supervisor) activeSession(groupID int64) (*activeSession, bool) {
	if s == nil || groupID <= 0 {
		return nil, false
	}

	s.mu.RLock()
	runtime := s.runtimeByGroup[groupID]
	s.mu.RUnlock()
	if runtime == nil || runtime.conn == nil || runtime.session == nil {
		return nil, false
	}
	return &activeSession{
		conn:    runtime.conn,
		session: runtime.session,
	}, true
}

// ActiveSession implements controlruntime.SupervisorOperator.
// It returns the active session for a group as a runtime.ActiveSession.
func (s *Supervisor) ActiveSession(groupID int64) (*controlruntime.ActiveSession, bool) {
	active, ok := s.activeSession(groupID)
	if !ok {
		return nil, false
	}
	return &controlruntime.ActiveSession{
		Conn:    active.conn,
		Session: &active.session.Control,
	}, true
}

// ActiveRuntimeGroups implements controlruntime.SupervisorOperator.
// It returns all active runtime groups, optionally excluding a specific session.
func (s *Supervisor) ActiveRuntimeGroups(exclude any) []controlruntime.RuntimeGroupSnapshot {
	var excludeSession *sessionState
	if exclude != nil {
		if ss, ok := exclude.(*sessionState); ok {
			excludeSession = ss
		}
	}
	snapshot := s.Snapshot(excludeSession)
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

func (s *Supervisor) ReserveGroupSlot(groupID int64, sessionID uint64) bool {
	if s == nil || groupID <= 0 || sessionID == 0 {
		return false
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.groupSlots[groupID]; ok {
		return false
	}
	s.groupSlots[groupID] = sessionID
	return true
}

func (s *Supervisor) ReleaseGroupSlot(groupID int64, sessionID uint64) {
	if s == nil || groupID <= 0 || sessionID == 0 {
		return
	}

	s.mu.Lock()
	if s.groupSlots[groupID] == sessionID {
		delete(s.groupSlots, groupID)
	}
	s.mu.Unlock()
}

func (s *Supervisor) Snapshot(exclude *sessionState) supervisorSnapshot {
	if s == nil {
		return supervisorSnapshot{}
	}

	s.mu.RLock()
	groupSlots := make(map[int64]uint64, len(s.groupSlots))
	for groupID, sessionID := range s.groupSlots {
		groupSlots[groupID] = sessionID
	}
	entries := make([]struct {
		groupID int64
		agent   *controlsession.Agent
		state   controlsession.SessionState
		runtime *runtimeExecutor
	}, 0, len(s.runtimeBySession))
	for sessionID, runtime := range s.runtimeBySession {
		if runtime == nil || runtime.session == nil || runtime.session == exclude {
			continue
		}
		entries = append(entries, struct {
			groupID int64
			agent   *controlsession.Agent
			state   controlsession.SessionState
			runtime *runtimeExecutor
		}{
			groupID: runtime.groupID,
			agent:   s.bySession[sessionID],
			state:   s.stateBySession[sessionID],
			runtime: runtime,
		})
	}
	s.mu.RUnlock()

	snapshots := make([]controlruntime.SessionSnapshot, 0, len(entries))
	for _, entry := range entries {
		if entry.agent == nil || entry.runtime == nil {
			if entry.runtime == nil {
				continue
			}
			snapshots = append(snapshots, entry.runtime.snapshot(entry.state))
			continue
		}
		snapshots = append(snapshots, entry.runtime.snapshot(entry.agent.State()))
	}

	return supervisorSnapshot{
		groupSlots: groupSlots,
		sessions:   snapshots,
	}
}

func (s *Supervisor) Shutdown() {
	if s == nil {
		return
	}

	s.mu.Lock()
	cancels := make([]context.CancelFunc, 0, len(s.cancelByID))
	agents := make([]*controlsession.Agent, 0, len(s.bySession))
	for _, cancel := range s.cancelByID {
		cancels = append(cancels, cancel)
	}
	for _, agent := range s.bySession {
		agents = append(agents, agent)
	}
	s.mu.Unlock()

	for _, agent := range agents {
		agent.Stop()
	}
	for _, cancel := range cancels {
		cancel()
	}
}
