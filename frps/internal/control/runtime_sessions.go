package control

import (
	"context"
	"log/slog"
	"net"

	controlsession "github.com/zightch/frp/frps/internal/control/session"
	"github.com/zightch/frp/frps/pkg/transport"
)

type runtimeGroupSnapshot struct {
	group    GroupRuntime
	snapshot ConfigSnapshot
}

func (s runtimeSessionSnapshot) activeRuntimeGroup() (runtimeGroupSnapshot, bool) {
	activeTunnelIDs := s.activeRuntimeTunnelIDs()
	if len(activeTunnelIDs) == 0 {
		return runtimeGroupSnapshot{}, false
	}

	group := s.desiredGroup
	config := buildRuntimeObservedConfig(s.state, group)
	snapshot := config.snapshot
	snapshot.Tunnels = filterTunnelsByID(snapshot.Tunnels, activeTunnelIDs)
	if len(snapshot.Tunnels) == 0 {
		return runtimeGroupSnapshot{}, false
	}

	group.EffectiveIP = config.effectiveIP
	group.Snapshot = snapshot
	return runtimeGroupSnapshot{
		group:    group,
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
	if s == nil || s.supervisor == nil {
		return false
	}
	return s.supervisor.ReserveGroupSlot(groupID, sessionID)
}

func (s *Server) releaseGroupSlot(groupID int64, sessionID uint64) {
	if s == nil || s.supervisor == nil {
		return
	}
	s.supervisor.ReleaseGroupSlot(groupID, sessionID)
}

func (s *Server) activeSession(groupID int64) (*activeSession, bool) {
	if s == nil || s.supervisor == nil {
		return nil, false
	}
	return s.supervisor.ActiveSession(groupID)
}

func (s *Server) activeRuntimeGroups(exclude *sessionState) []runtimeGroupSnapshot {
	if s == nil || s.supervisor == nil {
		return nil
	}

	snapshot := s.supervisor.Snapshot(exclude)
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

func (s *Server) registerActiveSession(conn net.Conn, session *sessionState) {
	if s == nil || s.supervisor == nil || conn == nil || session == nil {
		return
	}
	_ = attachProjectedRuntimeSession(context.Background(), s.supervisor, s.logger, conn, session)
}

func (s *Server) unregisterActiveSession(session *sessionState) {
	if s == nil || s.supervisor == nil || session == nil {
		return
	}
	s.supervisor.DetachRuntime(session.ID)
	_ = s.supervisor.DispatchBySessionID(session.ID, controlsession.ControlConnClosed{Reason: "runtime unregistered"})
}

func projectedSessionState(session *sessionState, conn net.Conn) controlsession.SessionState {
	if session == nil {
		return controlsession.SessionState{}
	}

	configState, runtimeState := session.observeState()
	state := controlsession.NewState(configState.group.ID, session.ID)
	state.Conn = controlsession.ControlConnState{
		Attached: conn != nil,
		ConnID:   transport.ConnectionID(conn),
	}
	state.Phase = controlsession.SessionPhaseOnline

	applied := desiredRuntimeFromObservedSnapshot(configState.group.EffectiveIP, configState.snapshot)
	if configState.lastAckedConfigValue != 0 || configState.snapshot.Version != 0 || len(configState.snapshot.Tunnels) != 0 {
		state.Applied = &controlsession.AppliedRuntimeSnapshot{Snapshot: applied}
	}

	desired := applied
	if configState.pendingRequestID != 0 {
		pending := desiredRuntimeFromObservedSnapshot(configState.pendingGroup.EffectiveIP, configState.pendingSnapshot)
		state.Pending = &controlsession.PendingConfigPush{
			RequestID: configState.pendingRequestID,
			Snapshot:  pending,
		}
		desired = pending
		state.Phase = controlsession.SessionPhaseSyncingConfig
	}
	state.Desired = &desired
	state.Bindings = projectSessionBindings(desired, runtimeState.activeTunnelIDs)
	state.RuntimePhase = projectedRuntimePhase(desired, state.Bindings, runtimeState)

	return state
}

func projectSessionBindings(snapshot controlsession.DesiredRuntimeSnapshot, activeTunnelIDs map[uint32]struct{}) map[controlsession.BindingKey]controlsession.BindingState {
	bindings := make(map[controlsession.BindingKey]controlsession.BindingState)
	for _, tunnel := range snapshot.Tunnels {
		if !tunnel.Enabled {
			continue
		}

		phase := controlsession.BindingPhaseClosed
		if _, ok := activeTunnelIDs[tunnel.TunnelID]; ok {
			phase = controlsession.BindingPhaseActive
		}

		for port := tunnel.RemoteStart; port <= tunnel.RemoteEnd; port++ {
			key := controlsession.BindingKey{
				Protocol:    tunnel.Protocol,
				EffectiveIP: snapshot.EffectiveIP,
				Port:        port,
			}
			bindings[key] = controlsession.BindingState{
				Key:   key,
				Phase: phase,
			}
			if port == tunnel.RemoteEnd {
				break
			}
		}
	}
	return bindings
}

func projectedRuntimePhase(snapshot controlsession.DesiredRuntimeSnapshot, bindings map[controlsession.BindingKey]controlsession.BindingState, runtime observedSessionRuntimeState) controlsession.RuntimePhase {
	if !desiredSnapshotHasEnabledTunnels(snapshot) {
		return controlsession.RuntimePhaseEmpty
	}
	if runtime.frozen {
		return controlsession.RuntimePhaseBlocked
	}
	if len(bindings) == 0 {
		return controlsession.RuntimePhaseBinding
	}
	for _, binding := range bindings {
		if binding.Phase != controlsession.BindingPhaseActive {
			if runtime.listenersStarted {
				return controlsession.RuntimePhaseRecovering
			}
			return controlsession.RuntimePhaseBinding
		}
	}
	return controlsession.RuntimePhaseActive
}

func desiredSnapshotHasEnabledTunnels(snapshot controlsession.DesiredRuntimeSnapshot) bool {
	for _, tunnel := range snapshot.Tunnels {
		if tunnel.Enabled {
			return true
		}
	}
	return false
}

func desiredRuntimeFromObservedSnapshot(effectiveIP string, snapshot ConfigSnapshot) controlsession.DesiredRuntimeSnapshot {
	return controlsession.DesiredRuntimeSnapshot{
		Version:       snapshot.Version,
		GeneratedAtMs: snapshot.GeneratedAtMs,
		EffectiveIP:   effectiveIP,
		Tunnels:       desiredTunnelsFromConfig(snapshot.Tunnels),
	}
}

func attachProjectedRuntimeSession(parent context.Context, supervisor *Supervisor, baseLogger Logger, conn net.Conn, session *sessionState) *controlsession.Agent {
	if supervisor == nil || conn == nil || session == nil {
		return nil
	}

	group, snapshot := session.currentGroupAndSnapshot()
	group.Snapshot = snapshot
	runtime := &runtimeExecutor{
		groupID:      group.ID,
		conn:         conn,
		logger:       scopedRuntimeLogger(baseLogger, session, group),
		session:      session,
		desiredGroup: group,
	}
	return supervisor.AttachSession(parent, projectedSessionState(session, conn), runtime)
}

func scopedRuntimeLogger(base Logger, session *sessionState, group GroupRuntime) *slog.Logger {
	if base == nil || session == nil {
		return nil
	}
	if logger, ok := base.(*slog.Logger); ok {
		return logger.With("session_id", session.ID, "group_id", group.ID, "group_name", group.Name)
	}
	return nil
}
