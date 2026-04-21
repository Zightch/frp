package control

import (
	"errors"
	"net"

	"github.com/zightch/frp/frps/pkg/protocol"
)

func (s *Server) registerActiveSession(conn net.Conn, session *sessionState) {
	if s == nil || conn == nil || session == nil {
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	s.sessions[session.Group.ID] = &activeSession{
		conn:    conn,
		session: session,
	}
}

func (s *Server) unregisterActiveSession(session *sessionState) {
	if s == nil || session == nil {
		return
	}

	groupID := session.Group.ID
	for {
		s.mu.Lock()
		current, ok := s.sessions[groupID]
		if !ok || current == nil || current.session != session {
			if s.groupSlots[groupID] == session.ID {
				delete(s.groupSlots, groupID)
			}
			s.mu.Unlock()
			return
		}
		current.mu.Lock()
		if s.sessions[groupID] != current {
			current.mu.Unlock()
			s.mu.Unlock()
			continue
		}
		delete(s.sessions, groupID)
		if s.groupSlots[groupID] == session.ID {
			delete(s.groupSlots, groupID)
		}
		s.mu.Unlock()
		current.mu.Unlock()
		return
	}
}

func (s *Server) activeSession(groupID int64) (*activeSession, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	current, ok := s.sessions[groupID]
	return current, ok
}

func (s *Server) lockCurrentActiveSession(groupID int64) (*activeSession, bool) {
	if s == nil || groupID <= 0 {
		return nil, false
	}

	for {
		s.mu.Lock()
		current, ok := s.sessions[groupID]
		if !ok || current == nil {
			s.mu.Unlock()
			return nil, false
		}
		current.mu.Lock()
		if s.sessions[groupID] == current {
			s.mu.Unlock()
			return current, true
		}
		current.mu.Unlock()
		s.mu.Unlock()
	}
}

func (s *Server) RefreshGroup(groupID int64) {
	if s == nil || groupID <= 0 {
		return
	}

	active, ok := s.lockCurrentActiveSession(groupID)
	if !ok || active == nil || active.conn == nil || active.session == nil {
		if active != nil {
			active.mu.Unlock()
		}
		return
	}
	defer active.mu.Unlock()

	group, err := s.loadGroupRuntimeByID(groupID)
	if err != nil {
		switch {
		case errors.Is(err, ErrGroupNotFound):
			s.logger.Info("closing active session for deleted proxy group", "group_id", groupID, "session_id", active.session.ID)
			_ = active.conn.Close()
		default:
			s.logger.Warn("refresh proxy group runtime failed", "group_id", groupID, "session_id", active.session.ID, "error", err)
		}
		return
	}

	currentGroup := active.session.currentGroup()
	if currentGroup.TokenHash != group.TokenHash {
		s.logger.Info("closing active session after proxy group token reset", "group_id", groupID, "session_id", active.session.ID)
		_ = active.conn.Close()
		return
	}

	snapshot := runtimeSnapshotForGroup(group)
	if active.session.hasPendingConfig() {
		s.logger.Info("closing active session because a previous config update is still pending", "group_id", groupID, "session_id", active.session.ID)
		_ = active.conn.Close()
		return
	}

	if err := s.freezeGroupRuntime(active.conn, active.session); err != nil {
		s.logger.Warn("freeze active group runtime failed", "group_id", groupID, "session_id", active.session.ID, "error", err)
		_ = active.conn.Close()
		return
	}

	if err := s.pushReloadConfig(active.conn, active.session, group, snapshot); err != nil {
		if errors.Is(err, errConfigUpdateInFlight) {
			s.logger.Info("closing active session because a previous config update is still pending", "group_id", groupID, "session_id", active.session.ID)
		}
		s.logger.Warn("push refreshed config failed", "group_id", groupID, "session_id", active.session.ID, "error", err)
		_ = active.conn.Close()
	}
}

func (s *Server) freezeGroupRuntime(conn net.Conn, session *sessionState) error {
	listeners, udpListeners, streams, udpSessions := session.freezeTunnelRuntime()
	closeStartedTunnelListeners(listeners, udpListeners)

	var freezeErr error
	for streamID, stream := range streams {
		if err := s.sendStreamClose(conn, session, streamID, protocol.CloseReasonAdminTerminated, "reload in progress"); err != nil && freezeErr == nil {
			freezeErr = err
		}
		stream.signalReady(net.ErrClosed)
		stream.close()
	}
	for _, udpSession := range udpSessions {
		if err := s.sendUDPClose(conn, session, udpSession.sessionID, protocol.CloseReasonAdminTerminated, "reload in progress"); err != nil && freezeErr == nil {
			freezeErr = err
		}
	}
	return freezeErr
}
