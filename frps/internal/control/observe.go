package control

import (
	"context"
	"net"
	"sort"
	"strconv"
	"strings"

	"github.com/zightch/frp/frps/pkg/protocol"
	"github.com/zightch/frp/frps/pkg/testsupport"
	"github.com/zightch/frp/frps/pkg/transport"
)

const (
	observedTunnelStatusEnabled  = "启用"
	observedTunnelStatusDisabled = "禁用"
	observedTunnelStatusConflict = "冲突"
	observedTunnelStatusAbnormal = "异常"
)

func (s *Server) ObserveState() testsupport.ServerObservedState {
	if s == nil {
		return testsupport.ServerObservedState{}
	}

	s.mu.Lock()
	initialRuntimeScanDone := s.initialRuntimeScanDone
	controlListenerOpen := s.controlListenerOpen
	s.mu.Unlock()

	registryState := runtimeRegistrySnapshot{}
	if s.runtimeRegistry != nil {
		registryState = s.runtimeRegistry.snapshot()
	}
	runtimeIssues := s.TunnelRuntimeIssues()

	state := testsupport.ServerObservedState{
		InitialRuntimeScanDone: initialRuntimeScanDone,
		ControlListenerOpen:    controlListenerOpen,
		LoginGateOpen:          initialRuntimeScanDone && controlListenerOpen,
		GroupSlots:             registryState.groupSlots,
	}

	for _, session := range registryState.sessions {
		sessionState, listeners, missing, connections := observeSessionState(session)
		state.Sessions = append(state.Sessions, sessionState)
		state.Listeners = append(state.Listeners, listeners...)
		state.MissingListeners = append(state.MissingListeners, missing...)
		state.Connections = append(state.Connections, connections...)
	}

	groups := s.observeGroups()
	staticConflictIDs := detectConfiguredConflictTunnelIDs(groups)
	for _, group := range groups {
		for _, tunnel := range group.Snapshot.Tunnels {
			runtimeReason := strings.TrimSpace(runtimeIssues[int64(tunnel.TunnelID)])
			staticConflict := false
			if _, ok := staticConflictIDs[int64(tunnel.TunnelID)]; ok {
				staticConflict = true
			}
			finalStatus, finalReason := observedTunnelStatus(tunnel, staticConflict, runtimeReason)
			state.Tunnels = append(state.Tunnels, testsupport.TunnelObservedState{
				GroupID:        group.ID,
				TunnelID:       tunnel.TunnelID,
				StaticConflict: staticConflict,
				RuntimeIssue:   runtimeReason,
				RuntimeKind:    runtimeIssueKind(runtimeReason),
				FinalStatus:    finalStatus,
				FinalReason:    finalReason,
			})
		}
	}

	sort.Slice(state.Sessions, func(i, j int) bool {
		if state.Sessions[i].GroupID == state.Sessions[j].GroupID {
			return state.Sessions[i].SessionID < state.Sessions[j].SessionID
		}
		return state.Sessions[i].GroupID < state.Sessions[j].GroupID
	})
	sort.Slice(state.Tunnels, func(i, j int) bool {
		if state.Tunnels[i].GroupID == state.Tunnels[j].GroupID {
			return state.Tunnels[i].TunnelID < state.Tunnels[j].TunnelID
		}
		return state.Tunnels[i].GroupID < state.Tunnels[j].GroupID
	})
	sort.Slice(state.Listeners, func(i, j int) bool {
		if state.Listeners[i].GroupID == state.Listeners[j].GroupID {
			if state.Listeners[i].TunnelID == state.Listeners[j].TunnelID {
				return state.Listeners[i].Port < state.Listeners[j].Port
			}
			return state.Listeners[i].TunnelID < state.Listeners[j].TunnelID
		}
		return state.Listeners[i].GroupID < state.Listeners[j].GroupID
	})
	sort.Slice(state.MissingListeners, func(i, j int) bool {
		if state.MissingListeners[i].GroupID == state.MissingListeners[j].GroupID {
			return state.MissingListeners[i].TunnelID < state.MissingListeners[j].TunnelID
		}
		return state.MissingListeners[i].GroupID < state.MissingListeners[j].GroupID
	})
	sort.Slice(state.Connections, func(i, j int) bool {
		if state.Connections[i].GroupID == state.Connections[j].GroupID {
			if state.Connections[i].SessionID == state.Connections[j].SessionID {
				if state.Connections[i].Kind == state.Connections[j].Kind {
					return state.Connections[i].ConnectionID < state.Connections[j].ConnectionID
				}
				return state.Connections[i].Kind < state.Connections[j].Kind
			}
			return state.Connections[i].SessionID < state.Connections[j].SessionID
		}
		return state.Connections[i].GroupID < state.Connections[j].GroupID
	})

	return state
}

func (s *Server) observeGroups() []GroupRuntime {
	if s == nil || s.repo == nil {
		return nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), s.options.ReadTimeout)
	defer cancel()

	groups, err := s.repo.ListGroupRuntimes(ctx)
	if err != nil {
		return nil
	}
	return groups
}

func observeSessionState(snapshot runtimeSessionSnapshot) (testsupport.SessionObservedState, []testsupport.AttachedListenerObservedState, []testsupport.MissingListenerObservedState, []testsupport.ConnectionObservedState) {
	group := snapshot.config.group
	currentSnapshot := snapshot.config.snapshot

	observed := testsupport.SessionObservedState{
		GroupID:                group.ID,
		SessionID:              snapshot.sessionID,
		ConnID:                 transport.ConnectionID(snapshot.conn),
		EffectiveIP:            group.EffectiveIP,
		SnapshotVersion:        currentSnapshot.Version,
		SnapshotTunnelCount:    len(currentSnapshot.Tunnels),
		LastAckedConfigVersion: snapshot.config.lastAckedConfigValue,
		RuntimeFrozen:          snapshot.runtime.frozen,
		ListenersStarted:       snapshot.runtime.listenersStarted,
		RuntimeGeneration:      snapshot.runtime.generation,
		RecoveryMode:           snapshot.config.recoveryMode,
		ActiveStreams:          snapshot.runtime.activeStreamCount,
		ActiveUDPSessions:      snapshot.runtime.activeUDPSessionCount,
	}
	if snapshot.config.pendingRequestID != 0 {
		observed.Pending = &testsupport.PendingConfigObservedState{
			RequestID:   snapshot.config.pendingRequestID,
			Version:     snapshot.config.pendingSnapshot.Version,
			TunnelCount: len(snapshot.config.pendingSnapshot.Tunnels),
			EffectiveIP: snapshot.config.pendingGroup.EffectiveIP,
		}
	}

	listeners := make([]testsupport.AttachedListenerObservedState, 0)
	tunnelPorts := make(map[uint32]map[uint16]struct{})
	for _, attached := range snapshot.runtime.attachedListeners {
		if _, ok := tunnelPorts[attached.tunnelID]; !ok {
			tunnelPorts[attached.tunnelID] = make(map[uint16]struct{})
		}
		tunnelPorts[attached.tunnelID][attached.port] = struct{}{}
		listeners = append(listeners, testsupport.AttachedListenerObservedState{
			GroupID:       group.ID,
			SessionID:     snapshot.sessionID,
			TunnelID:      attached.tunnelID,
			Protocol:      attached.protocol,
			BindIP:        attached.bindIP,
			Port:          attached.port,
			ConfigVersion: snapshot.runtime.generation,
			Kind:          attached.protocol,
		})
	}

	connections := make([]testsupport.ConnectionObservedState, 0, len(snapshot.runtime.connections))
	for _, connection := range snapshot.runtime.connections {
		connections = append(connections, testsupport.ConnectionObservedState{
			GroupID:        group.ID,
			SessionID:      snapshot.sessionID,
			ConnectionID:   connection.connectionID,
			Kind:           connection.kind,
			Protocol:       connection.protocol,
			TunnelID:       connection.tunnelID,
			RemotePort:     connection.remotePort,
			ClientAddr:     connection.clientAddr,
			OpenedAtMs:     connection.openedAtMs,
			LastActiveAtMs: connection.lastActiveAtMs,
			IdleTimeoutMs:  connection.idleTimeoutMs,
		})
	}

	missing := make([]testsupport.MissingListenerObservedState, 0)
	for _, tunnel := range currentSnapshot.Tunnels {
		if tunnel.TunnelFlags&protocol.TunnelFlagEnabled == 0 {
			continue
		}
		actual := tunnelPorts[tunnel.TunnelID]
		var missingPorts []uint16
		for port := tunnel.RemoteStart; port <= tunnel.RemoteEnd; port++ {
			if _, ok := actual[port]; ok {
				continue
			}
			missingPorts = append(missingPorts, port)
			if port == tunnel.RemoteEnd {
				break
			}
		}
		if len(missingPorts) == 0 {
			continue
		}
		missing = append(missing, testsupport.MissingListenerObservedState{
			GroupID:      group.ID,
			SessionID:    snapshot.sessionID,
			TunnelID:     tunnel.TunnelID,
			Protocol:     protocolName(tunnel.Protocol),
			MissingPorts: missingPorts,
		})
	}

	return observed, listeners, missing, connections
}

func listenerAddr(addr net.Addr) (string, uint16) {
	if addr == nil {
		return "", 0
	}
	host, portText, err := net.SplitHostPort(addr.String())
	if err != nil {
		return addr.String(), 0
	}
	port, _ := strconv.Atoi(portText)
	return host, uint16(port)
}

func observedTunnelStatus(tunnel protocol.TunnelEntry, staticConflict bool, runtimeReason string) (string, string) {
	if tunnel.TunnelFlags&protocol.TunnelFlagEnabled == 0 {
		return observedTunnelStatusDisabled, ""
	}
	if staticConflict {
		return observedTunnelStatusConflict, "配置冲突"
	}
	if runtimeReason != "" {
		return observedTunnelStatusAbnormal, runtimeReason
	}
	return observedTunnelStatusEnabled, ""
}

func runtimeIssueKind(reason string) string {
	reason = strings.TrimSpace(strings.ToLower(reason))
	switch {
	case reason == "":
		return ""
	case strings.Contains(reason, "无效"):
		return "effective_ip_invalid"
	case strings.Contains(reason, "不存在于本机"):
		return "effective_ip_not_local"
	case strings.Contains(reason, "端口冲突"), strings.Contains(reason, "冲突"):
		return "runtime_bind_conflict"
	default:
		return "runtime_bind_error"
	}
}
