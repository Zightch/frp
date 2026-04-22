package control

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"syscall"

	"github.com/zightch/frp/frps/internal/ports"
	"github.com/zightch/frp/frps/internal/system"
	"github.com/zightch/frp/frps/internal/testhooks"
	"github.com/zightch/frp/frps/pkg/protocol"
)

type tcpTunnelListener struct {
	configVersion uint64
	tunnel        protocol.TunnelEntry
	remotePort    uint16
	listener      net.Listener
}

type udpTunnelListener struct {
	configVersion uint64
	tunnel        protocol.TunnelEntry
	remotePort    uint16
	listener      UDPListener
}

type tunnelListenerStartError struct {
	TunnelID    uint32
	Protocol    uint8
	EffectiveIP string
	RemotePort  uint16
	Cause       error
}

type groupEffectiveIPStartErrorKind uint8

const (
	groupEffectiveIPStartErrorInvalid groupEffectiveIPStartErrorKind = iota + 1
	groupEffectiveIPStartErrorNotLocal
)

type groupEffectiveIPStartError struct {
	EffectiveIP string
	Kind        groupEffectiveIPStartErrorKind
	Cause       error
}

func (e *tunnelListenerStartError) Error() string {
	if e == nil {
		return ""
	}
	return buildTunnelListenerStartReason(e.Protocol, e.EffectiveIP, e.RemotePort, e.Cause)
}

func (e *tunnelListenerStartError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Cause
}

func (e *groupEffectiveIPStartError) Error() string {
	if e == nil {
		return ""
	}
	switch e.Kind {
	case groupEffectiveIPStartErrorInvalid:
		return fmt.Sprintf("group effective_ip %q is invalid: %v", e.EffectiveIP, e.Cause)
	case groupEffectiveIPStartErrorNotLocal:
		return fmt.Sprintf("group effective_ip %q is not a current local IP", e.EffectiveIP)
	default:
		if e.Cause == nil {
			return fmt.Sprintf("group effective_ip %q failed", e.EffectiveIP)
		}
		return fmt.Sprintf("group effective_ip %q failed: %v", e.EffectiveIP, e.Cause)
	}
}

func (e *groupEffectiveIPStartError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Cause
}

func (s *Server) ensureTunnelListeners(conn net.Conn, logger Logger, session *sessionState) error {
	session.runtimeMu.Lock()
	if session.runtimeFrozen {
		session.runtimeMu.Unlock()
		return nil
	}
	session.runtimeMu.Unlock()

	group, snapshot := session.currentGroupAndSnapshot()
	if len(snapshot.Tunnels) == 0 {
		session.runtimeMu.Lock()
		if !session.hasRuntimeListenersLocked() {
			session.listenersStarted = false
			session.runtimeGeneration = 0
		}
		session.runtimeMu.Unlock()
		return nil
	}

	activeTunnelIDs := session.activeRuntimeTunnelIDs()
	targetTunnels := selectNonListeningEnabledTunnels(snapshot.Tunnels, activeTunnelIDs)
	for _, tunnel := range snapshot.Tunnels {
		if tunnel.TunnelFlags&protocol.TunnelFlagEnabled == 0 {
			s.recordTunnelRuntimeIssue(tunnel.TunnelID, "")
			continue
		}
		if _, active := activeTunnelIDs[tunnel.TunnelID]; active {
			s.recordTunnelRuntimeIssue(tunnel.TunnelID, "")
		}
	}
	if len(targetTunnels) == 0 {
		return nil
	}

	bindIP, err := s.resolveGroupEffectiveIP(group)
	if err != nil {
		reason := buildGroupEffectiveIPRuntimeReason(group, err)
		for _, tunnel := range enabledTunnels(snapshot) {
			s.recordTunnelRuntimeIssue(tunnel.TunnelID, reason)
		}
		return fmt.Errorf("%s: %w", reason, err)
	}
	conflictIssues := s.detectRuntimePortConflictIssues(group, bindIP, targetTunnels)
	for tunnelID, reason := range conflictIssues {
		s.recordTunnelRuntimeIssue(tunnelID, reason)
	}

	for _, tunnel := range targetTunnels {
		if reason := strings.TrimSpace(conflictIssues[tunnel.TunnelID]); reason != "" {
			logger.Warn(
				"skip tunnel listener start because runtime conflict was detected",
				"tunnel_id", tunnel.TunnelID,
				"group_id", group.ID,
				"reason", reason,
			)
			continue
		}
		started, startErr := s.startTunnelListeners(group.ID, snapshot.Version, tunnel, bindIP)
		if startErr != nil {
			s.recordTunnelRuntimeIssue(tunnel.TunnelID, startErr.Error())
			logger.Warn(
				"tunnel listener start failed",
				"tunnel_id", tunnel.TunnelID,
				"group_id", group.ID,
				"error", startErr,
			)
			continue
		}
		startUDPCleanup, attached := session.attachTunnelListeners(snapshot.Version, tunnel.TunnelID, started.tcpListeners, started.udpListeners)
		if !attached {
			closeStartedTunnelListeners(started.tcpListeners, started.udpListeners)
			return nil
		}
		if startUDPCleanup {
			go s.serveUDPIdleCleanup(conn, logger, session)
		}
		for _, runtime := range started.tcpRuntimes {
			logger.Info(
				"tcp tunnel listener ready",
				"tunnel_id", runtime.tunnel.TunnelID,
				"remote_port", runtime.remotePort,
				"addr", runtime.listener.Addr().String(),
			)
			go s.serveTunnelListener(conn, logger, session, runtime.configVersion, runtime.tunnel, runtime.remotePort, runtime.listener)
		}
		for _, runtime := range started.udpRuntimes {
			logger.Info(
				"udp tunnel listener ready",
				"tunnel_id", runtime.tunnel.TunnelID,
				"remote_port", runtime.remotePort,
				"addr", runtime.listener.LocalAddr().String(),
			)
			go s.serveUDPTunnelListener(conn, logger, session, runtime.configVersion, runtime.tunnel, runtime.remotePort, runtime.listener)
		}
		s.recordTunnelRuntimeIssue(tunnel.TunnelID, "")
	}
	return nil
}

type runtimeClaimOwner struct {
	GroupID     int64
	GroupName   string
	TunnelID    uint32
	EffectiveIP string
}

type runtimeGroupSnapshot struct {
	group    GroupRuntime
	snapshot ConfigSnapshot
}

func (s *Server) detectRuntimePortConflictIssues(group GroupRuntime, bindIP string, tunnels []protocol.TunnelEntry) map[uint32]string {
	claims, owners, targetOrder := buildRuntimeClaims(group, tunnels, bindIP)
	if len(targetOrder) == 0 {
		return nil
	}

	for _, active := range s.activeRuntimeGroups(nil) {
		otherBindIP, ok := normalizeRuntimeListenIP(active.group.EffectiveIP)
		if !ok {
			continue
		}
		otherClaims, otherOwners, _ := buildRuntimeClaims(active.group, active.snapshot.Tunnels, otherBindIP)
		claims = append(claims, otherClaims...)
		for ownerID, owner := range otherOwners {
			owners[ownerID] = owner
		}
	}

	conflicts := ports.DetectConflicts(claims)
	if len(conflicts) == 0 {
		return nil
	}

	issues := make(map[uint32]string)
	for _, tunnelID := range targetOrder {
		conflict, ok := conflicts[int64(tunnelID)]
		if !ok {
			continue
		}
		target, ok := owners[int64(tunnelID)]
		if !ok {
			continue
		}
		other, ok := owners[conflict.OtherOwnerID]
		if !ok {
			continue
		}
		reason := buildRuntimeConflictReason(target, other, conflict)
		issues[tunnelID] = reason
	}
	return issues
}

func buildRuntimeClaims(group GroupRuntime, tunnels []protocol.TunnelEntry, bindIP string) ([]ports.Claim, map[int64]runtimeClaimOwner, []uint32) {
	claims := make([]ports.Claim, 0, len(tunnels))
	owners := make(map[int64]runtimeClaimOwner)
	targetOrder := make([]uint32, 0, len(tunnels))
	for _, tunnel := range tunnels {
		if tunnel.TunnelFlags&protocol.TunnelFlagEnabled == 0 {
			continue
		}
		ownerID := int64(tunnel.TunnelID)
		claims = append(claims, ports.Claim{
			OwnerID:     ownerID,
			Protocol:    protocolName(tunnel.Protocol),
			EffectiveIP: bindIP,
			PortStart:   int64(tunnel.RemoteStart),
			PortEnd:     int64(tunnel.RemoteEnd),
		})
		owners[ownerID] = runtimeClaimOwner{
			GroupID:     group.ID,
			GroupName:   group.Name,
			TunnelID:    tunnel.TunnelID,
			EffectiveIP: bindIP,
		}
		targetOrder = append(targetOrder, tunnel.TunnelID)
	}
	return claims, owners, targetOrder
}

func (s *Server) activeRuntimeGroups(exclude *sessionState) []runtimeGroupSnapshot {
	if s == nil {
		return nil
	}

	s.mu.Lock()
	sessions := make([]*sessionState, 0, len(s.sessions))
	for _, active := range s.sessions {
		if active == nil || active.session == nil || active.session == exclude {
			continue
		}
		sessions = append(sessions, active.session)
	}
	s.mu.Unlock()

	result := make([]runtimeGroupSnapshot, 0, len(sessions))
	for _, activeSession := range sessions {
		activeTunnelIDs := activeSession.activeRuntimeTunnelIDs()
		if len(activeTunnelIDs) == 0 {
			continue
		}

		group, snapshot := activeSession.currentGroupAndSnapshot()
		snapshot.Tunnels = filterTunnelsByID(snapshot.Tunnels, activeTunnelIDs)
		if len(snapshot.Tunnels) == 0 {
			continue
		}
		result = append(result, runtimeGroupSnapshot{
			group:    group,
			snapshot: snapshot,
		})
	}
	return result
}

type tunnelListenerBatch struct {
	tcpListeners []net.Listener
	tcpRuntimes  []tcpTunnelListener
	udpListeners []UDPListener
	udpRuntimes  []udpTunnelListener
}

func (s *Server) startTunnelListeners(groupID int64, configVersion uint64, tunnel protocol.TunnelEntry, bindIP string) (tunnelListenerBatch, error) {
	started := tunnelListenerBatch{}
	switch tunnel.Protocol {
	case protocol.ProtocolTCP:
		started.tcpListeners = make([]net.Listener, 0, int(tunnel.RemoteEnd-tunnel.RemoteStart)+1)
		started.tcpRuntimes = make([]tcpTunnelListener, 0, int(tunnel.RemoteEnd-tunnel.RemoteStart)+1)
		for remotePort := int(tunnel.RemoteStart); remotePort <= int(tunnel.RemoteEnd); remotePort++ {
			bind := ListenerBind{
				GroupID:       groupID,
				TunnelID:      tunnel.TunnelID,
				ConfigVersion: configVersion,
				Kind:          BindKindRuntimeStart,
				Key: ListenKey{
					Protocol: "tcp",
					IP:       bindIP,
					Port:     uint16(remotePort),
				},
			}
			listener, err := s.listenTCP(context.Background(), bind)
			if err != nil {
				closeStartedTunnelListeners(started.tcpListeners, started.udpListeners)
				return tunnelListenerBatch{}, &tunnelListenerStartError{
					TunnelID:    tunnel.TunnelID,
					Protocol:    tunnel.Protocol,
					EffectiveIP: bindIP,
					RemotePort:  uint16(remotePort),
					Cause:       err,
				}
			}
			started.tcpListeners = append(started.tcpListeners, listener)
			started.tcpRuntimes = append(started.tcpRuntimes, tcpTunnelListener{
				configVersion: configVersion,
				tunnel:        tunnel,
				remotePort:    uint16(remotePort),
				listener:      listener,
			})
		}
	case protocol.ProtocolUDP:
		started.udpListeners = make([]UDPListener, 0, int(tunnel.RemoteEnd-tunnel.RemoteStart)+1)
		started.udpRuntimes = make([]udpTunnelListener, 0, int(tunnel.RemoteEnd-tunnel.RemoteStart)+1)
		for remotePort := int(tunnel.RemoteStart); remotePort <= int(tunnel.RemoteEnd); remotePort++ {
			bind := ListenerBind{
				GroupID:       groupID,
				TunnelID:      tunnel.TunnelID,
				ConfigVersion: configVersion,
				Kind:          BindKindRuntimeStart,
				Key: ListenKey{
					Protocol: "udp",
					IP:       bindIP,
					Port:     uint16(remotePort),
				},
			}
			udpAddr, err := s.resolveUDPAddr(context.Background(), bind)
			if err != nil {
				closeStartedTunnelListeners(started.tcpListeners, started.udpListeners)
				return tunnelListenerBatch{}, &tunnelListenerStartError{
					TunnelID:    tunnel.TunnelID,
					Protocol:    tunnel.Protocol,
					EffectiveIP: bindIP,
					RemotePort:  uint16(remotePort),
					Cause:       err,
				}
			}
			listener, err := s.listenUDP(context.Background(), bind, udpAddr)
			if err != nil {
				closeStartedTunnelListeners(started.tcpListeners, started.udpListeners)
				return tunnelListenerBatch{}, &tunnelListenerStartError{
					TunnelID:    tunnel.TunnelID,
					Protocol:    tunnel.Protocol,
					EffectiveIP: bindIP,
					RemotePort:  uint16(remotePort),
					Cause:       err,
				}
			}
			started.udpListeners = append(started.udpListeners, listener)
			started.udpRuntimes = append(started.udpRuntimes, udpTunnelListener{
				configVersion: configVersion,
				tunnel:        tunnel,
				remotePort:    uint16(remotePort),
				listener:      listener,
			})
		}
	}
	return started, nil
}

func selectNonListeningEnabledTunnels(tunnels []protocol.TunnelEntry, activeTunnelIDs map[uint32]struct{}) []protocol.TunnelEntry {
	selected := make([]protocol.TunnelEntry, 0, len(tunnels))
	for _, tunnel := range tunnels {
		if tunnel.TunnelFlags&protocol.TunnelFlagEnabled == 0 {
			continue
		}
		if _, active := activeTunnelIDs[tunnel.TunnelID]; active {
			continue
		}
		selected = append(selected, tunnel)
	}
	return selected
}

func filterTunnelsByID(tunnels []protocol.TunnelEntry, activeTunnelIDs map[uint32]struct{}) []protocol.TunnelEntry {
	if len(activeTunnelIDs) == 0 {
		return nil
	}
	filtered := make([]protocol.TunnelEntry, 0, len(tunnels))
	for _, tunnel := range tunnels {
		if _, ok := activeTunnelIDs[tunnel.TunnelID]; !ok {
			continue
		}
		filtered = append(filtered, tunnel)
	}
	return filtered
}

func (s *Server) resolveGroupEffectiveIP(group GroupRuntime) (string, error) {
	effectiveIP, err := system.NormalizeListenIP(group.EffectiveIP)
	if err != nil {
		return "", &groupEffectiveIPStartError{
			EffectiveIP: group.EffectiveIP,
			Kind:        groupEffectiveIPStartErrorInvalid,
			Cause:       err,
		}
	}
	if system.IsSpecialListenIP(effectiveIP) {
		return effectiveIP, nil
	}
	if s.network != nil && !s.network.Current().HasIP(effectiveIP) {
		return "", &groupEffectiveIPStartError{
			EffectiveIP: effectiveIP,
			Kind:        groupEffectiveIPStartErrorNotLocal,
		}
	}
	return effectiveIP, nil
}

func closeStartedTunnelListeners(tcpListeners []net.Listener, udpListeners []UDPListener) {
	for _, listener := range tcpListeners {
		testhooks.Point(
			"control.listener.before_close",
			testhooks.F("protocol", "tcp"),
			testhooks.F("addr", listener.Addr().String()),
		)
		_ = listener.Close()
		testhooks.Point(
			"control.listener.after_close",
			testhooks.F("protocol", "tcp"),
			testhooks.F("addr", listener.Addr().String()),
		)
	}
	for _, listener := range udpListeners {
		testhooks.Point(
			"control.listener.before_close",
			testhooks.F("protocol", "udp"),
			testhooks.F("addr", listener.LocalAddr().String()),
		)
		_ = listener.Close()
		testhooks.Point(
			"control.listener.after_close",
			testhooks.F("protocol", "udp"),
			testhooks.F("addr", listener.LocalAddr().String()),
		)
	}
}

func protocolName(value uint8) string {
	switch value {
	case protocol.ProtocolTCP:
		return "tcp"
	case protocol.ProtocolUDP:
		return "udp"
	default:
		return fmt.Sprintf("protocol(%d)", value)
	}
}

func normalizeRuntimeListenIP(raw string) (string, bool) {
	normalized, err := system.NormalizeListenIP(raw)
	if err != nil {
		return "", false
	}
	return normalized, true
}

func buildRuntimeConflictReason(target, other runtimeClaimOwner, conflict ports.Conflict) string {
	return fmt.Sprintf(
		`与分组"%s"的 tunnel_id=%d 在 %s (%s) %s 上冲突，无法启动监听`,
		other.GroupName,
		other.TunnelID,
		strings.ToUpper(conflict.Protocol),
		formatConflictEffectiveIPs(conflict.OwnerEffectiveIP, conflict.OtherEffectiveIP),
		formatConflictPortRange(conflict.ConflictStart, conflict.ConflictEnd),
	)
}

func buildTunnelListenerStartReason(protocolValue uint8, effectiveIP string, remotePort uint16, cause error) string {
	addr := net.JoinHostPort(effectiveIP, strconv.Itoa(int(remotePort)))
	if isListenPortConflictError(cause) {
		return fmt.Sprintf("%s 监听 %s 端口冲突，无法启动", strings.ToUpper(protocolName(protocolValue)), addr)
	}
	return fmt.Sprintf("%s 监听 %s 启动失败: %v", strings.ToUpper(protocolName(protocolValue)), addr, cause)
}

func isListenPortConflictError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, syscall.EADDRINUSE) {
		return true
	}
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "address already in use") ||
		strings.Contains(message, "only one usage of each socket address") ||
		strings.Contains(message, "10048")
}

func formatConflictEffectiveIPs(ownerIP, otherIP string) string {
	if strings.TrimSpace(ownerIP) == "" {
		return strings.TrimSpace(otherIP)
	}
	if strings.TrimSpace(otherIP) == "" || ownerIP == otherIP {
		return ownerIP
	}
	return ownerIP + " <-> " + otherIP
}

func formatConflictPortRange(start, end int64) string {
	if start == end {
		return strconv.FormatInt(start, 10)
	}
	return fmt.Sprintf("%d-%d", start, end)
}

func (s *Server) serveTunnelListener(conn net.Conn, logger Logger, session *sessionState, configVersion uint64, tunnel protocol.TunnelEntry, remotePort uint16, listener net.Listener) {
	for {
		publicConn, err := listener.Accept()
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return
			}
			logger.Warn("tcp tunnel accept failed", "tunnel_id", tunnel.TunnelID, "error", err)
			continue
		}

		go s.handlePublicConnection(conn, logger, session, configVersion, tunnel, remotePort, publicConn)
	}
}

func (s *Server) serveUDPTunnelListener(conn net.Conn, logger Logger, session *sessionState, configVersion uint64, tunnel protocol.TunnelEntry, remotePort uint16, listener UDPListener) {
	buffer := make([]byte, protocol.MaxDataBodyLen)
	for {
		n, clientAddr, err := listener.ReadFromUDP(buffer)
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return
			}
			logger.Warn("udp tunnel read failed", "tunnel_id", tunnel.TunnelID, "error", err)
			continue
		}
		payload := append([]byte(nil), buffer[:n]...)
		if err := s.handlePublicUDPDatagram(conn, logger, session, configVersion, tunnel, remotePort, listener, clientAddr, payload); err != nil {
			logger.Warn(
				"udp tunnel forward failed",
				"tunnel_id", tunnel.TunnelID,
				"remote_port", remotePort,
				"client_addr", clientAddr.String(),
				"error", err,
			)
		}
	}
}
