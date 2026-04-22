package control

import (
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"syscall"

	"github.com/zightch/frp/frps/internal/ports"
	"github.com/zightch/frp/frps/internal/system"
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
	listener      *net.UDPConn
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
	if session.listenersStarted || session.runtimeFrozen {
		session.runtimeMu.Unlock()
		return nil
	}
	session.runtimeMu.Unlock()

	group, snapshot := session.currentGroupAndSnapshot()
	startedTCP := make([]net.Listener, 0)
	startedTCPByTunnel := make(map[uint32][]net.Listener)
	startedTCPRuntimes := make([]tcpTunnelListener, 0)
	startedUDP := make([]*net.UDPConn, 0)
	startedUDPByTunnel := make(map[uint32][]*net.UDPConn)
	startedUDPRuntimes := make([]udpTunnelListener, 0)

	if len(snapshot.Tunnels) == 0 {
		session.runtimeMu.Lock()
		if session.listenersStarted || session.runtimeFrozen {
			session.runtimeMu.Unlock()
			return nil
		}
		session.listenersStarted = true
		session.runtimeMu.Unlock()
		s.clearTunnelRuntimeIssues(snapshot.Tunnels)
		return nil
	}

	s.clearTunnelRuntimeIssues(snapshot.Tunnels)
	bindIP, err := s.resolveGroupEffectiveIP(group)
	if err != nil {
		reason := buildGroupEffectiveIPRuntimeReason(group, err)
		for _, tunnel := range enabledTunnels(snapshot) {
			s.recordTunnelRuntimeIssue(tunnel.TunnelID, reason)
		}
		return fmt.Errorf("%s: %w", reason, err)
	}
	if issues, conflictErr := s.detectRuntimePortConflictIssues(session, group, snapshot, bindIP); conflictErr != nil {
		for tunnelID, reason := range issues {
			s.recordTunnelRuntimeIssue(tunnelID, reason)
		}
		return conflictErr
	}

	for _, tunnel := range snapshot.Tunnels {
		if tunnel.TunnelFlags&protocol.TunnelFlagEnabled == 0 {
			continue
		}
		switch tunnel.Protocol {
		case protocol.ProtocolTCP:
			for remotePort := int(tunnel.RemoteStart); remotePort <= int(tunnel.RemoteEnd); remotePort++ {
				addr := net.JoinHostPort(bindIP, strconv.Itoa(remotePort))
				listener, err := net.Listen("tcp", addr)
				if err != nil {
					closeStartedTunnelListeners(startedTCP, startedUDP)
					startErr := &tunnelListenerStartError{
						TunnelID:    tunnel.TunnelID,
						Protocol:    tunnel.Protocol,
						EffectiveIP: bindIP,
						RemotePort:  uint16(remotePort),
						Cause:       err,
					}
					s.recordTunnelRuntimeIssue(tunnel.TunnelID, startErr.Error())
					return startErr
				}
				startedTCP = append(startedTCP, listener)
				startedTCPByTunnel[tunnel.TunnelID] = append(startedTCPByTunnel[tunnel.TunnelID], listener)
				startedTCPRuntimes = append(startedTCPRuntimes, tcpTunnelListener{
					configVersion: snapshot.Version,
					tunnel:        tunnel,
					remotePort:    uint16(remotePort),
					listener:      listener,
				})
			}
		case protocol.ProtocolUDP:
			for remotePort := int(tunnel.RemoteStart); remotePort <= int(tunnel.RemoteEnd); remotePort++ {
				addr := net.JoinHostPort(bindIP, strconv.Itoa(remotePort))
				udpAddr, err := net.ResolveUDPAddr("udp", addr)
				if err != nil {
					closeStartedTunnelListeners(startedTCP, startedUDP)
					startErr := &tunnelListenerStartError{
						TunnelID:    tunnel.TunnelID,
						Protocol:    tunnel.Protocol,
						EffectiveIP: bindIP,
						RemotePort:  uint16(remotePort),
						Cause:       err,
					}
					s.recordTunnelRuntimeIssue(tunnel.TunnelID, startErr.Error())
					return startErr
				}
				listener, err := net.ListenUDP("udp", udpAddr)
				if err != nil {
					closeStartedTunnelListeners(startedTCP, startedUDP)
					startErr := &tunnelListenerStartError{
						TunnelID:    tunnel.TunnelID,
						Protocol:    tunnel.Protocol,
						EffectiveIP: bindIP,
						RemotePort:  uint16(remotePort),
						Cause:       err,
					}
					s.recordTunnelRuntimeIssue(tunnel.TunnelID, startErr.Error())
					return startErr
				}
				startedUDP = append(startedUDP, listener)
				startedUDPByTunnel[tunnel.TunnelID] = append(startedUDPByTunnel[tunnel.TunnelID], listener)
				startedUDPRuntimes = append(startedUDPRuntimes, udpTunnelListener{
					configVersion: snapshot.Version,
					tunnel:        tunnel,
					remotePort:    uint16(remotePort),
					listener:      listener,
				})
			}
		default:
			continue
		}
	}

	session.runtimeMu.Lock()
	if session.listenersStarted || session.runtimeFrozen {
		session.runtimeMu.Unlock()
		closeStartedTunnelListeners(startedTCP, startedUDP)
		return nil
	}
	for tunnelID, listeners := range startedTCPByTunnel {
		session.listeners[tunnelID] = append(session.listeners[tunnelID], listeners...)
	}
	for tunnelID, listeners := range startedUDPByTunnel {
		session.udpListeners[tunnelID] = append(session.udpListeners[tunnelID], listeners...)
	}
	session.listenersStarted = true
	session.runtimeGeneration = snapshot.Version
	startUDPCleanup := len(startedUDPRuntimes) > 0 && !session.udpCleanupStarted
	if startUDPCleanup {
		session.udpCleanupStarted = true
	}
	session.runtimeMu.Unlock()

	if startUDPCleanup {
		go s.serveUDPIdleCleanup(conn, logger, session)
	}

	for _, runtime := range startedTCPRuntimes {
		logger.Info(
			"tcp tunnel listener ready",
			"tunnel_id", runtime.tunnel.TunnelID,
			"remote_port", runtime.remotePort,
			"addr", runtime.listener.Addr().String(),
		)
		go s.serveTunnelListener(conn, logger, session, runtime.configVersion, runtime.tunnel, runtime.remotePort, runtime.listener)
	}
	for _, runtime := range startedUDPRuntimes {
		logger.Info(
			"udp tunnel listener ready",
			"tunnel_id", runtime.tunnel.TunnelID,
			"remote_port", runtime.remotePort,
			"addr", runtime.listener.LocalAddr().String(),
		)
		go s.serveUDPTunnelListener(conn, logger, session, runtime.configVersion, runtime.tunnel, runtime.remotePort, runtime.listener)
	}

	s.clearTunnelRuntimeIssues(snapshot.Tunnels)
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

func (s *Server) detectRuntimePortConflictIssues(session *sessionState, group GroupRuntime, snapshot ConfigSnapshot, bindIP string) (map[uint32]string, error) {
	claims, owners, targetOrder := buildRuntimeClaims(group, snapshot, bindIP)
	if len(targetOrder) == 0 {
		return nil, nil
	}

	for _, active := range s.activeRuntimeGroups(session) {
		otherBindIP, ok := normalizeRuntimeListenIP(active.group.EffectiveIP)
		if !ok {
			continue
		}
		otherClaims, otherOwners, _ := buildRuntimeClaims(active.group, active.snapshot, otherBindIP)
		claims = append(claims, otherClaims...)
		for ownerID, owner := range otherOwners {
			owners[ownerID] = owner
		}
	}

	conflicts := ports.DetectConflicts(claims)
	if len(conflicts) == 0 {
		return nil, nil
	}

	issues := make(map[uint32]string)
	var firstReason string
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
		if firstReason == "" {
			firstReason = reason
		}
	}
	if firstReason == "" {
		return nil, nil
	}
	return issues, errors.New(firstReason)
}

func buildRuntimeClaims(group GroupRuntime, snapshot ConfigSnapshot, bindIP string) ([]ports.Claim, map[int64]runtimeClaimOwner, []uint32) {
	claims := make([]ports.Claim, 0, len(snapshot.Tunnels))
	owners := make(map[int64]runtimeClaimOwner)
	targetOrder := make([]uint32, 0, len(snapshot.Tunnels))
	for _, tunnel := range snapshot.Tunnels {
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
		activeSession.runtimeMu.Lock()
		listenersStarted := activeSession.listenersStarted
		runtimeFrozen := activeSession.runtimeFrozen
		activeSession.runtimeMu.Unlock()
		if !listenersStarted || runtimeFrozen {
			continue
		}

		group, snapshot := activeSession.currentGroupAndSnapshot()
		result = append(result, runtimeGroupSnapshot{
			group:    group,
			snapshot: snapshot,
		})
	}
	return result
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

func closeStartedTunnelListeners(tcpListeners []net.Listener, udpListeners []*net.UDPConn) {
	for _, listener := range tcpListeners {
		_ = listener.Close()
	}
	for _, listener := range udpListeners {
		_ = listener.Close()
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

func (s *Server) serveUDPTunnelListener(conn net.Conn, logger Logger, session *sessionState, configVersion uint64, tunnel protocol.TunnelEntry, remotePort uint16, listener *net.UDPConn) {
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
