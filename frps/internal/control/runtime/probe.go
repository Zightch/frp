package runtime

import (
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
	op RuntimeOperator,
	groupID int64,
	bindIP string,
	tunnel protocol.TunnelEntry,
) string {
	if op == nil {
		return ""
	}

	opCtx := NewTunnelRuntimeProbeContext(groupID, tunnel, bindIP)
	switch tunnel.Protocol {
	case protocol.ProtocolTCP:
		listeners, err := op.StartTunnelListeners(opCtx)
		if err != nil {
			return BuildTunnelListenerStartReason(opCtx.Tunnel.Protocol, opCtx.BindIP, 0, err)
		}
		CloseStartedTunnelListeners(listeners.TCPListeners, nil)
	case protocol.ProtocolUDP:
		listeners, err := op.StartTunnelListeners(opCtx)
		if err != nil {
			return BuildTunnelListenerStartReason(opCtx.Tunnel.Protocol, opCtx.BindIP, 0, err)
		}
		CloseStartedTunnelListeners(nil, listeners.UDPListeners)
	}
	return ""
}

// ProbeTunnelRuntimeIssueWithData probes for runtime issues using explicit data.
// This is useful for testing or when the RuntimeOperator interface is not available.
func ProbeTunnelRuntimeIssueWithData(
	groupID int64,
	bindIP string,
	tunnel protocol.TunnelEntry,
	startListeners func(opCtx TunnelListenerOperationContext) (TunnelListenerBatch, error),
) string {
	if startListeners == nil {
		return ""
	}

	opCtx := NewTunnelRuntimeProbeContext(groupID, tunnel, bindIP)
	switch tunnel.Protocol {
	case protocol.ProtocolTCP:
		listeners, err := startListeners(opCtx)
		if err != nil {
			return BuildTunnelListenerStartReason(opCtx.Tunnel.Protocol, opCtx.BindIP, 0, err)
		}
		CloseStartedTunnelListeners(listeners.TCPListeners, nil)
	case protocol.ProtocolUDP:
		listeners, err := startListeners(opCtx)
		if err != nil {
			return BuildTunnelListenerStartReason(opCtx.Tunnel.Protocol, opCtx.BindIP, 0, err)
		}
		CloseStartedTunnelListeners(nil, listeners.UDPListeners)
	}
	return ""
}

// RequestAuditedSessionRuntimeRecovery requests recovery for a session's tunnels.
// It dispatches events to close bindings that are missing listeners, or requests a reconcile event.
func RequestAuditedSessionRuntimeRecovery(op RuntimeOperator, sessionID uint64, targetTunnels []protocol.TunnelEntry) error {
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
		for key := range state.Bindings {
			tunnelID := TunnelIDForBinding(state, key)
			if _, ok := targetTunnelIDs[tunnelID]; !ok {
				continue
			}
			event := BindingClosed{
				Key:    key,
				Reason: "runtime audit detected missing listener",
			}
			op.ApplySessionEvent(sessionID, event)
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
	op.ApplySessionEvent(sessionID, reconcileEvent)
	op.DispatchBySessionID(sessionID, reconcileEvent)
	AwaitAuditedSessionRecovery(op, sessionID, targetTunnelIDs)
	return nil
}
