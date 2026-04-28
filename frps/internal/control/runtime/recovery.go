package runtime

import (
	"time"

	controlsession "github.com/zightch/frp/frps/internal/control/session"
)

// TunnelIDForBinding returns the tunnel ID for a binding key based on the session state.
func TunnelIDForBinding(state controlsession.SessionState, key controlsession.BindingKey) uint32 {
	if state.Applied == nil {
		return 0
	}

	for _, tunnel := range state.Applied.Snapshot.Tunnels {
		if !tunnel.Enabled || tunnel.Protocol != key.Protocol {
			continue
		}
		if key.Port >= tunnel.RemoteStart && key.Port <= tunnel.RemoteEnd {
			return tunnel.TunnelID
		}
	}
	return 0
}

// ActiveRuntimeTunnelIDsFromSession extracts active runtime tunnel IDs from a session.
// The session parameter must have ActiveRuntimeTunnelIDs() method.
func ActiveRuntimeTunnelIDsFromSession(session any) map[uint32]struct{} {
	if session == nil {
		return nil
	}
	if ace, ok := session.(interface{ ActiveRuntimeTunnelIDs() map[uint32]struct{} }); ok {
		return ace.ActiveRuntimeTunnelIDs()
	}
	return nil
}

// AwaitAuditedSessionRecovery waits for the session to recover the specified tunnels.
// It polls the session's active tunnel IDs until the deadline is reached.
func AwaitAuditedSessionRecovery(op RuntimeOperator, sessionID uint64, targetTunnelIDs map[uint32]struct{}) {
	if op == nil || len(targetTunnelIDs) == 0 {
		return
	}

	runtime := op.RuntimeExecutor(sessionID)
	if runtime == nil || runtime.Session == nil {
		return
	}

	deadline := time.Now().Add(200 * time.Millisecond)
	for time.Now().Before(deadline) {
		active := ActiveRuntimeTunnelIDsFromSession(runtime.Session)
		recovered := true
		for tunnelID := range targetTunnelIDs {
			if _, ok := active[tunnelID]; !ok {
				recovered = false
				break
			}
		}
		if recovered {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
}
