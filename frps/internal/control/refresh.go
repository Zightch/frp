package control

import (
	"errors"
	"net"

	"github.com/zightch/frp/frps/pkg/protocol"
)

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

	currentGroup, _ := active.session.currentGroupAndSnapshot()
	if currentGroup.ClientSecretHash != group.ClientSecretHash {
		s.logger.Info("closing active session after proxy group credential rotation", "group_id", groupID, "session_id", active.session.ID)
		_ = active.conn.Close()
		return
	}

	coordinator := s.runtimeCoordinator()
	target := newRuntimeRecoveryTarget(
		active.conn,
		s.logger.With(
			"session_id", active.session.ID,
			"group_id", group.ID,
			"group_name", group.Name,
		),
		active.session,
	)
	plan := coordinator.planRefresh(target, group)
	switch plan.action {
	case runtimeRecoveryActionNoop:
		message := "deduplicating refresh against matching pending config"
		if len(plan.snapshot.Tunnels) == 0 {
			message = "deduplicating refresh against matching pending empty config"
		}
		s.logger.Info(message, "group_id", groupID, "session_id", active.session.ID, "config_version", plan.snapshot.Version)
		return
	case runtimeRecoveryActionCloseSession:
		s.logger.Info("closing active session because a previous config update is still pending", "group_id", groupID, "session_id", active.session.ID)
		_ = active.conn.Close()
		return
	case runtimeRecoveryActionRebindRuntime:
		s.logger.Info(
			"rebinding active group runtime after effective_ip change",
			"group_id", groupID,
			"session_id", active.session.ID,
			"old_effective_ip", currentGroup.EffectiveIP,
			"new_effective_ip", group.EffectiveIP,
		)
	case runtimeRecoveryActionPushEmptyConfig:
		if desiredSnapshot := runtimeSnapshotForGroup(group); len(desiredSnapshot.Tunnels) != 0 {
			s.logger.Info(
				"shrinking active group runtime to empty config after effective_ip became unavailable",
				"group_id", groupID,
				"session_id", active.session.ID,
				"effective_ip", group.EffectiveIP,
			)
		}
	}

	if err := coordinator.execute(target, plan); err != nil {
		switch plan.action {
		case runtimeRecoveryActionRebindRuntime:
			s.logger.Warn("rebind active group runtime failed", "group_id", groupID, "session_id", active.session.ID, "error", err)
		case runtimeRecoveryActionPushEmptyConfig:
			if errors.Is(err, errConfigUpdateInFlight) {
				s.logger.Info("closing active session because a previous config update is still pending", "group_id", groupID, "session_id", active.session.ID)
			}
			message := "push empty config failed"
			if desiredSnapshot := runtimeSnapshotForGroup(group); len(desiredSnapshot.Tunnels) != 0 {
				message = "push empty config after effective_ip refresh failed"
			}
			s.logger.Warn(message, "group_id", groupID, "session_id", active.session.ID, "error", err)
		case runtimeRecoveryActionPushFullConfig:
			if errors.Is(err, errConfigUpdateInFlight) {
				s.logger.Info("closing active session because a previous config update is still pending", "group_id", groupID, "session_id", active.session.ID)
			}
			s.logger.Warn("push refreshed config failed", "group_id", groupID, "session_id", active.session.ID, "error", err)
		default:
			s.logger.Warn("refresh active group runtime failed", "group_id", groupID, "session_id", active.session.ID, "action", plan.action.String(), "error", err)
		}
		_ = active.conn.Close()
		return
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

func (s *Server) rebindGroupRuntime(conn net.Conn, session *sessionState, group GroupRuntime) error {
	if err := s.freezeGroupRuntime(conn, session); err != nil {
		return err
	}
	session.replaceGroupRuntime(group)
	session.allowTunnelRuntimeStart()
	logger := s.logger.With(
		"session_id", session.ID,
		"group_id", group.ID,
		"group_name", group.Name,
	)
	return s.ensureTunnelListeners(conn, logger, session)
}

func (s *Server) runtimeRefreshSnapshot(group GroupRuntime, snapshot ConfigSnapshot) (ConfigSnapshot, error, bool) {
	enabled := enabledTunnels(snapshot)
	if len(enabled) == 0 {
		return snapshot, nil, false
	}

	if _, err := s.resolveGroupEffectiveIP(group); err != nil {
		s.clearTunnelRuntimeIssues(group.Snapshot.Tunnels)
		reason := buildGroupEffectiveIPRuntimeReason(group, err)
		for _, tunnel := range enabled {
			s.recordTunnelRuntimeIssueForConfig(tunnel.TunnelID, snapshot.Version, reason)
		}
		return emptyConfigSnapshot(snapshot), err, true
	}

	return snapshot, nil, false
}

func emptyConfigSnapshot(snapshot ConfigSnapshot) ConfigSnapshot {
	snapshot.Tunnels = nil
	return snapshot
}

func samePushedConfigSnapshot(current, next ConfigSnapshot) bool {
	return current.Version == next.Version &&
		current.GeneratedAtMs == next.GeneratedAtMs &&
		sameRuntimeSnapshot(current, next)
}

func sameRuntimeSnapshot(current, next ConfigSnapshot) bool {
	return sameTunnelEntries(current.Tunnels, next.Tunnels)
}

func sameTunnelEntries(left, right []protocol.TunnelEntry) bool {
	if len(left) == 0 && len(right) == 0 {
		return true
	}
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if !sameTunnelEntry(left[index], right[index]) {
			return false
		}
	}
	return true
}

func sameTunnelEntry(left, right protocol.TunnelEntry) bool {
	return left.TunnelID == right.TunnelID &&
		left.Protocol == right.Protocol &&
		left.TunnelFlags == right.TunnelFlags &&
		left.RemoteStart == right.RemoteStart &&
		left.RemoteEnd == right.RemoteEnd &&
		sameHost(left.LocalHost, right.LocalHost) &&
		left.LocalStart == right.LocalStart &&
		left.LocalEnd == right.LocalEnd
}

func sameHost(left, right protocol.Host) bool {
	if left.Type != right.Type || left.Name != right.Name {
		return false
	}
	switch left.Type {
	case protocol.HostTypeIPv4, protocol.HostTypeIPv6:
		return left.IP.Equal(right.IP)
	default:
		return true
	}
}
