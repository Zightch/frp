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
	requestID := active.session.nextRequestID()
	pushBody, err := protocol.MarshalConfigPush(protocol.ConfigPush{
		ConfigVersion: snapshot.Version,
		GeneratedAtMs: snapshot.GeneratedAtMs,
		Tunnels:       snapshot.Tunnels,
	})
	if err != nil {
		s.logger.Warn("marshal refreshed config push failed", "group_id", groupID, "session_id", active.session.ID, "error", err)
		return
	}

	if err := active.session.reconfigure(group, snapshot, requestID); err != nil {
		if errors.Is(err, errConfigUpdateInFlight) {
			s.logger.Info("closing active session because a previous config update is still pending", "group_id", groupID, "session_id", active.session.ID)
			_ = active.conn.Close()
			return
		}
		s.logger.Warn("reconfigure active session failed", "group_id", groupID, "session_id", active.session.ID, "error", err)
		return
	}

	active.session.resetTunnelRuntime()
	if err := s.writeFrameWithSession(active.conn, active.session, protocol.Frame{
		Type:      protocol.TypeConfigPush,
		RequestID: requestID,
		Body:      pushBody,
	}); err != nil {
		active.session.clearPendingConfigRequest(requestID)
		s.logger.Warn("push refreshed config failed", "group_id", groupID, "session_id", active.session.ID, "error", err)
		_ = active.conn.Close()
	}
}
