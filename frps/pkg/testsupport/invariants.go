package testsupport

import (
	"fmt"
	"strings"
)

func CheckStartupGate(input InvariantCheckInput) []InvariantViolation {
	if input.After == nil {
		return nil
	}

	after := input.After
	var violations []InvariantViolation
	if !after.App.InitialRuntimeScanDone {
		if after.App.LoginGateOpen {
			violations = append(violations, InvariantViolation{
				Rule:     "startup.login_gate_before_initial_scan",
				Summary:  "首轮扫描未完成前登录门闩不应开放",
				Expected: "LoginGateOpen=false",
				Actual:   "LoginGateOpen=true",
				Fields:   []string{"app.initial_runtime_scan_done", "app.login_gate_open"},
			})
		}
		if after.App.ControlListenerOpen {
			violations = append(violations, InvariantViolation{
				Rule:     "startup.control_listener_before_initial_scan",
				Summary:  "首轮扫描未完成前控制端口不应开放",
				Expected: "ControlListenerOpen=false",
				Actual:   "ControlListenerOpen=true",
				Fields:   []string{"app.initial_runtime_scan_done", "app.control_listener_open"},
			})
		}
		if after.App.ManagementAPIVisible {
			violations = append(violations, InvariantViolation{
				Rule:     "startup.management_api_before_initial_scan",
				Summary:  "首轮扫描未完成前管理 API 不应首次可见",
				Expected: "ManagementAPIVisible=false",
				Actual:   "ManagementAPIVisible=true",
				Fields:   []string{"app.initial_runtime_scan_done", "app.management_api_visible"},
			})
		}
	}
	return violations
}

func CheckSinglePendingConfig(input InvariantCheckInput) []InvariantViolation {
	if input.After == nil {
		return nil
	}

	groupPending := make(map[int64]int)
	var violations []InvariantViolation
	for _, session := range input.After.Server.Sessions {
		if session.Pending == nil {
			continue
		}
		groupPending[session.GroupID]++
	}
	for groupID, count := range groupPending {
		if count <= 1 {
			continue
		}
		violations = append(violations, InvariantViolation{
			Rule:     "session.single_pending_config",
			Summary:  "同一分组同时存在多个有效 pending config",
			Expected: "每个 group 最多 1 个 pending config",
			Actual:   fmt.Sprintf("group_id=%d pending=%d", groupID, count),
			Fields:   []string{"server.sessions.pending"},
		})
	}
	return violations
}

func CheckStaticConflictPriority(input InvariantCheckInput) []InvariantViolation {
	if input.After == nil {
		return nil
	}

	var violations []InvariantViolation
	for _, tunnel := range input.After.Server.Tunnels {
		if !tunnel.StaticConflict || strings.TrimSpace(tunnel.RuntimeIssue) == "" {
			continue
		}
		if tunnel.FinalStatus == "冲突" {
			continue
		}
		violations = append(violations, InvariantViolation{
			Rule:     "tunnel.static_conflict_priority",
			Summary:  "静态冲突优先级被 runtime 异常覆盖",
			Expected: "final_status=冲突",
			Actual:   fmt.Sprintf("tunnel_id=%d final_status=%s", tunnel.TunnelID, tunnel.FinalStatus),
			Fields:   []string{"server.tunnels.static_conflict", "server.tunnels.runtime_issue", "server.tunnels.final_status"},
		})
	}
	return violations
}

func CheckEmptyConfigRecoveryOrder(input InvariantCheckInput) []InvariantViolation {
	if input.Before == nil || input.After == nil {
		return nil
	}

	beforeByGroup := make(map[int64]SessionObservedState, len(input.Before.Server.Sessions))
	for _, session := range input.Before.Server.Sessions {
		beforeByGroup[session.GroupID] = session
	}

	var violations []InvariantViolation
	for _, afterSession := range input.After.Server.Sessions {
		beforeSession, ok := beforeByGroup[afterSession.GroupID]
		if !ok {
			continue
		}
		if beforeSession.RecoveryMode != RecoveryModeEmptyConfig {
			continue
		}
		if afterSession.RecoveryMode == RecoveryModeRunning && afterSession.SnapshotTunnelCount == 0 {
			violations = append(violations, InvariantViolation{
				Rule:     "recovery.full_snapshot_before_listener_resume",
				Summary:  "空配置保活恢复时未先切回完整快照就恢复到了 running",
				Expected: "恢复到 running 前 snapshot_tunnel_count 必须大于 0",
				Actual:   fmt.Sprintf("group_id=%d snapshot_tunnel_count=%d", afterSession.GroupID, afterSession.SnapshotTunnelCount),
				Fields:   []string{"server.sessions.recovery_mode", "server.sessions.snapshot_tunnel_count"},
			})
		}
	}
	return violations
}

func CheckOldSessionIsolation(input InvariantCheckInput) []InvariantViolation {
	if input.Before == nil || input.After == nil || input.After.Transport == nil {
		return nil
	}

	beforeByGroup := make(map[int64]SessionObservedState, len(input.Before.Server.Sessions))
	for _, session := range input.Before.Server.Sessions {
		beforeByGroup[session.GroupID] = session
	}

	currentByGroup := make(map[int64]SessionObservedState, len(input.After.Server.Sessions))
	for _, session := range input.After.Server.Sessions {
		currentByGroup[session.GroupID] = session
	}

	var violations []InvariantViolation
	for groupID, beforeSession := range beforeByGroup {
		afterSession, ok := currentByGroup[groupID]
		if !ok || beforeSession.SessionID == 0 || afterSession.SessionID == 0 || beforeSession.SessionID == afterSession.SessionID {
			continue
		}
		for _, frame := range input.After.Transport.Delivered {
			if frame.SessionID != beforeSession.SessionID {
				continue
			}
			if frame.ConnID == afterSession.ConnID {
				violations = append(violations, InvariantViolation{
					Rule:     "session.old_session_isolation",
					Summary:  "旧 session 的晚到帧落到了新连接身份上",
					Expected: "old session frame must not target new active conn",
					Actual:   fmt.Sprintf("group_id=%d old_session=%d new_conn=%s frame=%s", groupID, beforeSession.SessionID, afterSession.ConnID, frame.FrameType),
					Fields:   []string{"transport.delivered", "server.sessions.conn_id", "server.sessions.session_id"},
				})
				break
			}
		}
	}
	return violations
}

func CheckAll(input InvariantCheckInput) []InvariantViolation {
	var violations []InvariantViolation
	violations = append(violations, CheckStartupGate(input)...)
	violations = append(violations, CheckSinglePendingConfig(input)...)
	violations = append(violations, CheckStaticConflictPriority(input)...)
	violations = append(violations, CheckEmptyConfigRecoveryOrder(input)...)
	violations = append(violations, CheckOldSessionIsolation(input)...)
	return violations
}
