package runtime

import (
	controllistener "github.com/zightch/frp/frps/internal/control/runtime/listener"
	controlsession "github.com/zightch/frp/frps/internal/control/session"
	"github.com/zightch/frp/frps/pkg/protocol"
)

// Re-export session types for use in recovery functions
const RuntimePhaseActive = controlsession.RuntimePhaseActive

type BindingClosed = controlsession.BindingClosed
type ReconcileRequested = controlsession.ReconcileRequested

// ProbeTunnelRuntimeIssue probes for runtime issues with a specific tunnel.
// It attempts to bind the tunnel's ports to check if they can be listened on.
// Returns an empty string if no issues are found, or a reason string if issues exist.
func ProbeTunnelRuntimeIssue(
	op ListenerStarter,
	groupID int64,
	bindIP string,
	tunnel protocol.TunnelEntry,
) string {
	if op == nil {
		return ""
	}
	return controllistener.ProbeTunnelRuntimeIssue(op, groupID, bindIP, tunnel)
}

// ProbeTunnelRuntimeIssueWithData probes for runtime issues using explicit data.
// This is useful for testing or when the listener starter seam is not available.
func ProbeTunnelRuntimeIssueWithData(
	groupID int64,
	bindIP string,
	tunnel protocol.TunnelEntry,
	startListeners func(opCtx TunnelListenerOperationContext) (TunnelListenerBatch, error),
) string {
	if startListeners == nil {
		return ""
	}
	return controllistener.ProbeTunnelRuntimeIssueWithData(groupID, bindIP, tunnel, startListeners)
}

// RequestAuditedSessionRuntimeRecovery requests recovery for a session's tunnels.
// It dispatches events to close bindings that are missing listeners, or requests a reconcile event.
func RequestAuditedSessionRuntimeRecovery(op RuntimeAuditedSessionRecoveryDeps, sessionID uint64, targetTunnels []protocol.TunnelEntry) error {
	if op == nil || sessionID == 0 || len(targetTunnels) == 0 {
		return nil
	}

	state, ok := op.SessionState(sessionID)
	if !ok {
		return nil
	}

	targetTunnelIDs := make(map[uint32]struct{}, len(targetTunnels))
	for _, tunnel := range targetTunnels {
		targetTunnelIDs[tunnel.TunnelID] = struct{}{}
	}

	dispatched := false
	if state.RuntimePhase == RuntimePhaseActive {
		closedTunnelIDs := make(map[uint32]struct{})
		for key := range state.Bindings {
			tunnelID := TunnelIDForBinding(state, key)
			if _, ok := targetTunnelIDs[tunnelID]; !ok {
				continue
			}
			if _, seen := closedTunnelIDs[tunnelID]; seen {
				continue
			}
			closedTunnelIDs[tunnelID] = struct{}{}
			event := BindingClosed{
				Key:    key,
				Reason: "runtime audit detected missing listener",
			}
			dispatched = op.DispatchBySessionID(sessionID, event) || dispatched
		}
	}
	if dispatched {
		AwaitAuditedSessionRecovery(op, sessionID, targetTunnelIDs)
		return nil
	}
	reconcileEvent := ReconcileRequested{
		Reason: "runtime_audit_recover",
	}
	if op.DispatchBySessionID(sessionID, reconcileEvent) {
		AwaitAuditedSessionRecovery(op, sessionID, targetTunnelIDs)
	}
	return nil
}
