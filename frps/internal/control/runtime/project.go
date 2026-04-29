package runtime

import (
	"log/slog"
)

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
}

// SupervisorOperator defines the interface for supervisor operations.
type SupervisorOperator interface {
	// Group slot management
	ReserveGroupSlot(groupID int64, sessionID uint64) bool
	ReleaseGroupSlot(groupID int64, sessionID uint64)

	// Session access
	ActiveSession(groupID int64) (*ActiveSession, bool)
	ActiveRuntimeGroups(exclude SessionStateProjectionTarget) []RuntimeGroupSnapshot

	// Session lifecycle
	DetachRuntime(sessionID uint64)
	DispatchBySessionID(sessionID uint64, event Event) bool
}
