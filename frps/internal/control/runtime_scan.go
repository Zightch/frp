package control

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/zightch/frp/frps/internal/ports"
	"github.com/zightch/frp/frps/pkg/protocol"
)

func (s *Server) startRuntimeIssuePolling(parent context.Context) {
	if s == nil {
		return
	}

	pollCtx, cancel := context.WithCancel(parent)

	s.mu.Lock()
	s.runtimeScanCancel = cancel
	s.mu.Unlock()

	s.scanWG.Add(1)
	go func() {
		defer s.scanWG.Done()

		ticker := time.NewTicker(s.options.RuntimeScanPoll)
		defer ticker.Stop()

		for {
			select {
			case <-pollCtx.Done():
				return
			case <-ticker.C:
				if err := s.scanNonListeningTunnelRuntimeIssues(pollCtx); err != nil && !errors.Is(err, context.Canceled) {
					s.logger.Warn("scan non-listening tunnel runtime issues failed", "error", err)
				}
			}
		}
	}()
}

func (s *Server) scanNonListeningTunnelRuntimeIssues(ctx context.Context) error {
	if s == nil {
		return nil
	}
	if s.repo == nil {
		return errors.New("repository not configured")
	}

	groups, err := s.repo.ListGroupRuntimes(ctx)
	if err != nil {
		return err
	}

	knownTunnelIDs := collectKnownTunnelIDs(groups)
	s.clearUnknownTunnelRuntimeIssues(knownTunnelIDs)

	staticConflictIDs := detectConfiguredConflictTunnelIDs(groups)

	for _, group := range groups {
		targetTunnels := s.selectScannedNonListeningTunnels(group)
		issues := s.scanGroupRuntimeIssues(group, staticConflictIDs, targetTunnels)
		s.applyScannedTunnelRuntimeIssues(group.Snapshot, staticConflictIDs, issues)
		if err := s.recoverScannedActiveSessionTunnels(group, targetTunnels, staticConflictIDs, issues); err != nil {
			s.logger.Warn("recover scanned non-listening tunnels failed", "group_id", group.ID, "error", err)
		}
	}

	return nil
}

func (s *Server) selectScannedNonListeningTunnels(group GroupRuntime) []protocol.TunnelEntry {
	if !group.Enabled {
		return nil
	}

	active, ok := s.activeSession(group.ID)
	if !ok || active == nil || active.session == nil {
		return enabledTunnels(group.Snapshot)
	}

	return selectNonListeningEnabledTunnels(group.Snapshot.Tunnels, active.session.activeRuntimeTunnelIDs())
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
		if reason := probeTunnelRuntimeIssue(bindIP, tunnel); reason != "" {
			issues[tunnel.TunnelID] = reason
		}
	}

	if len(issues) == 0 {
		return nil
	}
	return issues
}

func (s *Server) recoverScannedActiveSessionTunnels(group GroupRuntime, targetTunnels []protocol.TunnelEntry, staticConflictIDs map[int64]struct{}, issues map[uint32]string) error {
	if s == nil || group.ID <= 0 || len(targetTunnels) == 0 {
		return nil
	}
	if !hasRecoverableScannedTunnels(targetTunnels, staticConflictIDs, issues) {
		return nil
	}

	active, ok := s.activeSession(group.ID)
	if !ok || active == nil || active.conn == nil || active.session == nil {
		return nil
	}
	if active.session.hasPendingConfig() {
		return nil
	}

	currentGroup, currentSnapshot := active.session.currentGroupAndSnapshot()
	if currentGroup.EffectiveIP != group.EffectiveIP || !sameRuntimeSnapshot(currentSnapshot, group.Snapshot) {
		return nil
	}

	logger := s.logger.With(
		"session_id", active.session.ID,
		"group_id", currentGroup.ID,
		"group_name", currentGroup.Name,
	)
	return s.ensureTunnelListeners(active.conn, logger, active.session)
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

func (s *Server) applyScannedTunnelRuntimeIssues(snapshot ConfigSnapshot, staticConflictIDs map[int64]struct{}, issues map[uint32]string) {
	if s == nil {
		return
	}

	for _, tunnel := range snapshot.Tunnels {
		if tunnel.TunnelFlags&protocol.TunnelFlagEnabled == 0 {
			s.recordTunnelRuntimeIssue(tunnel.TunnelID, "")
			continue
		}
		if _, conflicted := staticConflictIDs[int64(tunnel.TunnelID)]; conflicted {
			s.recordTunnelRuntimeIssue(tunnel.TunnelID, "")
			continue
		}
		s.recordTunnelRuntimeIssue(tunnel.TunnelID, strings.TrimSpace(issues[tunnel.TunnelID]))
	}
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

func (s *Server) clearUnknownTunnelRuntimeIssues(knownTunnelIDs map[int64]struct{}) {
	if s == nil {
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	for tunnelID := range s.tunnelRuntimeIssues {
		if _, ok := knownTunnelIDs[tunnelID]; ok {
			continue
		}
		delete(s.tunnelRuntimeIssues, tunnelID)
	}
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

func probeTunnelRuntimeIssue(bindIP string, tunnel protocol.TunnelEntry) string {
	switch tunnel.Protocol {
	case protocol.ProtocolTCP:
		listeners := make([]net.Listener, 0, int(tunnel.RemoteEnd-tunnel.RemoteStart)+1)
		for remotePort := int(tunnel.RemoteStart); remotePort <= int(tunnel.RemoteEnd); remotePort++ {
			addr := net.JoinHostPort(bindIP, strconv.Itoa(remotePort))
			listener, err := net.Listen("tcp", addr)
			if err != nil {
				closeStartedTunnelListeners(listeners, nil)
				return buildTunnelListenerStartReason(tunnel.Protocol, bindIP, uint16(remotePort), err)
			}
			listeners = append(listeners, listener)
		}
		closeStartedTunnelListeners(listeners, nil)
	case protocol.ProtocolUDP:
		listeners := make([]*net.UDPConn, 0, int(tunnel.RemoteEnd-tunnel.RemoteStart)+1)
		for remotePort := int(tunnel.RemoteStart); remotePort <= int(tunnel.RemoteEnd); remotePort++ {
			addr := net.JoinHostPort(bindIP, strconv.Itoa(remotePort))
			udpAddr, err := net.ResolveUDPAddr("udp", addr)
			if err != nil {
				closeStartedTunnelListeners(nil, listeners)
				return buildTunnelListenerStartReason(tunnel.Protocol, bindIP, uint16(remotePort), err)
			}
			listener, err := net.ListenUDP("udp", udpAddr)
			if err != nil {
				closeStartedTunnelListeners(nil, listeners)
				return buildTunnelListenerStartReason(tunnel.Protocol, bindIP, uint16(remotePort), err)
			}
			listeners = append(listeners, listener)
		}
		closeStartedTunnelListeners(nil, listeners)
	}
	return ""
}
