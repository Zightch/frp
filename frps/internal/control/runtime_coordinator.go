package control

import (
	"net"

	"github.com/zightch/frp/frps/pkg/protocol"
	"github.com/zightch/frp/frps/pkg/testsupport"
)

type runtimeRecoveryAction uint8

const (
	runtimeRecoveryActionNoop runtimeRecoveryAction = iota
	runtimeRecoveryActionKeepRuntime
	runtimeRecoveryActionEnsureListeners
	runtimeRecoveryActionRebindRuntime
	runtimeRecoveryActionPushEmptyConfig
	runtimeRecoveryActionPushFullConfig
	runtimeRecoveryActionCloseSession
)

func (a runtimeRecoveryAction) String() string {
	switch a {
	case runtimeRecoveryActionNoop:
		return "noop"
	case runtimeRecoveryActionKeepRuntime:
		return "keep_runtime"
	case runtimeRecoveryActionEnsureListeners:
		return "ensure_listeners"
	case runtimeRecoveryActionRebindRuntime:
		return "rebind_runtime"
	case runtimeRecoveryActionPushEmptyConfig:
		return "push_empty_config"
	case runtimeRecoveryActionPushFullConfig:
		return "push_full_config"
	case runtimeRecoveryActionCloseSession:
		return "close_session"
	default:
		return "unknown"
	}
}

// runtimeRecoveryPlan keeps recovery flow focused on a single internal
// operation, so refresh/scan callers decide "what to do" before executing it.
type runtimeRecoveryPlan struct {
	action   runtimeRecoveryAction
	group    GroupRuntime
	snapshot ConfigSnapshot
}

type runtimeRecoveryTarget struct {
	conn            net.Conn
	logger          Logger
	session         *sessionState
	currentGroup    GroupRuntime
	currentSnapshot ConfigSnapshot
}

type runtimeCoordinator struct {
	server *Server
}

func (s *Server) runtimeCoordinator() runtimeCoordinator {
	return runtimeCoordinator{server: s}
}

func newRuntimeRecoveryTarget(conn net.Conn, logger Logger, session *sessionState) runtimeRecoveryTarget {
	currentGroup, currentSnapshot := session.currentGroupAndSnapshot()
	return runtimeRecoveryTarget{
		conn:            conn,
		logger:          logger,
		session:         session,
		currentGroup:    currentGroup,
		currentSnapshot: currentSnapshot,
	}
}

func (c runtimeCoordinator) planRefresh(target runtimeRecoveryTarget, group GroupRuntime) runtimeRecoveryPlan {
	snapshot := runtimeSnapshotForGroup(group)
	desiredSnapshot := snapshot
	shrinkToEmpty := false
	if emptySnapshot, _, shouldShrinkToEmpty := c.server.runtimeRefreshSnapshot(group, snapshot); shouldShrinkToEmpty {
		desiredSnapshot = emptySnapshot
		shrinkToEmpty = true
	}
	if target.session.refreshPendingConfig(group, desiredSnapshot) {
		return runtimeRecoveryPlan{
			action:   runtimeRecoveryActionNoop,
			group:    group,
			snapshot: desiredSnapshot,
		}
	}
	if target.session.hasPendingConfig() {
		return runtimeRecoveryPlan{action: runtimeRecoveryActionCloseSession}
	}
	if shrinkToEmpty {
		return runtimeRecoveryPlan{
			action:   runtimeRecoveryActionPushEmptyConfig,
			group:    group,
			snapshot: desiredSnapshot,
		}
	}
	if sameRuntimeSnapshot(target.currentSnapshot, snapshot) {
		if target.currentGroup.EffectiveIP == group.EffectiveIP {
			return runtimeRecoveryPlan{
				action: runtimeRecoveryActionKeepRuntime,
				group:  group,
			}
		}
		return runtimeRecoveryPlan{
			action: runtimeRecoveryActionRebindRuntime,
			group:  group,
		}
	}
	if len(desiredSnapshot.Tunnels) == 0 {
		return runtimeRecoveryPlan{
			action:   runtimeRecoveryActionPushEmptyConfig,
			group:    group,
			snapshot: desiredSnapshot,
		}
	}
	return runtimeRecoveryPlan{
		action:   runtimeRecoveryActionPushFullConfig,
		group:    group,
		snapshot: desiredSnapshot,
	}
}

func (c runtimeCoordinator) planScannedRecovery(target runtimeRecoveryTarget, group GroupRuntime, targetTunnels []protocol.TunnelEntry, staticConflictIDs map[int64]struct{}, issues map[uint32]string) runtimeRecoveryPlan {
	if len(targetTunnels) == 0 || target.session.hasPendingConfig() || c.server.isShuttingDown() || target.session.isDone() {
		return runtimeRecoveryPlan{}
	}
	if target.currentGroup.EffectiveIP != group.EffectiveIP {
		return runtimeRecoveryPlan{}
	}
	if sameRuntimeSnapshot(target.currentSnapshot, group.Snapshot) {
		if !hasRecoverableScannedTunnels(targetTunnels, staticConflictIDs, issues) {
			return runtimeRecoveryPlan{}
		}
		return runtimeRecoveryPlan{action: runtimeRecoveryActionEnsureListeners}
	}
	if !shouldRecoverScannedActiveSessionConfig(target.currentSnapshot, group.Snapshot) {
		return runtimeRecoveryPlan{}
	}
	if _, err := c.server.resolveGroupEffectiveIP(group); err != nil {
		return runtimeRecoveryPlan{}
	}
	return runtimeRecoveryPlan{
		action:   runtimeRecoveryActionPushFullConfig,
		group:    group,
		snapshot: group.Snapshot,
	}
}

func (c runtimeCoordinator) execute(target runtimeRecoveryTarget, plan runtimeRecoveryPlan) error {
	switch plan.action {
	case runtimeRecoveryActionNoop:
		return nil
	case runtimeRecoveryActionKeepRuntime:
		target.session.replaceGroupRuntime(plan.group)
		return nil
	case runtimeRecoveryActionEnsureListeners:
		target.session.setRecoveryMode(testsupport.RecoveryModeListenerRecovery)
		return c.server.ensureTunnelListeners(target.conn, target.logger, target.session)
	case runtimeRecoveryActionRebindRuntime:
		target.session.setRecoveryMode(testsupport.RecoveryModeListenerRecovery)
		return c.server.rebindGroupRuntime(target.conn, target.session, plan.group)
	case runtimeRecoveryActionPushEmptyConfig:
		if err := c.server.freezeGroupRuntime(target.conn, target.session); err != nil {
			return err
		}
		return c.server.pushReloadConfig(target.conn, target.session, plan.group, plan.snapshot)
	case runtimeRecoveryActionPushFullConfig:
		if err := c.server.freezeGroupRuntime(target.conn, target.session); err != nil {
			return err
		}
		return c.server.pushReloadConfig(target.conn, target.session, plan.group, plan.snapshot)
	case runtimeRecoveryActionCloseSession:
		if target.conn == nil {
			return nil
		}
		return target.conn.Close()
	default:
		return nil
	}
}
