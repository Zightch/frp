package runtime

import (
	controlsession "github.com/zightch/frp/frps/internal/control/session"
)

// ReserveGroupSlot reserves a slot for a group in the supervisor.
// Returns true if the slot was reserved, false if already reserved.
func ReserveGroupSlot(op SupervisorOperator, groupID int64, sessionID uint64) bool {
	return op.ReserveGroupSlot(groupID, sessionID)
}

// ReleaseGroupSlot releases a group slot from the supervisor.
func ReleaseGroupSlot(op SupervisorOperator, groupID int64, sessionID uint64) {
	op.ReleaseGroupSlot(groupID, sessionID)
}

// SupervisorActiveSession returns the active session for a group from the supervisor.
func SupervisorActiveSession(op SupervisorOperator, groupID int64) (*ActiveSession, bool) {
	return op.ActiveSession(groupID)
}

// SupervisorActiveRuntimeGroups returns all active runtime groups, optionally excluding a specific session.
func SupervisorActiveRuntimeGroups(op SupervisorOperator, exclude any) []RuntimeGroupSnapshot {
	return op.ActiveRuntimeGroups(exclude)
}

// DetachRuntime detaches a runtime session from the supervisor.
func DetachRuntime(op SupervisorOperator, sessionID uint64) {
	op.DetachRuntime(sessionID)
}

// DispatchBySessionID dispatches an event to a session by ID.
func DispatchBySessionID(op SupervisorOperator, sessionID uint64, event controlsession.Event) bool {
	return op.DispatchBySessionID(sessionID, event)
}
