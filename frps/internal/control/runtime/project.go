package runtime

import (
	"context"
	controlsession "github.com/zightch/frp/frps/internal/control/session"
	"log/slog"
	"net"
)

// ProjectedSessionState projects the session state with runtime information.
func ProjectedSessionState(session SessionStateProjectionTarget, conn net.Conn) controlsession.SessionState {
	if session == nil {
		return controlsession.SessionState{}
	}
	return session.ProjectedSessionState(conn)
}

// AttachProjectedRuntimeSession attaches a projected runtime session to the supervisor.
func AttachProjectedRuntimeSession(parent context.Context, supervisor SupervisorOperator, baseLogger Logger, conn net.Conn, session SessionStateProjectionTarget) *controlsession.Agent {
	if supervisor == nil || conn == nil || session == nil {
		return nil
	}

	group, snapshot := session.CurrentGroupAndSnapshot()
	group.Snapshot = snapshot
	runtime := &RuntimeExecutor{
		GroupID: group.ID,
		Conn:    conn,
		Logger:  ScopedRuntimeLogger(baseLogger, session, group),
		Session: session,
	}
	return supervisor.AttachRuntimeSession(parent, runtime)
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
	ProjectedSessionState(conn net.Conn) controlsession.SessionState
}

// SupervisorOperator defines the interface for supervisor operations.
type SupervisorOperator interface {
	// Session attachment
	AttachRuntimeSession(parent context.Context, runtime *RuntimeExecutor) *controlsession.Agent

	// Group slot management
	ReserveGroupSlot(groupID int64, sessionID uint64) bool
	ReleaseGroupSlot(groupID int64, sessionID uint64)

	// Session access
	ActiveSession(groupID int64) (*ActiveSession, bool)
	ActiveRuntimeGroups(exclude SessionStateProjectionTarget) []RuntimeGroupSnapshot
	SessionState(sessionID uint64) (controlsession.SessionState, bool)

	// Session lifecycle
	DetachRuntime(sessionID uint64)
	DispatchBySessionID(sessionID uint64, event controlsession.Event) bool
}
