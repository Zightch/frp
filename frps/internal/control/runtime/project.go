package runtime

import (
	"context"
	"log/slog"
	"net"

	controlsession "github.com/zightch/frp/frps/internal/control/session"
	"github.com/zightch/frp/frps/pkg/transport"
)

// ProjectedSessionState projects the session state with runtime information.
func ProjectedSessionState(session SessionStateProjectionTarget, conn net.Conn) controlsession.SessionState {
	if session == nil {
		return controlsession.SessionState{}
	}

	configState, runtimeState := session.ObserveState()
	state := controlsession.Clone(configState.State)
	if state.GroupID == 0 {
		state = controlsession.NewState(configState.Group.ID, session.SessionID())
	}
	state.GroupID = configState.Group.ID
	state.SessionID = session.SessionID()
	state.Conn = controlsession.ControlConnState{
		Attached: conn != nil,
		ConnID:   transport.ConnectionID(conn),
	}
	if state.Phase == controlsession.SessionPhaseUnknown {
		state.Phase = controlsession.SessionPhaseOnline
	}

	if state.Desired == nil {
		desired := DesiredRuntimeFromObservedSnapshot(configState.Group.EffectiveIP, configState.Group.Snapshot)
		state.Desired = &desired
	}
	desired := *state.Desired
	state.Desired = &desired
	state.Bindings = ProjectSessionBindings(desired, runtimeState.ActiveTunnelIDs)
	state.RuntimePhase = ProjectedRuntimePhase(desired, state.Bindings, runtimeState)

	return state
}

// ProjectSessionBindings projects the session bindings from the desired snapshot and active tunnel IDs.
func ProjectSessionBindings(snapshot controlsession.DesiredRuntimeSnapshot, activeTunnelIDs map[uint32]struct{}) map[controlsession.BindingKey]controlsession.BindingState {
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

// ProjectedRuntimePhase projects the runtime phase from the desired snapshot, bindings, and runtime state.
func ProjectedRuntimePhase(snapshot controlsession.DesiredRuntimeSnapshot, bindings map[controlsession.BindingKey]controlsession.BindingState, runtime ObservedState) controlsession.RuntimePhase {
	if !DesiredSnapshotHasEnabledTunnels(snapshot) {
		return controlsession.RuntimePhaseEmpty
	}
	if runtime.Frozen {
		return controlsession.RuntimePhaseBlocked
	}
	if len(bindings) == 0 {
		return controlsession.RuntimePhaseBinding
	}
	for _, binding := range bindings {
		if binding.Phase != controlsession.BindingPhaseActive {
			if runtime.ListenersStarted {
				return controlsession.RuntimePhaseRecovering
			}
			return controlsession.RuntimePhaseBinding
		}
	}
	return controlsession.RuntimePhaseActive
}

// AttachProjectedRuntimeSession attaches a projected runtime session to the supervisor.
func AttachProjectedRuntimeSession(parent context.Context, supervisor SupervisorOperator, baseLogger Logger, conn net.Conn, session SessionStateProjectionTarget) *controlsession.Agent {
	if supervisor == nil || conn == nil || session == nil {
		return nil
	}

	group, snapshot := session.CurrentGroupAndSnapshot()
	group.Snapshot = snapshot
	runtime := &RuntimeExecutor{
		GroupID:      group.ID,
		Conn:         conn,
		Logger:       ScopedRuntimeLogger(baseLogger, session, group),
		Session:      session,
		DesiredGroup: group,
	}
	return supervisor.AttachRuntimeSession(parent, ProjectedSessionState(session, conn), runtime)
}

// ScopedRuntimeLogger creates a scoped logger for the runtime session.
func ScopedRuntimeLogger(base Logger, session SessionStateProjectionTarget, group GroupRuntime) *slog.Logger {
	if base == nil || session == nil {
		return nil
	}
	if logger, ok := base.(*slog.Logger); ok {
		return logger.With("session_id", session.SessionID(), "group_id", group.ID, "group_name", group.Name)
	}
	return nil
}

// SessionStateProjectionTarget defines the interface for session state projection.
// This interface is implemented by the control package's sessionState.
type SessionStateProjectionTarget interface {
	SessionID() uint64
	ObserveState() (ObservedConfigState, ObservedState)
	CurrentGroupAndSnapshot() (GroupRuntime, ConfigSnapshot)
}

// SupervisorOperator defines the interface for supervisor operations.
type SupervisorOperator interface {
	// Session attachment
	AttachRuntimeSession(parent context.Context, state controlsession.SessionState, runtime *RuntimeExecutor) *controlsession.Agent

	// Group slot management
	ReserveGroupSlot(groupID int64, sessionID uint64) bool
	ReleaseGroupSlot(groupID int64, sessionID uint64)

	// Session access
	ActiveSession(groupID int64) (*ActiveSession, bool)
	ActiveRuntimeGroups(exclude any) []RuntimeGroupSnapshot
	SessionState(sessionID uint64) (controlsession.SessionState, bool)

	// Session lifecycle
	DetachRuntime(sessionID uint64)
	DispatchBySessionID(sessionID uint64, event controlsession.Event) bool
}

// DesiredRuntimeFromObservedSnapshot creates a desired runtime snapshot from the observed effective IP and config snapshot.
func DesiredRuntimeFromObservedSnapshot(effectiveIP string, snapshot ConfigSnapshot) controlsession.DesiredRuntimeSnapshot {
	return controlsession.DesiredRuntimeSnapshot{
		Version:       snapshot.Version,
		GeneratedAtMs: snapshot.GeneratedAtMs,
		EffectiveIP:   effectiveIP,
		Tunnels:       DesiredTunnelsFromConfig(snapshot.Tunnels),
	}
}
