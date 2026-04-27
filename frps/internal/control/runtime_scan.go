package control

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"time"

	controlsession "github.com/zightch/frp/frps/internal/control/session"
	"github.com/zightch/frp/frps/internal/ports"
	"github.com/zightch/frp/frps/internal/testhooks"
	"github.com/zightch/frp/frps/pkg/protocol"
)

func (s *Server) startRuntimeIssuePolling(parent context.Context) {
	if s == nil || s.isShuttingDown() {
		return
	}

	pollCtx, cancel := context.WithCancel(parent)

	s.mu.Lock()
	if s.isShuttingDown() {
		s.mu.Unlock()
		cancel()
		return
	}
	s.runtimeScanCancel = cancel
	s.mu.Unlock()

	task := s.scheduler.Every(pollCtx, "control.runtime_scan_poll", s.options.RuntimeScanPoll, func(ctx context.Context, _ time.Time) {
		if err := s.scanNonListeningTunnelRuntimeIssues(ctx); err != nil && !errors.Is(err, context.Canceled) {
			s.logger.Warn("scan non-listening tunnel runtime issues failed", "error", err)
		}
	})
	s.scanWG.Add(1)
	go func() {
		defer s.scanWG.Done()
		<-task.Done()
	}()
}

func (s *Server) scanNonListeningTunnelRuntimeIssues(ctx context.Context) error {
	if s == nil {
		return nil
	}
	if !s.beginRuntimeScanRound() {
		testhooks.Point("runtime.scan.skip_overlap")
		return nil
	}
	defer s.finishRuntimeScanRound()
	if s.repo == nil {
		return errors.New("repository not configured")
	}

	testhooks.Point("runtime.scan.before_round")
	groups, err := s.repo.ListGroupRuntimes(ctx)
	if err != nil {
		return err
	}

	knownTunnelIDs := collectKnownTunnelIDs(groups)
	s.clearUnknownTunnelRuntimeIssues(knownTunnelIDs)

	staticConflictIDs := detectConfiguredConflictTunnelIDs(groups)
	viewIndex := s.runtimeSnapshotIndex()

	for _, group := range groups {
		targetTunnels := viewIndex.selectNonListeningEnabledTunnels(group)
		issues := s.scanGroupRuntimeIssues(group, staticConflictIDs, targetTunnels)
		preserveHealthyIssues := s.preserveScannedHealthyRuntimeIssuesUntilRecovery(viewIndex, group, targetTunnels, staticConflictIDs, issues)
		s.applyScannedTunnelRuntimeIssues(group.Snapshot, staticConflictIDs, issues, preserveHealthyIssues)
		testhooks.Point(
			"runtime.scan.before_group_recover",
			testhooks.F("group_id", group.ID),
			testhooks.F("target_tunnel_count", len(targetTunnels)),
		)
		if err := s.recoverScannedActiveSessionTunnels(viewIndex, group, targetTunnels, staticConflictIDs, issues); err != nil {
			s.logger.Warn("recover scanned non-listening tunnels failed", "group_id", group.ID, "error", err)
		}
	}

	testhooks.Point("runtime.scan.after_round", testhooks.F("group_count", len(groups)))
	return nil
}

func (s *Server) beginRuntimeScanRound() bool {
	if s == nil {
		return false
	}

	s.runtimeScanStateMu.Lock()
	defer s.runtimeScanStateMu.Unlock()
	if s.runtimeScanInFlight {
		return false
	}
	s.runtimeScanInFlight = true
	return true
}

func (s *Server) finishRuntimeScanRound() {
	if s == nil {
		return
	}

	s.runtimeScanStateMu.Lock()
	s.runtimeScanInFlight = false
	s.runtimeScanStateMu.Unlock()
}

func (s *Server) scanGroupRuntimeIssues(group GroupRuntime, staticConflictIDs map[int64]struct{}, targetTunnels []protocol.TunnelEntry) map[uint32]string {
	if !group.Enabled {
		return nil
	}

	if len(targetTunnels) == 0 {
		return nil
	}

	bindIP, err := s.resolveGroupEffectiveIP(group)
	if err != nil {
		reason := buildGroupEffectiveIPRuntimeReason(group, err)
		issues := make(map[uint32]string, len(targetTunnels))
		for _, tunnel := range targetTunnels {
			if _, conflicted := staticConflictIDs[int64(tunnel.TunnelID)]; conflicted {
				continue
			}
			issues[tunnel.TunnelID] = reason
		}
		return issues
	}

	issues := s.detectRuntimePortConflictIssues(group, bindIP, targetTunnels)
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
		if reason := s.probeTunnelRuntimeIssue(group.ID, bindIP, tunnel); reason != "" {
			issues[tunnel.TunnelID] = reason
		}
	}

	if len(issues) == 0 {
		return nil
	}
	return issues
}

func (s *Server) recoverScannedActiveSessionTunnels(viewIndex runtimeSnapshotIndex, group GroupRuntime, targetTunnels []protocol.TunnelEntry, staticConflictIDs map[int64]struct{}, issues map[uint32]string) error {
	if s == nil || group.ID <= 0 || len(targetTunnels) == 0 {
		return nil
	}
	active, ok := s.activeSession(group.ID)
	if !ok || active == nil || active.session == nil {
		return nil
	}

	target, ok := viewIndex.session(group.ID)
	if !ok || target.hasPendingConfig() || s.isShuttingDown() {
		return nil
	}

	if target.effectiveIP != group.EffectiveIP {
		return nil
	}

	logger := s.logger.With(
		"session_id", active.session.ID,
		"group_id", group.ID,
		"group_name", group.Name,
	)

	if sameRuntimeSnapshot(target.snapshot, group.Snapshot) {
		if !hasRecoverableScannedTunnels(targetTunnels, staticConflictIDs, issues) {
			return nil
		}
		logger.Info(
			"requesting active session listener recovery after runtime prerequisites returned",
			"config_version", group.Snapshot.Version,
			"tunnel_count", len(group.Snapshot.Tunnels),
		)
		return s.requestAuditedSessionRuntimeRecovery(target.id.SessionID, targetTunnels)
	}

	if !shouldRecoverScannedActiveSessionConfig(target.snapshot, group.Snapshot) {
		return nil
	}
	if _, err := s.resolveGroupEffectiveIP(group); err != nil {
		return nil
	}

	logger.Info(
		"recovering active session config after runtime prerequisites returned",
		"config_version", group.Snapshot.Version,
		"tunnel_count", len(group.Snapshot.Tunnels),
	)
	if runtime := s.runtimeExecutor(active.session.ID); runtime != nil {
		runtime.setDesiredGroup(group)
	}
	s.supervisor.UpdateDesiredRuntime(group.ID, desiredRuntimeFromGroup(group))
	return nil
}

func shouldRecoverScannedActiveSessionConfig(currentSnapshot, nextSnapshot ConfigSnapshot) bool {
	if sameRuntimeSnapshot(currentSnapshot, nextSnapshot) {
		return false
	}
	if len(currentSnapshot.Tunnels) != 0 {
		return false
	}
	return len(nextSnapshot.Tunnels) > 0
}

func hasRecoverableScannedTunnels(targetTunnels []protocol.TunnelEntry, staticConflictIDs map[int64]struct{}, issues map[uint32]string) bool {
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

func (s *Server) requestAuditedSessionRuntimeRecovery(sessionID uint64, targetTunnels []protocol.TunnelEntry) error {
	if s == nil || s.supervisor == nil || sessionID == 0 || len(targetTunnels) == 0 {
		return nil
	}

	state, ok := s.supervisor.SessionState(sessionID)
	if !ok {
		return nil
	}

	targetTunnelIDs := make(map[uint32]struct{}, len(targetTunnels))
	for _, tunnel := range targetTunnels {
		targetTunnelIDs[tunnel.TunnelID] = struct{}{}
	}

	dispatched := false
	if state.RuntimePhase == controlsession.RuntimePhaseActive {
		for key := range state.Bindings {
			tunnelID := tunnelIDForBinding(state, key)
			if _, ok := targetTunnelIDs[tunnelID]; !ok {
				continue
			}
			dispatched = s.supervisor.DispatchBySessionID(sessionID, controlsession.BindingClosed{
				Key:    key,
				Reason: "runtime audit detected missing listener",
			}) || dispatched
		}
	}
	if dispatched {
		s.awaitAuditedSessionRecovery(sessionID, targetTunnelIDs)
		return nil
	}
	s.supervisor.DispatchBySessionID(sessionID, controlsession.ReconcileRequested{
		Reason: "runtime_audit_recover",
	})
	s.awaitAuditedSessionRecovery(sessionID, targetTunnelIDs)
	return nil
}

func (s *Server) awaitAuditedSessionRecovery(sessionID uint64, targetTunnelIDs map[uint32]struct{}) {
	if s == nil || len(targetTunnelIDs) == 0 {
		return
	}

	runtime := s.runtimeExecutor(sessionID)
	if runtime == nil || runtime.session == nil {
		return
	}

	deadline := time.Now().Add(200 * time.Millisecond)
	for time.Now().Before(deadline) {
		active := runtime.session.activeRuntimeTunnelIDs()
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

func (s *Server) preserveScannedHealthyRuntimeIssuesUntilRecovery(viewIndex runtimeSnapshotIndex, group GroupRuntime, targetTunnels []protocol.TunnelEntry, staticConflictIDs map[int64]struct{}, issues map[uint32]string) map[uint32]struct{} {
	if s == nil || group.ID <= 0 || len(targetTunnels) == 0 {
		return nil
	}

	target, ok := viewIndex.session(group.ID)
	if !ok {
		return nil
	}
	if target.hasPendingConfig() {
		return preserveHealthyScannedTunnels(targetTunnels, staticConflictIDs, issues)
	}

	if target.effectiveIP != group.EffectiveIP {
		return nil
	}
	if !sameRuntimeSnapshot(target.snapshot, group.Snapshot) && !shouldRecoverScannedActiveSessionConfig(target.snapshot, group.Snapshot) {
		return nil
	}

	return preserveHealthyScannedTunnels(targetTunnels, staticConflictIDs, issues)
}

func preserveHealthyScannedTunnels(targetTunnels []protocol.TunnelEntry, staticConflictIDs map[int64]struct{}, issues map[uint32]string) map[uint32]struct{} {
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

func detectConfiguredConflictTunnelIDs(groups []GroupRuntime) map[int64]struct{} {
	claims := make([]ports.Claim, 0)
	for _, group := range groups {
		if !group.Enabled {
			continue
		}
		for _, tunnel := range group.Snapshot.Tunnels {
			if tunnel.TunnelFlags&protocol.TunnelFlagEnabled == 0 {
				continue
			}
			claims = append(claims, ports.Claim{
				OwnerID:     int64(tunnel.TunnelID),
				Protocol:    protocolName(tunnel.Protocol),
				EffectiveIP: group.EffectiveIP,
				PortStart:   int64(tunnel.RemoteStart),
				PortEnd:     int64(tunnel.RemoteEnd),
			})
		}
	}

	conflicts := ports.DetectConflicts(claims)
	conflicted := make(map[int64]struct{}, len(conflicts))
	for tunnelID := range conflicts {
		conflicted[tunnelID] = struct{}{}
	}
	return conflicted
}

func collectKnownTunnelIDs(groups []GroupRuntime) map[int64]struct{} {
	known := make(map[int64]struct{})
	for _, group := range groups {
		for _, tunnel := range group.Snapshot.Tunnels {
			known[int64(tunnel.TunnelID)] = struct{}{}
		}
	}
	return known
}

func enabledTunnels(snapshot ConfigSnapshot) []protocol.TunnelEntry {
	enabled := make([]protocol.TunnelEntry, 0, len(snapshot.Tunnels))
	for _, tunnel := range snapshot.Tunnels {
		if tunnel.TunnelFlags&protocol.TunnelFlagEnabled == 0 {
			continue
		}
		enabled = append(enabled, tunnel)
	}
	return enabled
}

func buildGroupEffectiveIPRuntimeReason(group GroupRuntime, err error) string {
	var effectiveIPErr *groupEffectiveIPStartError
	if errors.As(err, &effectiveIPErr) {
		switch effectiveIPErr.Kind {
		case groupEffectiveIPStartErrorNotLocal:
			return fmt.Sprintf("生效 IP %q 当前不存在于本机，无法启动监听", group.EffectiveIP)
		case groupEffectiveIPStartErrorInvalid:
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

func buildInitialStartupRejectedReason(group GroupRuntime, err error) (string, bool) {
	var effectiveIPErr *groupEffectiveIPStartError
	if !errors.As(err, &effectiveIPErr) {
		return "", false
	}

	switch effectiveIPErr.Kind {
	case groupEffectiveIPStartErrorNotLocal:
		return fmt.Sprintf("生效 IP %q 当前不存在于本机，请联系管理员解决", group.EffectiveIP), true
	case groupEffectiveIPStartErrorInvalid:
		return fmt.Sprintf("生效 IP %q 无效，请联系管理员解决", group.EffectiveIP), true
	default:
		return "", false
	}
}

func (s *Server) probeTunnelRuntimeIssue(groupID int64, bindIP string, tunnel protocol.TunnelEntry) string {
	opCtx := newTunnelRuntimeProbeContext(groupID, tunnel, bindIP)
	switch tunnel.Protocol {
	case protocol.ProtocolTCP:
		listeners := make([]net.Listener, 0, opCtx.remotePortCount())
		for remotePort := int(opCtx.tunnel.RemoteStart); remotePort <= int(opCtx.tunnel.RemoteEnd); remotePort++ {
			bind := opCtx.listenerBind(uint16(remotePort))
			listener, err := s.listenTCP(context.Background(), bind)
			if err != nil {
				closeStartedTunnelListeners(listeners, nil)
				return buildTunnelListenerStartReason(opCtx.tunnel.Protocol, opCtx.bindIP, uint16(remotePort), err)
			}
			listeners = append(listeners, listener)
		}
		closeStartedTunnelListeners(listeners, nil)
	case protocol.ProtocolUDP:
		listeners := make([]UDPListener, 0, opCtx.remotePortCount())
		for remotePort := int(opCtx.tunnel.RemoteStart); remotePort <= int(opCtx.tunnel.RemoteEnd); remotePort++ {
			bind := opCtx.listenerBind(uint16(remotePort))
			udpAddr, err := s.resolveUDPAddr(context.Background(), bind)
			if err != nil {
				closeStartedTunnelListeners(nil, listeners)
				return buildTunnelListenerStartReason(opCtx.tunnel.Protocol, opCtx.bindIP, uint16(remotePort), err)
			}
			listener, err := s.listenUDP(context.Background(), bind, udpAddr)
			if err != nil {
				closeStartedTunnelListeners(nil, listeners)
				return buildTunnelListenerStartReason(opCtx.tunnel.Protocol, opCtx.bindIP, uint16(remotePort), err)
			}
			listeners = append(listeners, listener)
		}
		closeStartedTunnelListeners(nil, listeners)
	}
	return ""
}
