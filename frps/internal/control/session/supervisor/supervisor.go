package supervisor

import (
	"context"
	"net"
	"sync"

	controlruntime "github.com/zightch/frp/frps/internal/control/runtime"
	controlsession "github.com/zightch/frp/frps/internal/control/session"
)

// Runtime is the supervisor-facing handle for a live concrete runtime.
type Runtime interface {
	RuntimeGroupID() int64
	RuntimeConn() net.Conn
	RuntimeSession() controlruntime.SessionStateProjectionTarget
	RuntimeSnapshot(state controlsession.SessionState) controlruntime.SessionSnapshot
}

// Snapshot is the supervisor's externally observable registry state.
type Snapshot struct {
	GroupSlots map[int64]uint64
	Sessions   []controlruntime.SessionSnapshot
}

// ActiveRuntime is the concrete runtime currently attached to a group.
type ActiveRuntime struct {
	Conn    net.Conn
	Session controlruntime.SessionStateProjectionTarget
	Runtime Runtime
}

// Supervisor owns session agents, runtime handles, group slots, and cancellation.
type Supervisor struct {
	executor controlsession.Executor

	mu               sync.RWMutex
	byGroup          map[int64]*controlsession.Agent
	bySession        map[uint64]*controlsession.Agent
	runtimeByGroup   map[int64]Runtime
	runtimeBySession map[uint64]Runtime
	stateBySession   map[uint64]controlsession.SessionState
	groupSlots       map[int64]uint64
	cancelByID       map[uint64]context.CancelFunc
}

func New(executor controlsession.Executor) *Supervisor {
	return &Supervisor{
		executor:         executor,
		byGroup:          make(map[int64]*controlsession.Agent),
		bySession:        make(map[uint64]*controlsession.Agent),
		runtimeByGroup:   make(map[int64]Runtime),
		runtimeBySession: make(map[uint64]Runtime),
		stateBySession:   make(map[uint64]controlsession.SessionState),
		groupSlots:       make(map[int64]uint64),
		cancelByID:       make(map[uint64]context.CancelFunc),
	}
}

func (s *Supervisor) AttachSession(parent context.Context, initial controlsession.SessionState, runtime Runtime) *controlsession.Agent {
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
			delete(s.runtimeByGroup, initial.GroupID)
			if s.groupSlots[initial.GroupID] == initial.SessionID {
				delete(s.groupSlots, initial.GroupID)
			}
		}
		if s.bySession[initial.SessionID] == agent {
			delete(s.bySession, initial.SessionID)
			delete(s.runtimeBySession, initial.SessionID)
		}
		delete(s.stateBySession, initial.SessionID)
		delete(s.cancelByID, initial.SessionID)
	}()
	return agent
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

func (s *Supervisor) Runtime(sessionID uint64) Runtime {
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
		groupID := runtime.RuntimeGroupID()
		if active := s.runtimeByGroup[groupID]; active != nil {
			session := active.RuntimeSession()
			if session != nil && session.SessionID() == sessionID {
				delete(s.runtimeByGroup, groupID)
			}
		}
		if s.groupSlots[groupID] == sessionID {
			delete(s.groupSlots, groupID)
		}
	}
	delete(s.stateBySession, sessionID)
	s.mu.Unlock()
}

func (s *Supervisor) ActiveRuntime(groupID int64) (ActiveRuntime, bool) {
	if s == nil || groupID <= 0 {
		return ActiveRuntime{}, false
	}

	s.mu.RLock()
	runtime := s.runtimeByGroup[groupID]
	s.mu.RUnlock()
	if runtime == nil || runtime.RuntimeConn() == nil || runtime.RuntimeSession() == nil {
		return ActiveRuntime{}, false
	}
	return ActiveRuntime{
		Conn:    runtime.RuntimeConn(),
		Session: runtime.RuntimeSession(),
		Runtime: runtime,
	}, true
}

func (s *Supervisor) ActiveSession(groupID int64) (*controlruntime.ActiveSession, bool) {
	active, ok := s.ActiveRuntime(groupID)
	if !ok {
		return nil, false
	}
	state, ok := s.SessionState(active.Session.SessionID())
	if !ok {
		return nil, false
	}
	return &controlruntime.ActiveSession{
		Conn:    active.Conn,
		Session: &state,
	}, true
}

func (s *Supervisor) ActiveRuntimeGroups(exclude controlruntime.SessionStateProjectionTarget) []controlruntime.RuntimeGroupSnapshot {
	snapshot := s.Snapshot(exclude)
	if len(snapshot.Sessions) == 0 {
		return nil
	}

	result := make([]controlruntime.RuntimeGroupSnapshot, 0, len(snapshot.Sessions))
	for _, session := range snapshot.Sessions {
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

func (s *Supervisor) Snapshot(exclude controlruntime.SessionStateProjectionTarget) Snapshot {
	if s == nil {
		return Snapshot{}
	}

	var excludeSessionID uint64
	if exclude != nil {
		excludeSessionID = exclude.SessionID()
	}

	s.mu.RLock()
	groupSlots := make(map[int64]uint64, len(s.groupSlots))
	for groupID, sessionID := range s.groupSlots {
		groupSlots[groupID] = sessionID
	}
	entries := make([]struct {
		sessionID uint64
		agent     *controlsession.Agent
		state     controlsession.SessionState
		runtime   Runtime
	}, 0, len(s.runtimeBySession))
	for sessionID, runtime := range s.runtimeBySession {
		if runtime == nil {
			continue
		}
		session := runtime.RuntimeSession()
		if session == nil || session.SessionID() == excludeSessionID {
			continue
		}
		entries = append(entries, struct {
			sessionID uint64
			agent     *controlsession.Agent
			state     controlsession.SessionState
			runtime   Runtime
		}{
			sessionID: sessionID,
			agent:     s.bySession[sessionID],
			state:     s.stateBySession[sessionID],
			runtime:   runtime,
		})
	}
	s.mu.RUnlock()

	snapshots := make([]controlruntime.SessionSnapshot, 0, len(entries))
	for _, entry := range entries {
		if entry.agent == nil {
			snapshots = append(snapshots, entry.runtime.RuntimeSnapshot(entry.state))
			continue
		}
		snapshots = append(snapshots, entry.runtime.RuntimeSnapshot(entry.agent.State()))
	}

	return Snapshot{
		GroupSlots: groupSlots,
		Sessions:   snapshots,
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
