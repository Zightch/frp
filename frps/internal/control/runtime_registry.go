package control

import (
	"net"
	"sync"
)

type runtimeRegistry struct {
	mu         sync.Mutex
	sessions   map[int64]*activeSession
	groupSlots map[int64]uint64
}

type runtimeRegistrySnapshot struct {
	groupSlots map[int64]uint64
	sessions   []runtimeSessionSnapshot
}

type runtimeSessionSnapshot struct {
	sessionID uint64
	conn      net.Conn
	config    observedSessionConfigState
	runtime   observedSessionRuntimeState
}

type runtimeGroupSnapshot struct {
	group    GroupRuntime
	snapshot ConfigSnapshot
}

func newRuntimeRegistry() *runtimeRegistry {
	return &runtimeRegistry{
		sessions:   make(map[int64]*activeSession),
		groupSlots: make(map[int64]uint64),
	}
}

func (r *runtimeRegistry) reserveGroupSlot(groupID int64, sessionID uint64) bool {
	if r == nil {
		return false
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	if _, ok := r.groupSlots[groupID]; ok {
		return false
	}
	r.groupSlots[groupID] = sessionID
	return true
}

func (r *runtimeRegistry) releaseGroupSlot(groupID int64, sessionID uint64) {
	if r == nil {
		return
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	if r.groupSlots[groupID] == sessionID {
		delete(r.groupSlots, groupID)
	}
}

func (r *runtimeRegistry) register(conn net.Conn, session *sessionState) {
	if r == nil || conn == nil || session == nil {
		return
	}

	r.mu.Lock()
	r.sessions[session.currentGroupID()] = &activeSession{
		conn:    conn,
		session: session,
	}
	r.mu.Unlock()
}

func (r *runtimeRegistry) unregister(session *sessionState) {
	if r == nil || session == nil {
		return
	}

	groupID := session.currentGroupID()
	for {
		r.mu.Lock()
		current, ok := r.sessions[groupID]
		if !ok || current == nil || current.session != session {
			if r.groupSlots[groupID] == session.ID {
				delete(r.groupSlots, groupID)
			}
			r.mu.Unlock()
			return
		}
		current.mu.Lock()
		if r.sessions[groupID] != current {
			current.mu.Unlock()
			r.mu.Unlock()
			continue
		}
		delete(r.sessions, groupID)
		if r.groupSlots[groupID] == session.ID {
			delete(r.groupSlots, groupID)
		}
		r.mu.Unlock()
		current.mu.Unlock()
		return
	}
}

func (r *runtimeRegistry) session(groupID int64) (*activeSession, bool) {
	if r == nil {
		return nil, false
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	current, ok := r.sessions[groupID]
	return current, ok
}

func (r *runtimeRegistry) lockSession(groupID int64) (*activeSession, bool) {
	if r == nil || groupID <= 0 {
		return nil, false
	}

	for {
		r.mu.Lock()
		current, ok := r.sessions[groupID]
		if !ok || current == nil {
			r.mu.Unlock()
			return nil, false
		}
		current.mu.Lock()
		if r.sessions[groupID] == current {
			r.mu.Unlock()
			return current, true
		}
		current.mu.Unlock()
		r.mu.Unlock()
	}
}

func (r *runtimeRegistry) snapshot() runtimeRegistrySnapshot {
	return r.snapshotExcluding(nil)
}

func (r *runtimeRegistry) snapshotExcluding(exclude *sessionState) runtimeRegistrySnapshot {
	if r == nil {
		return runtimeRegistrySnapshot{}
	}

	type sessionRef struct {
		conn    net.Conn
		session *sessionState
	}

	r.mu.Lock()
	groupSlots := make(map[int64]uint64, len(r.groupSlots))
	for groupID, sessionID := range r.groupSlots {
		groupSlots[groupID] = sessionID
	}
	sessionRefs := make([]sessionRef, 0, len(r.sessions))
	for _, active := range r.sessions {
		if active == nil || active.session == nil || active.session == exclude {
			continue
		}
		sessionRefs = append(sessionRefs, sessionRef{conn: active.conn, session: active.session})
	}
	r.mu.Unlock()

	sessions := make([]runtimeSessionSnapshot, 0, len(sessionRefs))
	for _, sessionRef := range sessionRefs {
		if sessionRef.session == nil {
			continue
		}
		configState, runtimeState := sessionRef.session.observeState()
		sessions = append(sessions, runtimeSessionSnapshot{
			sessionID: sessionRef.session.ID,
			conn:      sessionRef.conn,
			config:    configState,
			runtime:   runtimeState,
		})
	}

	return runtimeRegistrySnapshot{
		groupSlots: groupSlots,
		sessions:   sessions,
	}
}

func (r *runtimeRegistry) activeRuntimeGroups(exclude *sessionState) []runtimeGroupSnapshot {
	snapshot := r.snapshotExcluding(exclude)
	if len(snapshot.sessions) == 0 {
		return nil
	}

	result := make([]runtimeGroupSnapshot, 0, len(snapshot.sessions))
	for _, session := range snapshot.sessions {
		group, ok := session.activeRuntimeGroup()
		if !ok {
			continue
		}
		result = append(result, group)
	}
	return result
}

func (s runtimeSessionSnapshot) activeRuntimeGroup() (runtimeGroupSnapshot, bool) {
	activeTunnelIDs := s.activeRuntimeTunnelIDs()
	if len(activeTunnelIDs) == 0 {
		return runtimeGroupSnapshot{}, false
	}

	snapshot := s.config.snapshot
	snapshot.Tunnels = filterTunnelsByID(snapshot.Tunnels, activeTunnelIDs)
	if len(snapshot.Tunnels) == 0 {
		return runtimeGroupSnapshot{}, false
	}

	return runtimeGroupSnapshot{
		group:    s.config.group,
		snapshot: snapshot,
	}, true
}

func (s runtimeSessionSnapshot) activeRuntimeTunnelIDs() map[uint32]struct{} {
	if s.runtime.frozen {
		return nil
	}
	return s.runtime.activeTunnelIDs
}

func (s *Server) reserveGroupSlot(groupID int64, sessionID uint64) bool {
	if s == nil || s.runtimeRegistry == nil {
		return false
	}
	return s.runtimeRegistry.reserveGroupSlot(groupID, sessionID)
}

func (s *Server) releaseGroupSlot(groupID int64, sessionID uint64) {
	if s == nil || s.runtimeRegistry == nil {
		return
	}
	s.runtimeRegistry.releaseGroupSlot(groupID, sessionID)
}

func (s *Server) registerActiveSession(conn net.Conn, session *sessionState) {
	if s == nil || s.runtimeRegistry == nil {
		return
	}
	s.runtimeRegistry.register(conn, session)
}

func (s *Server) unregisterActiveSession(session *sessionState) {
	if s == nil || s.runtimeRegistry == nil {
		return
	}
	s.runtimeRegistry.unregister(session)
}

func (s *Server) activeSession(groupID int64) (*activeSession, bool) {
	if s == nil || s.runtimeRegistry == nil {
		return nil, false
	}
	return s.runtimeRegistry.session(groupID)
}

func (s *Server) lockCurrentActiveSession(groupID int64) (*activeSession, bool) {
	if s == nil || s.runtimeRegistry == nil {
		return nil, false
	}
	return s.runtimeRegistry.lockSession(groupID)
}

func (s *Server) activeRuntimeGroups(exclude *sessionState) []runtimeGroupSnapshot {
	if s == nil || s.runtimeRegistry == nil {
		return nil
	}
	return s.runtimeRegistry.activeRuntimeGroups(exclude)
}
