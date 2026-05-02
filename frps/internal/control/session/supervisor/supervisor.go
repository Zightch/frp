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
	RuntimeSnapshot() controlruntime.SessionSnapshot
}

type runtimeStateObserver interface {
	SyncControlState(state controlsession.SessionState)
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
	groupSlots       map[int64]uint64
	groupSlotIPs     map[int64]string
	cancelByID       map[uint64]context.CancelFunc
}

func New(executor controlsession.Executor) *Supervisor {
	return &Supervisor{
		executor:         executor,
		byGroup:          make(map[int64]*controlsession.Agent),
		bySession:        make(map[uint64]*controlsession.Agent),
		runtimeByGroup:   make(map[int64]Runtime),
		runtimeBySession: make(map[uint64]Runtime),
		groupSlots:       make(map[int64]uint64),
		groupSlotIPs:     make(map[int64]string),
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

	var agent *controlsession.Agent
	observer := func(state controlsession.SessionState) {
		if runtimeObserver, ok := runtime.(runtimeStateObserver); ok {
			runtimeObserver.SyncControlState(state)
		}
	}

	agent = controlsession.NewAgentWithObserver(initial, s.executor, observer)
	ctx, cancel := context.WithCancel(parent)

	s.mu.Lock()
	slotSessionID, slotReserved := s.groupSlots[initial.GroupID]
	switch {
	case !slotReserved:
		s.groupSlots[initial.GroupID] = initial.SessionID
		if remoteIP := runtimeRemoteIP(runtime); remoteIP != "" {
			s.groupSlotIPs[initial.GroupID] = remoteIP
		}
	case slotSessionID != initial.SessionID:
		s.mu.Unlock()
		cancel()
		return nil
	}
	if s.groupSlotIPs[initial.GroupID] == "" {
		if remoteIP := runtimeRemoteIP(runtime); remoteIP != "" {
			s.groupSlotIPs[initial.GroupID] = remoteIP
		}
	}
	if existing := s.byGroup[initial.GroupID]; existing != nil {
		if !slotReserved && s.groupSlots[initial.GroupID] == initial.SessionID {
			delete(s.groupSlots, initial.GroupID)
			delete(s.groupSlotIPs, initial.GroupID)
		}
		s.mu.Unlock()
		cancel()
		return nil
	}
	if existing := s.bySession[initial.SessionID]; existing != nil {
		if !slotReserved && s.groupSlots[initial.GroupID] == initial.SessionID {
			delete(s.groupSlots, initial.GroupID)
			delete(s.groupSlotIPs, initial.GroupID)
		}
		s.mu.Unlock()
		cancel()
		return nil
	}
	s.byGroup[initial.GroupID] = agent
	s.bySession[initial.SessionID] = agent
	if runtime != nil {
		s.runtimeByGroup[initial.GroupID] = runtime
		s.runtimeBySession[initial.SessionID] = runtime
	}
	s.cancelByID[initial.SessionID] = cancel
	s.mu.Unlock()

	observer(initial)

	go func() {
		agent.Run(ctx)
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.byGroup[initial.GroupID] == agent {
			delete(s.byGroup, initial.GroupID)
			delete(s.runtimeByGroup, initial.GroupID)
			if s.groupSlots[initial.GroupID] == initial.SessionID {
				delete(s.groupSlots, initial.GroupID)
				delete(s.groupSlotIPs, initial.GroupID)
			}
		}
		if s.bySession[initial.SessionID] == agent {
			delete(s.bySession, initial.SessionID)
			delete(s.runtimeBySession, initial.SessionID)
		}
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
	runtime := s.runtimeBySession[sessionID]
	agent := s.bySession[sessionID]
	s.mu.RUnlock()
	if runtime != nil {
		return runtime.RuntimeSnapshot().State, true
	}
	if agent != nil {
		return agent.State(), true
	}
	return controlsession.SessionState{}, false
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
			delete(s.groupSlotIPs, groupID)
		}
	}
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
	return s.ReserveGroupSlotWithRemoteIP(groupID, sessionID, "")
}

func (s *Supervisor) ReserveGroupSlotWithRemoteIP(groupID int64, sessionID uint64, remoteIP string) bool {
	if s == nil || groupID <= 0 || sessionID == 0 {
		return false
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.groupSlots[groupID]; ok {
		return false
	}
	s.groupSlots[groupID] = sessionID
	if remoteIP != "" {
		s.groupSlotIPs[groupID] = remoteIP
	}
	return true
}

func (s *Supervisor) ReleaseGroupSlot(groupID int64, sessionID uint64) {
	if s == nil || groupID <= 0 || sessionID == 0 {
		return
	}

	s.mu.Lock()
	if s.groupSlots[groupID] == sessionID {
		delete(s.groupSlots, groupID)
		delete(s.groupSlotIPs, groupID)
	}
	s.mu.Unlock()
}

func (s *Supervisor) GroupSlotRemoteIP(groupID int64) string {
	if s == nil || groupID <= 0 {
		return ""
	}

	s.mu.RLock()
	remoteIP := s.groupSlotIPs[groupID]
	s.mu.RUnlock()
	return remoteIP
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
		runtime Runtime
	}, 0, len(s.runtimeBySession))
	for _, runtime := range s.runtimeBySession {
		if runtime == nil {
			continue
		}
		session := runtime.RuntimeSession()
		if session == nil || session.SessionID() == excludeSessionID {
			continue
		}
		entries = append(entries, struct {
			runtime Runtime
		}{
			runtime: runtime,
		})
	}
	s.mu.RUnlock()

	snapshots := make([]controlruntime.SessionSnapshot, 0, len(entries))
	for _, entry := range entries {
		snapshots = append(snapshots, entry.runtime.RuntimeSnapshot())
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

func runtimeRemoteIP(runtime Runtime) string {
	if runtime == nil || runtime.RuntimeConn() == nil {
		return ""
	}
	return addrRemoteIP(runtime.RuntimeConn().RemoteAddr())
}

func addrRemoteIP(addr net.Addr) string {
	switch typed := addr.(type) {
	case *net.TCPAddr:
		if typed.IP != nil {
			return typed.IP.String()
		}
	case *net.UDPAddr:
		if typed.IP != nil {
			return typed.IP.String()
		}
	}
	if addr == nil {
		return ""
	}
	host, _, err := net.SplitHostPort(addr.String())
	if err == nil {
		return host
	}
	return addr.String()
}
