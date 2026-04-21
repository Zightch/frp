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

	s.mu.Lock()
	defer s.mu.Unlock()
	current, ok := s.sessions[session.Group.ID]
	if !ok || current.session != session {
		return
	}
	delete(s.sessions, session.Group.ID)
}

func (s *Server) activeSession(groupID int64) (*activeSession, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	current, ok := s.sessions[groupID]
	return current, ok
}

func (s *Server) RefreshGroup(groupID int64) {
	if s == nil || groupID <= 0 {
		return
	}

	active, ok := s.activeSession(groupID)
	if !ok || active == nil || active.conn == nil || active.session == nil {
		return
	}

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
