package wiring

import (
	"context"
	"errors"
	"net"

	controlruntime "github.com/zightch/frp/frps/internal/control/runtime"
	controlsession "github.com/zightch/frp/frps/internal/control/session"
	"github.com/zightch/frp/frps/pkg/protocol"
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
	s.notifyDesiredRuntimeRefresh(groupID, group)
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
	currentGroup, currentSnapshot := active.session.CurrentGroupAndSnapshot()
	if currentGroup.EffectiveIP == group.EffectiveIP && controlruntime.SamePushedConfigSnapshot(currentSnapshot, group.Snapshot) {
		active.session.ReplaceGroupRuntime(group)
	}
	if runtime := s.runtimeExecutor(active.session.ID); runtime != nil {
		runtime.setDesiredGroup(group)
	}
	next := active.session.applyControlEvent(controlsession.DesiredRuntimeUpdated{Snapshot: controlruntime.DesiredRuntimeFromGroup(group)})
	if next.Pending != nil {
		active.session.ControlMu.Lock()
		active.session.Pending = group
		active.session.Recovery = controlruntime.PendingRecoveryModeForSnapshot(group.Snapshot)
		active.session.ControlMu.Unlock()
	}
}

func (s *Server) notifyDesiredRuntimeRefresh(groupID int64, group GroupRuntime) {
	s.supervisor.UpdateDesiredRuntime(groupID, controlruntime.DesiredRuntimeFromGroup(group))
}

func (s *Server) freezeGroupRuntime(conn net.Conn, session *sessionState) error {
	listeners, udpListeners, streams, udpSessions := session.freezeTunnelRuntime()
	controlruntime.CloseStartedTunnelListeners(listeners, udpListeners)

	var freezeErr error
	for streamID, stream := range streams {
		if err := s.sendStreamClose(conn, session, streamID, protocol.CloseReasonAdminTerminated, "reload in progress"); err != nil && freezeErr == nil {
			freezeErr = err
		}
		stream.SignalReady(net.ErrClosed)
		stream.Close()
	}
	for _, udpSession := range udpSessions {
		if err := s.sendUDPClose(conn, session, udpSession.SessionID, protocol.CloseReasonAdminTerminated, "reload in progress"); err != nil && freezeErr == nil {
			freezeErr = err
		}
	}
	return freezeErr
}

func (s *Server) rebindGroupRuntime(conn net.Conn, session *sessionState, group GroupRuntime) error {
	if err := s.freezeGroupRuntime(conn, session); err != nil {
		return err
	}
	session.ReplaceGroupRuntime(group)
	session.allowTunnelRuntimeStart()
	logger := s.logger.With(
		"session_id", session.ID,
		"group_id", group.ID,
		"group_name", group.Name,
	)
	return s.ensureTunnelListeners(conn, logger, session)
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
