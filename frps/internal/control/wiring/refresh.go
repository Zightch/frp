package wiring

import (
	"context"
	"errors"

	controlruntime "github.com/zightch/frp/frps/internal/control/runtime"
)

func (s *Server) RefreshGroup(groupID int64) {
	if s == nil || groupID <= 0 {
		return
	}

	active, ok := s.refreshGroupActiveSession(groupID)
	if !ok {
		return
	}
	group, ok := s.loadLatestRefreshGroup(groupID, active)
	if !ok {
		return
	}

	group = s.computeRuntimeRefreshGroup(group)
	s.updateActiveSessionDesiredRuntime(active, group)
}

func (s *Server) refreshGroupActiveSession(groupID int64) (*activeSession, bool) {
	active, ok := s.activeSession(groupID)
	if !ok || active == nil || active.session == nil {
		return nil, false
	}
	return active, true
}

func (s *Server) loadLatestRefreshGroup(groupID int64, active *activeSession) (GroupRuntime, bool) {
	ctx, cancel := context.WithTimeout(context.Background(), s.options.ReadTimeout)
	group, err := s.repo.LoadGroupRuntimeByID(ctx, groupID)
	cancel()
	if err != nil {
		switch {
		case errors.Is(err, ErrGroupNotFound):
			s.logger.Info("closing active session for deleted proxy group", "group_id", groupID, "session_id", active.session.ID)
			_ = active.conn.Close()
		default:
			s.logger.Warn("refresh proxy group runtime failed", "group_id", groupID, "session_id", active.session.ID, "error", err)
		}
		return GroupRuntime{}, false
	}
	return group, true
}

func (s *Server) computeRuntimeRefreshGroup(group GroupRuntime) GroupRuntime {
	snapshot, _, _ := s.runtimeRefreshSnapshot(group, controlruntime.RuntimeSnapshotForGroup(group))
	group.Snapshot = snapshot
	return group
}

func (s *Server) updateActiveSessionDesiredRuntime(active *activeSession, group GroupRuntime) {
	if active == nil || active.session == nil || s.supervisor == nil {
		return
	}
	event := active.session.PrepareDesiredGroupUpdate(group)
	s.supervisor.DispatchBySessionID(active.session.ID, event)
}

func (s *Server) runtimeRefreshSnapshot(group GroupRuntime, snapshot ConfigSnapshot) (ConfigSnapshot, error, bool) {
	enabled := controlruntime.EnabledTunnels(snapshot)
	if len(enabled) == 0 {
		return snapshot, nil, false
	}

	if _, err := s.resolveGroupEffectiveIP(group); err != nil {
		s.clearTunnelRuntimeIssues(group.Snapshot.Tunnels)
		reason := controlruntime.BuildGroupEffectiveIPRuntimeReason(group, err)
		for _, tunnel := range enabled {
			s.recordTunnelRuntimeIssueForConfig(tunnel.TunnelID, snapshot.Version, reason)
		}
		return controlruntime.EmptyConfigSnapshot(snapshot), err, true
	}

	return snapshot, nil, false
}
