package control

import (
	"net"

	"github.com/zightch/frp/frps/pkg/protocol"
	"github.com/zightch/frp/frps/pkg/testsupport"
)

type runtimeAdminAction uint8

const (
	runtimeAdminActionNoop runtimeAdminAction = iota
	runtimeAdminActionSyncPendingConfig
	runtimeAdminActionKeepRuntime
	runtimeAdminActionEnsureListeners
	runtimeAdminActionRebindRuntime
	runtimeAdminActionPushEmptyConfig
	runtimeAdminActionPushFullConfig
	runtimeAdminActionCloseSession
)

func (a runtimeAdminAction) String() string {
	switch a {
	case runtimeAdminActionNoop:
		return "noop"
	case runtimeAdminActionSyncPendingConfig:
		return "sync_pending_config"
	case runtimeAdminActionKeepRuntime:
		return "keep_runtime"
	case runtimeAdminActionEnsureListeners:
		return "ensure_listeners"
	case runtimeAdminActionRebindRuntime:
		return "rebind_runtime"
	case runtimeAdminActionPushEmptyConfig:
		return "push_empty_config"
	case runtimeAdminActionPushFullConfig:
		return "push_full_config"
	case runtimeAdminActionCloseSession:
		return "close_session"
	default:
		return "unknown"
	}
}

// runtimeAdminActionPlan keeps recovery and future admin writes focused on a
// single internal action against a stable runtime session target ID.
type runtimeAdminActionPlan struct {
	action   runtimeAdminAction
	targetID runtimeSessionTargetID
	group    GroupRuntime
	snapshot ConfigSnapshot
}

type runtimeAdminRequest struct {
	target            runtimeSessionTarget
	group             GroupRuntime
	targetTunnels     []protocol.TunnelEntry
	staticConflictIDs map[int64]struct{}
	issues            map[uint32]string
}

type runtimeAdminOperationTarget struct {
	id              runtimeSessionTargetID
	conn            net.Conn
	session         *sessionState
	currentGroup    GroupRuntime
	currentSnapshot ConfigSnapshot
	release         func()
}

func (t runtimeAdminOperationTarget) unlock() {
	if t.release != nil {
		t.release()
	}
}

type runtimeAdminCoordinator struct {
	server *Server
}

func (s *Server) runtimeAdminCoordinator() runtimeAdminCoordinator {
	return runtimeAdminCoordinator{server: s}
}

func newRuntimeRefreshRequest(target runtimeSessionTarget, group GroupRuntime) runtimeAdminRequest {
	return runtimeAdminRequest{
		target: target,
		group:  group,
	}
}

func newRuntimeScannedRecoveryRequest(target runtimeSessionTarget, group GroupRuntime, targetTunnels []protocol.TunnelEntry, staticConflictIDs map[int64]struct{}, issues map[uint32]string) runtimeAdminRequest {
	return runtimeAdminRequest{
		target:            target,
		group:             group,
		targetTunnels:     targetTunnels,
		staticConflictIDs: staticConflictIDs,
		issues:            issues,
	}
}

func (c runtimeAdminCoordinator) planRefresh(request runtimeAdminRequest) runtimeAdminActionPlan {
	snapshot := runtimeSnapshotForGroup(request.group)
	desiredSnapshot := snapshot
	shrinkToEmpty := false
	if emptySnapshot, _, shouldShrinkToEmpty := c.server.runtimeRefreshSnapshot(request.group, snapshot); shouldShrinkToEmpty {
		desiredSnapshot = emptySnapshot
		shrinkToEmpty = true
	}

	if request.target.hasPendingConfig() && samePushedConfigSnapshot(request.target.pending.snapshot, desiredSnapshot) {
		return runtimeAdminActionPlan{
			action:   runtimeAdminActionSyncPendingConfig,
			targetID: request.target.id,
			group:    request.group,
			snapshot: desiredSnapshot,
		}
	}
	if request.target.hasPendingConfig() {
		return runtimeAdminActionPlan{
			action:   runtimeAdminActionCloseSession,
			targetID: request.target.id,
		}
	}
	if shrinkToEmpty {
		return runtimeAdminActionPlan{
			action:   runtimeAdminActionPushEmptyConfig,
			targetID: request.target.id,
			group:    request.group,
			snapshot: desiredSnapshot,
		}
	}
	if sameRuntimeSnapshot(request.target.snapshot, snapshot) {
		if request.target.group.EffectiveIP == request.group.EffectiveIP {
			return runtimeAdminActionPlan{
				action:   runtimeAdminActionKeepRuntime,
				targetID: request.target.id,
				group:    request.group,
			}
		}
		return runtimeAdminActionPlan{
			action:   runtimeAdminActionRebindRuntime,
			targetID: request.target.id,
			group:    request.group,
		}
	}
	if len(desiredSnapshot.Tunnels) == 0 {
		return runtimeAdminActionPlan{
			action:   runtimeAdminActionPushEmptyConfig,
			targetID: request.target.id,
			group:    request.group,
			snapshot: desiredSnapshot,
		}
	}
	return runtimeAdminActionPlan{
		action:   runtimeAdminActionPushFullConfig,
		targetID: request.target.id,
		group:    request.group,
		snapshot: desiredSnapshot,
	}
}

func (c runtimeAdminCoordinator) planScannedRecovery(request runtimeAdminRequest) runtimeAdminActionPlan {
	if len(request.targetTunnels) == 0 || request.target.hasPendingConfig() || c.server.isShuttingDown() {
		return runtimeAdminActionPlan{}
	}
	if request.target.group.EffectiveIP != request.group.EffectiveIP {
		return runtimeAdminActionPlan{}
	}
	if sameRuntimeSnapshot(request.target.snapshot, request.group.Snapshot) {
		if !hasRecoverableScannedTunnels(request.targetTunnels, request.staticConflictIDs, request.issues) {
			return runtimeAdminActionPlan{}
		}
		return runtimeAdminActionPlan{
			action:   runtimeAdminActionEnsureListeners,
			targetID: request.target.id,
		}
	}
	if !shouldRecoverScannedActiveSessionConfig(request.target.snapshot, request.group.Snapshot) {
		return runtimeAdminActionPlan{}
	}
	if _, err := c.server.resolveGroupEffectiveIP(request.group); err != nil {
		return runtimeAdminActionPlan{}
	}
	return runtimeAdminActionPlan{
		action:   runtimeAdminActionPushFullConfig,
		targetID: request.target.id,
		group:    request.group,
		snapshot: request.group.Snapshot,
	}
}

func (c runtimeAdminCoordinator) execute(logger Logger, plan runtimeAdminActionPlan) error {
	switch plan.action {
	case runtimeAdminActionNoop:
		return nil
	}
	if c.server == nil || c.server.runtimeRegistry == nil || plan.targetID.GroupID == 0 || plan.targetID.SessionID == 0 {
		return nil
	}

	target, ok := c.server.lockRuntimeAdminOperationTarget(plan.targetID)
	if !ok {
		return nil
	}
	defer target.unlock()

	return c.executeLocked(logger, target, plan)
}

func (c runtimeAdminCoordinator) executeLocked(logger Logger, target runtimeAdminOperationTarget, plan runtimeAdminActionPlan) error {
	execLogger := logger
	if execLogger == nil {
		execLogger = c.server.logger
	}

	switch plan.action {
	case runtimeAdminActionSyncPendingConfig:
		target.session.refreshPendingConfig(plan.group, plan.snapshot)
		return nil
	case runtimeAdminActionKeepRuntime:
		target.session.replaceGroupRuntime(plan.group)
		return nil
	case runtimeAdminActionEnsureListeners:
		if target.conn == nil {
			return nil
		}
		target.session.setRecoveryMode(testsupport.RecoveryModeListenerRecovery)
		return c.server.ensureTunnelListeners(target.conn, execLogger, target.session)
	case runtimeAdminActionRebindRuntime:
		if target.conn == nil {
			return nil
		}
		target.session.setRecoveryMode(testsupport.RecoveryModeListenerRecovery)
		return c.server.rebindGroupRuntime(target.conn, target.session, plan.group)
	case runtimeAdminActionPushEmptyConfig:
		if target.conn == nil {
			return nil
		}
		if err := c.server.freezeGroupRuntime(target.conn, target.session); err != nil {
			return err
		}
		return c.server.pushReloadConfig(target.conn, target.session, plan.group, plan.snapshot)
	case runtimeAdminActionPushFullConfig:
		if target.conn == nil {
			return nil
		}
		if err := c.server.freezeGroupRuntime(target.conn, target.session); err != nil {
			return err
		}
		return c.server.pushReloadConfig(target.conn, target.session, plan.group, plan.snapshot)
	case runtimeAdminActionCloseSession:
		if target.conn == nil {
			return nil
		}
		return target.conn.Close()
	default:
		return nil
	}
}
