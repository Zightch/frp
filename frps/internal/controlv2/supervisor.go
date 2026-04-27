package controlv2

import (
	"context"
	"sync"

	"github.com/zightch/frp/frps/internal/controlv2/session"
)

type Supervisor struct {
	executor session.Executor

	mu         sync.RWMutex
	byGroup    map[int64]*session.Agent
	bySession  map[uint64]*session.Agent
	cancelByID map[uint64]context.CancelFunc
}

func NewSupervisor(executor session.Executor) *Supervisor {
	return &Supervisor{
		executor:   executor,
		byGroup:    make(map[int64]*session.Agent),
		bySession:  make(map[uint64]*session.Agent),
		cancelByID: make(map[uint64]context.CancelFunc),
	}
}

func (s *Supervisor) AttachSession(parent context.Context, initial session.SessionState, connID string) *session.Agent {
	if s == nil {
		return nil
	}
	if parent == nil {
		parent = context.Background()
	}

	agent := session.NewAgent(initial, s.executor)
	ctx, cancel := context.WithCancel(parent)

	s.mu.Lock()
	if existing := s.byGroup[initial.GroupID]; existing != nil {
		_ = existing.Enqueue(session.SessionTakeoverRequested{ReplacementSessionID: initial.SessionID})
	}
	s.byGroup[initial.GroupID] = agent
	s.bySession[initial.SessionID] = agent
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
		if s.cancelByID[initial.SessionID] == cancel {
			delete(s.cancelByID, initial.SessionID)
		}
	}()

	_ = agent.Enqueue(session.SessionAttached{ConnID: connID})
	return agent
}

func (s *Supervisor) DispatchBySessionID(sessionID uint64, event session.Event) bool {
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

func (s *Supervisor) UpdateDesiredRuntime(groupID int64, snapshot session.DesiredRuntimeSnapshot) bool {
	if s == nil {
		return false
	}
	s.mu.RLock()
	agent := s.byGroup[groupID]
	s.mu.RUnlock()
	if agent == nil {
		return false
	}
	return agent.Enqueue(session.DesiredRuntimeUpdated{Snapshot: snapshot})
}

func (s *Supervisor) NotifyNetworkChange() {
	if s == nil {
		return
	}

	s.mu.RLock()
	agents := make([]*session.Agent, 0, len(s.bySession))
	for _, agent := range s.bySession {
		agents = append(agents, agent)
	}
	s.mu.RUnlock()

	for _, agent := range agents {
		_ = agent.Enqueue(session.NetworkSnapshotChanged{})
	}
}

func (s *Supervisor) SessionState(sessionID uint64) (session.SessionState, bool) {
	if s == nil {
		return session.SessionState{}, false
	}
	s.mu.RLock()
	agent := s.bySession[sessionID]
	s.mu.RUnlock()
	if agent == nil {
		return session.SessionState{}, false
	}
	return agent.State(), true
}

func (s *Supervisor) Shutdown() {
	if s == nil {
		return
	}

	s.mu.Lock()
	cancels := make([]context.CancelFunc, 0, len(s.cancelByID))
	agents := make([]*session.Agent, 0, len(s.bySession))
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
