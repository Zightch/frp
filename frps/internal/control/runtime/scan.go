package runtime

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/zightch/frp/frps/internal/testhooks"
	"github.com/zightch/frp/frps/pkg/protocol"
)

// ShouldRecoverScannedActiveSessionConfig determines whether the active session config should be recovered.
// Recovery is needed when the current snapshot has no tunnels but the next snapshot has tunnels,
// and the snapshots are different.
func ShouldRecoverScannedActiveSessionConfig(currentSnapshot, nextSnapshot ConfigSnapshot, sameRuntime bool) bool {
	if sameRuntime {
		return false
	}
	if len(currentSnapshot.Tunnels) != 0 {
		return false
	}
	return len(nextSnapshot.Tunnels) > 0
}

// HasRecoverableScannedTunnels checks if there are any tunnels that can be recovered.
// A tunnel is recoverable if it's not in static conflict and has no runtime issues.
func HasRecoverableScannedTunnels(targetTunnels []protocol.TunnelEntry, staticConflictIDs map[int64]struct{}, issues map[uint32]string) bool {
	for _, tunnel := range targetTunnels {
		if _, conflicted := staticConflictIDs[int64(tunnel.TunnelID)]; conflicted {
			continue
		}
		if strings.TrimSpace(issues[tunnel.TunnelID]) != "" {
			continue
		}
		return true
	}
	return false
}

// PreserveHealthyScannedTunnels returns the tunnel IDs that are healthy and can be preserved.
// A tunnel is preserved if it's not in static conflict and has no runtime issues.
func PreserveHealthyScannedTunnels(targetTunnels []protocol.TunnelEntry, staticConflictIDs map[int64]struct{}, issues map[uint32]string) map[uint32]struct{} {
	preserved := make(map[uint32]struct{})
	for _, tunnel := range targetTunnels {
		if _, conflicted := staticConflictIDs[int64(tunnel.TunnelID)]; conflicted {
			continue
		}
		if strings.TrimSpace(issues[tunnel.TunnelID]) != "" {
			continue
		}
		preserved[tunnel.TunnelID] = struct{}{}
	}
	if len(preserved) == 0 {
		return nil
	}
	return preserved
}

// CollectKnownTunnelIDs collects all tunnel IDs from the given group runtimes.
func CollectKnownTunnelIDs(groups []GroupRuntime) map[int64]struct{} {
	known := make(map[int64]struct{})
	for _, group := range groups {
		for _, tunnel := range group.Snapshot.Tunnels {
			known[int64(tunnel.TunnelID)] = struct{}{}
		}
	}
	return known
}

// GroupEffectiveIPStartErrorKind represents the kind of effective IP start error.
type GroupEffectiveIPStartErrorKind uint8

const (
	// GroupEffectiveIPStartErrorInvalid indicates the effective IP is invalid.
	GroupEffectiveIPStartErrorInvalid GroupEffectiveIPStartErrorKind = iota + 1
	// GroupEffectiveIPStartErrorNotLocal indicates the effective IP is not a local IP.
	GroupEffectiveIPStartErrorNotLocal
)

// GroupEffectiveIPStartError represents an error when starting a group with an effective IP.
type GroupEffectiveIPStartError struct {
	EffectiveIP string
	Kind        GroupEffectiveIPStartErrorKind
	Cause       error
}

// Error implements the error interface.
func (e *GroupEffectiveIPStartError) Error() string {
	if e == nil {
		return ""
	}
	switch e.Kind {
	case GroupEffectiveIPStartErrorInvalid:
		return fmt.Sprintf("group effective_ip %q is invalid: %v", e.EffectiveIP, e.Cause)
	case GroupEffectiveIPStartErrorNotLocal:
		return fmt.Sprintf("group effective_ip %q is not a current local IP", e.EffectiveIP)
	default:
		if e.Cause == nil {
			return fmt.Sprintf("group effective_ip %q failed", e.EffectiveIP)
		}
		return fmt.Sprintf("group effective_ip %q failed: %v", e.EffectiveIP, e.Cause)
	}
}

// Unwrap implements the errors.Unwrap interface.
func (e *GroupEffectiveIPStartError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Cause
}

// BuildGroupEffectiveIPRuntimeReason builds a runtime reason string from a group effective IP error.
func BuildGroupEffectiveIPRuntimeReason(group GroupRuntime, err error) string {
	var effectiveIPErr *GroupEffectiveIPStartError
	if errors.As(err, &effectiveIPErr) {
		switch effectiveIPErr.Kind {
		case GroupEffectiveIPStartErrorNotLocal:
			return fmt.Sprintf("生效 IP %q 当前不存在于本机，无法启动监听", group.EffectiveIP)
		case GroupEffectiveIPStartErrorInvalid:
			return fmt.Sprintf("生效 IP %q 无效，无法启动监听", group.EffectiveIP)
		}
	}

	message := strings.ToLower(strings.TrimSpace(err.Error()))
	switch {
	case strings.Contains(message, "not a current local ip"):
		return fmt.Sprintf("生效 IP %q 当前不存在于本机，无法启动监听", group.EffectiveIP)
	case strings.Contains(message, "is invalid"):
		return fmt.Sprintf("生效 IP %q 无效，无法启动监听", group.EffectiveIP)
	default:
		return fmt.Sprintf("生效 IP %q 无法启动监听: %v", group.EffectiveIP, err)
	}
}

// BuildInitialStartupRejectedReason builds a rejected reason string for initial startup failure.
// Returns the reason string and a boolean indicating if the error should be treated as a rejected reason.
func BuildInitialStartupRejectedReason(group GroupRuntime, err error) (string, bool) {
	var effectiveIPErr *GroupEffectiveIPStartError
	if !errors.As(err, &effectiveIPErr) {
		return "", false
	}

	switch effectiveIPErr.Kind {
	case GroupEffectiveIPStartErrorNotLocal:
		return fmt.Sprintf("生效 IP %q 当前不存在于本机，请联系管理员解决", group.EffectiveIP), true
	case GroupEffectiveIPStartErrorInvalid:
		return fmt.Sprintf("生效 IP %q 无效，请联系管理员解决", group.EffectiveIP), true
	default:
		return "", false
	}
}

// ScanGroupRuntimeIssues scans for runtime issues in the given group's target tunnels.
// It returns a map of tunnel IDs to their issue strings.
func ScanGroupRuntimeIssues(
	resolver RuntimeIPResolver,
	prober RuntimeListenerProbe,
	group GroupRuntime,
	staticConflictIDs map[int64]struct{},
	targetTunnels []protocol.TunnelEntry,
	activeGroups []RuntimeGroupData,
) map[uint32]string {
	if !group.Enabled {
		return nil
	}

	if len(targetTunnels) == 0 {
		return nil
	}

	bindIP, err := resolver.ResolveGroupEffectiveIP(group)
	if err != nil {
		reason := BuildGroupEffectiveIPRuntimeReason(group, err)
		issues := make(map[uint32]string, len(targetTunnels))
		for _, tunnel := range targetTunnels {
			if _, conflicted := staticConflictIDs[int64(tunnel.TunnelID)]; conflicted {
				continue
			}
			issues[tunnel.TunnelID] = reason
		}
		return issues
	}

	groupData := RuntimeGroupDataFromGroup(group, targetTunnels)
	issues := DetectRuntimePortConflictIssuesWithData(groupData, bindIP, targetTunnels, activeGroups)
	if len(issues) == 0 {
		issues = make(map[uint32]string)
	}

	for _, tunnel := range targetTunnels {
		if _, conflicted := staticConflictIDs[int64(tunnel.TunnelID)]; conflicted {
			continue
		}
		if strings.TrimSpace(issues[tunnel.TunnelID]) != "" {
			continue
		}
		if reason := prober.ProbeTunnelRuntimeIssue(group.ID, bindIP, tunnel); reason != "" {
			issues[tunnel.TunnelID] = reason
		}
	}

	if len(issues) == 0 {
		return nil
	}
	return issues
}

// ResolveGroupEffectiveIPFunc is a function type for resolving group effective IP.
type ResolveGroupEffectiveIPFunc func(group GroupRuntime) (string, error)

// ProbeTunnelRuntimeIssueFunc is a function type for probing tunnel runtime issues.
type ProbeTunnelRuntimeIssueFunc func(groupID int64, bindIP string, tunnel protocol.TunnelEntry) string

// ScanGroupRuntimeIssuesWithData scans for runtime issues using explicit data instead of an interface.
// It returns a map of tunnel IDs to their issue strings.
func ScanGroupRuntimeIssuesWithData(
	group GroupRuntime,
	staticConflictIDs map[int64]struct{},
	targetTunnels []protocol.TunnelEntry,
	resolveGroupEffectiveIP ResolveGroupEffectiveIPFunc,
	probeTunnelRuntimeIssue ProbeTunnelRuntimeIssueFunc,
	activeGroups []RuntimeGroupData,
) map[uint32]string {
	if !group.Enabled {
		return nil
	}

	if len(targetTunnels) == 0 {
		return nil
	}

	bindIP, err := resolveGroupEffectiveIP(group)
	if err != nil {
		reason := BuildGroupEffectiveIPRuntimeReason(group, err)
		issues := make(map[uint32]string, len(targetTunnels))
		for _, tunnel := range targetTunnels {
			if _, conflicted := staticConflictIDs[int64(tunnel.TunnelID)]; conflicted {
				continue
			}
			issues[tunnel.TunnelID] = reason
		}
		return issues
	}

	groupData := RuntimeGroupData{
		GroupID:     group.ID,
		GroupName:   group.Name,
		EffectiveIP: group.EffectiveIP,
		Tunnels:     targetTunnels,
	}
	issues := DetectRuntimePortConflictIssuesWithData(groupData, bindIP, targetTunnels, activeGroups)
	if len(issues) == 0 {
		issues = make(map[uint32]string)
	}

	for _, tunnel := range targetTunnels {
		if _, conflicted := staticConflictIDs[int64(tunnel.TunnelID)]; conflicted {
			continue
		}
		if strings.TrimSpace(issues[tunnel.TunnelID]) != "" {
			continue
		}
		if reason := probeTunnelRuntimeIssue(group.ID, bindIP, tunnel); reason != "" {
			issues[tunnel.TunnelID] = reason
		}
	}

	if len(issues) == 0 {
		return nil
	}
	return issues
}

// StartRuntimeIssuePolling starts the runtime issue polling loop.
func StartRuntimeIssuePolling(op RuntimeScannerDeps, parent context.Context) {
	if op == nil || op.IsShuttingDown() {
		return
	}

	pollCtx, cancel := context.WithCancel(parent)
	op.SetRuntimeScanCancel(cancel)

	task := op.Scheduler().Every(pollCtx, "control.runtime_scan_poll", op.RuntimeScanPoll(), func(ctx context.Context, _ time.Time) {
		if err := ScanNonListeningTunnelRuntimeIssues(op, ctx); err != nil && !errors.Is(err, context.Canceled) {
			op.Logger().Warn("scan non-listening tunnel runtime issues failed", "error", err)
		}
	})
	op.ScanWG().Add(1)
	go func() {
		defer op.ScanWG().Done()
		<-task.Done()
	}()
}

// ScanNonListeningTunnelRuntimeIssues scans for runtime issues in all groups.
func ScanNonListeningTunnelRuntimeIssues(op RuntimeScannerDeps, ctx context.Context) error {
	if op == nil {
		return nil
	}
	if !op.BeginRuntimeScanRound() {
		testhooks.Point("runtime.scan.skip_overlap")
		return nil
	}
	defer op.FinishRuntimeScanRound()

	if op.Repo() == nil {
		return errors.New("repository not configured")
	}

	testhooks.Point("runtime.scan.before_round")
	groups, err := op.Repo().ListGroupRuntimes(ctx)
	if err != nil {
		return err
	}

	knownTunnelIDs := CollectKnownTunnelIDs(groups)
	op.ClearUnknownTunnelRuntimeIssues(knownTunnelIDs)

	tunnelsByGroup := make(map[int64][]protocol.TunnelEntry)
	effectiveIPsByGroup := make(map[int64]string)
	for _, group := range groups {
		tunnelsByGroup[group.ID] = group.Snapshot.Tunnels
		effectiveIPsByGroup[group.ID] = group.EffectiveIP
	}
	staticConflictIDs := DetectConfiguredConflictTunnelIDs(tunnelsByGroup, effectiveIPsByGroup)
	viewIndex := op.RuntimeSnapshotIndex()

	for _, group := range groups {
		targetTunnels := viewIndex.SelectNonListeningEnabledTunnels(group)
		activeGroups := RuntimeGroupDataFromSnapshots(op.ActiveRuntimeGroups(nil))
		issues := ScanGroupRuntimeIssues(op, op, group, staticConflictIDs, targetTunnels, activeGroups)
		testhooks.Point("runtime.scan.before_group_recover",
			testhooks.F("group_id", group.ID),
			testhooks.F("target_tunnel_count", len(targetTunnels)),
		)
		if err := RecoverScannedActiveSessionTunnels(op, viewIndex, group, targetTunnels, staticConflictIDs, issues); err != nil {
			op.Logger().Warn("recover scanned non-listening tunnels failed", "group_id", group.ID, "error", err)
		}
		refreshedViewIndex := op.RuntimeSnapshotIndex()
		refreshedTargetTunnels := refreshedViewIndex.SelectNonListeningEnabledTunnels(group)
		preserveHealthyIssues := PreserveScannedHealthyRuntimeIssuesUntilRecovery(refreshedViewIndex, group, refreshedTargetTunnels, staticConflictIDs, issues)
		op.ApplyScannedTunnelRuntimeIssues(group.Snapshot, staticConflictIDs, issues, preserveHealthyIssues)
	}

	testhooks.Point("runtime.scan.after_round", testhooks.F("group_count", len(groups)))
	return nil
}

// RecoverScannedActiveSessionTunnels attempts to recover active session tunnels that are not listening.
func RecoverScannedActiveSessionTunnels(op RuntimeScannedSessionRecoveryDeps, viewIndex RuntimeSnapshotIndex, group GroupRuntime, targetTunnels []protocol.TunnelEntry, staticConflictIDs map[int64]struct{}, issues map[uint32]string) error {
	if op == nil || group.ID <= 0 || len(targetTunnels) == 0 {
		return nil
	}
	active, ok := op.ActiveSession(group.ID)
	if !ok || active == nil || active.Session == nil {
		return nil
	}

	target, ok := viewIndex.Session(group.ID)
	if !ok || target.HasPendingConfig() || op.IsShuttingDown() {
		return nil
	}

	if target.EffectiveIP != group.EffectiveIP {
		return nil
	}

	sameRuntime := SameRuntimeSnapshot(target.Snapshot, group.Snapshot)
	if sameRuntime {
		if !HasRecoverableScannedTunnels(targetTunnels, staticConflictIDs, issues) {
			return nil
		}
		op.Logger().Info(
			"requesting active session listener recovery after runtime prerequisites returned",
			"config_version", group.Snapshot.Version,
			"tunnel_count", len(group.Snapshot.Tunnels),
		)
		return op.RequestAuditedSessionRuntimeRecovery(target.ID.SessionID, targetTunnels)
	}

	if !ShouldRecoverScannedActiveSessionConfig(target.Snapshot, group.Snapshot, sameRuntime) {
		return nil
	}
	if _, err := op.ResolveGroupEffectiveIP(group); err != nil {
		return nil
	}

	op.Logger().Info(
		"recovering active session config after runtime prerequisites returned",
		"config_version", group.Snapshot.Version,
		"tunnel_count", len(group.Snapshot.Tunnels),
	)
	return op.ApplyActiveSessionConfigRecovery(group.ID, group)
}

// PreserveScannedHealthyRuntimeIssuesUntilRecovery preserves healthy runtime issues until recovery.
func PreserveScannedHealthyRuntimeIssuesUntilRecovery(viewIndex RuntimeSnapshotIndex, group GroupRuntime, targetTunnels []protocol.TunnelEntry, staticConflictIDs map[int64]struct{}, issues map[uint32]string) map[uint32]struct{} {
	if group.ID <= 0 || len(targetTunnels) == 0 {
		return nil
	}

	target, ok := viewIndex.Session(group.ID)
	if !ok {
		return nil
	}
	if target.HasPendingConfig() {
		return PreserveHealthyScannedTunnels(targetTunnels, staticConflictIDs, issues)
	}

	if target.EffectiveIP != group.EffectiveIP {
		return nil
	}
	sameRuntime := SameRuntimeSnapshot(target.Snapshot, group.Snapshot)
	if !sameRuntime && !ShouldRecoverScannedActiveSessionConfig(target.Snapshot, group.Snapshot, sameRuntime) {
		return nil
	}

	return PreserveHealthyScannedTunnels(targetTunnels, staticConflictIDs, issues)
}
