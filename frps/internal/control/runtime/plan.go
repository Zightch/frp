package runtime

import (
	"fmt"
	"strings"

	"github.com/zightch/frp/frps/pkg/protocol"
	"github.com/zightch/frp/frps/pkg/testsupport"
)

// CollectRuntimeIssueClearTunnelIDs collects tunnel IDs that should have their runtime issues cleared.
// This includes disabled tunnels and tunnels that are already active.
func CollectRuntimeIssueClearTunnelIDs(tunnels []protocol.TunnelEntry, activeTunnelIDs map[uint32]struct{}) []uint32 {
	clearIDs := make([]uint32, 0, len(tunnels))
	for _, tunnel := range tunnels {
		if tunnel.TunnelFlags&protocol.TunnelFlagEnabled == 0 {
			clearIDs = append(clearIDs, tunnel.TunnelID)
			continue
		}
		if _, active := activeTunnelIDs[tunnel.TunnelID]; active {
			clearIDs = append(clearIDs, tunnel.TunnelID)
		}
	}
	return clearIDs
}

// PlanSessionRuntimeStart creates a plan for starting a session runtime.
func PlanSessionRuntimeStart(op RuntimeOperator, target SessionRuntimeStartTarget) SessionRuntimeStartPlan {
	plan := SessionRuntimeStartPlan{
		Group:    target.Group(),
		Snapshot: target.Snapshot(),
	}
	if op.IsShuttingDown() || target.SessionIsDone() || !target.SessionCanStartTunnelRuntime() {
		plan.Blocked = true
		return plan
	}
	if len(target.Snapshot().Tunnels) == 0 {
		return plan
	}

	activeTunnelIDs := target.SessionActiveRuntimeTunnelIDs()
	plan.ActiveRuntime = len(activeTunnelIDs) != 0
	plan.TargetTunnels = SelectNonListeningEnabledTunnels(target.Snapshot().Tunnels, activeTunnelIDs)
	plan.ClearIssueTunnelIDs = CollectRuntimeIssueClearTunnelIDs(target.Snapshot().Tunnels, activeTunnelIDs)
	if len(plan.TargetTunnels) == 0 {
		return plan
	}

	bindIP, err := op.ResolveGroupEffectiveIP(target.Group())
	if err != nil {
		plan.BindErr = err
		return plan
	}
	plan.BindIP = bindIP
	plan.ConflictIssues = DetectRuntimePortConflictIssues(op, target.Group(), bindIP, plan.TargetTunnels)
	return plan
}

// ApplySessionRuntimeStartPlan applies the session runtime start plan.
func ApplySessionRuntimeStartPlan(op RuntimeOperator, target SessionRuntimeStartTarget, plan SessionRuntimeStartPlan) error {
	if plan.Blocked {
		return nil
	}
	if len(plan.Snapshot.Tunnels) == 0 {
		target.SessionResetRuntimeGenerationIfIdle()
		target.SessionSetRecoveryMode(testsupport.RecoveryModeEmptyConfig)
		return nil
	}
	for _, tunnelID := range plan.ClearIssueTunnelIDs {
		op.RecordTunnelRuntimeIssueForConfig(tunnelID, plan.Snapshot.Version, "")
	}
	if plan.BindErr != nil {
		reason := BuildGroupEffectiveIPRuntimeReason(plan.Group, plan.BindErr)
		for _, tunnel := range EnabledTunnels(plan.Snapshot) {
			op.RecordTunnelRuntimeIssueForConfig(tunnel.TunnelID, plan.Snapshot.Version, reason)
		}
		return fmt.Errorf("%s: %w", reason, plan.BindErr)
	}
	for tunnelID, reason := range plan.ConflictIssues {
		op.RecordTunnelRuntimeIssueForConfig(tunnelID, plan.Snapshot.Version, reason)
	}
	if len(plan.TargetTunnels) == 0 {
		if plan.ActiveRuntime {
			target.SessionSetRecoveryMode(testsupport.RecoveryModeRunning)
		}
		return nil
	}

	for _, tunnel := range plan.TargetTunnels {
		if reason := strings.TrimSpace(plan.ConflictIssues[tunnel.TunnelID]); reason != "" {
			target.Logger().Warn(
				"skip tunnel listener start because runtime conflict was detected",
				"tunnel_id", tunnel.TunnelID,
				"group_id", plan.Group.ID,
				"reason", reason,
			)
			continue
		}
		opCtx := TunnelListenerOperationContext{
			GroupID:       plan.Group.ID,
			ConfigVersion: plan.Snapshot.Version,
			Tunnel:        tunnel,
			BindIP:        plan.BindIP,
			Kind:          BindKindRuntimeStart,
		}
		started, startErr := op.StartTunnelListeners(opCtx)
		if startErr != nil {
			op.RecordTunnelRuntimeIssueForConfig(tunnel.TunnelID, plan.Snapshot.Version, startErr.Error())
			target.Logger().Warn(
				"tunnel listener start failed",
				"tunnel_id", tunnel.TunnelID,
				"group_id", plan.Group.ID,
				"error", startErr,
			)
			continue
		}
		op.RecordTunnelRuntimeIssueForConfig(tunnel.TunnelID, plan.Snapshot.Version, "")
		startUDPCleanup, attached := target.SessionAttachTunnelListeners(plan.Snapshot.Version, tunnel.TunnelID, started.TCPListeners, started.UDPListeners)
		if !attached {
			CloseStartedTunnelListeners(started.TCPListeners, started.UDPListeners)
			return nil
		}
		if op.IsShuttingDown() || target.SessionIsDone() {
			op.ShutdownSession(target)
			return nil
		}
		if startUDPCleanup {
			go op.ServeUDPIdleCleanup(target.Conn(), target.Logger(), target)
		}
		for _, runtime := range started.TCPRuntimes {
			serve := op.NewTunnelRuntimeServeContext(opCtx, target, runtime.RemotePort)
			target.Logger().Info(
				"tcp tunnel listener ready",
				"tunnel_id", runtime.Tunnel.TunnelID,
				"remote_port", runtime.RemotePort,
				"addr", runtime.Listener.Addr().String(),
			)
			go op.ServeTunnelListener(serve, runtime.Listener)
		}
		for _, runtime := range started.UDPRuntimes {
			serve := op.NewTunnelRuntimeServeContext(opCtx, target, runtime.RemotePort)
			target.Logger().Info(
				"udp tunnel listener ready",
				"tunnel_id", runtime.Tunnel.TunnelID,
				"remote_port", runtime.RemotePort,
				"addr", runtime.Listener.LocalAddr().String(),
			)
			go op.ServeUDPTunnelListener(serve, runtime.Listener)
		}
	}
	for tunnelID := range target.SessionActiveRuntimeTunnelIDs() {
		op.RecordTunnelRuntimeIssueForConfig(tunnelID, plan.Snapshot.Version, "")
	}
	if target.SessionHasActiveRuntimeListeners() {
		target.SessionSetRecoveryMode(testsupport.RecoveryModeRunning)
	}
	return nil
}
