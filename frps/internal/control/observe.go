package control

import (
	"context"
	"net"
	"sort"
	"strconv"
	"strings"

	"github.com/zightch/frp/frps/pkg/protocol"
	"github.com/zightch/frp/frps/pkg/testsupport"
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
	targetSelector := newRuntimeTargetSelector(registryState)
	runtimeIssues := s.TunnelRuntimeIssues()

	state := testsupport.ServerObservedState{
		InitialRuntimeScanDone: initialRuntimeScanDone,
		ControlListenerOpen:    controlListenerOpen,
		LoginGateOpen:          initialRuntimeScanDone && controlListenerOpen,
		GroupSlots:             registryState.groupSlots,
	}

	for _, session := range targetSelector.sessionsList() {
		sessionState, listeners, missing, connections := observeSessionState(session)
		state.Sessions = append(state.Sessions, sessionState)
		state.Listeners = append(state.Listeners, listeners...)
		state.MissingListeners = append(state.MissingListeners, missing...)
		state.Connections = append(state.Connections, connections...)
	}

	groups := s.observeGroups()
	staticConflictIDs := detectConfiguredConflictTunnelIDs(groups)
	for _, tunnel := range targetSelector.selectTunnels(groups, runtimeIssues, staticConflictIDs) {
		state.Tunnels = append(state.Tunnels, testsupport.TunnelObservedState{
			GroupID:        tunnel.id.GroupID,
			TunnelID:       tunnel.id.TunnelID,
			StaticConflict: tunnel.staticConflict,
			RuntimeIssue:   tunnel.runtimeIssue,
			RuntimeKind:    tunnel.runtimeKind,
			FinalStatus:    tunnel.finalStatus,
			FinalReason:    tunnel.finalReason,
		})
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

func observeSessionState(target runtimeSessionTarget) (testsupport.SessionObservedState, []testsupport.AttachedListenerObservedState, []testsupport.MissingListenerObservedState, []testsupport.ConnectionObservedState) {
	observed := testsupport.SessionObservedState{
		GroupID:                target.id.GroupID,
		SessionID:              target.id.SessionID,
		ConnID:                 target.connID,
		EffectiveIP:            target.effectiveIP,
		SnapshotVersion:        target.snapshot.Version,
		SnapshotTunnelCount:    len(target.snapshot.Tunnels),
		LastAckedConfigVersion: target.lastAckedConfigVersion,
		RuntimeFrozen:          target.runtimeFrozen,
		ListenersStarted:       target.listenersStarted,
		RuntimeGeneration:      target.runtimeGeneration,
		RecoveryMode:           target.recoveryMode,
		ActiveStreams:          target.activeStreamCount,
		ActiveUDPSessions:      target.activeUDPSessionCount,
	}
	if target.pending != nil {
		observed.Pending = &testsupport.PendingConfigObservedState{
			RequestID:   target.pending.RequestID,
			Version:     target.pending.Version,
			TunnelCount: target.pending.TunnelCount,
			EffectiveIP: target.pending.EffectiveIP,
		}
	}

	listeners := make([]testsupport.AttachedListenerObservedState, 0, len(target.listeners))
	for _, attached := range target.listeners {
		listeners = append(listeners, testsupport.AttachedListenerObservedState{
			GroupID:       attached.id.GroupID,
			SessionID:     attached.id.SessionID,
			TunnelID:      attached.id.TunnelID,
			Protocol:      attached.protocol,
			BindIP:        attached.bindIP,
			Port:          attached.port,
			ConfigVersion: attached.configVersion,
			Kind:          attached.kind,
		})
	}

	connections := make([]testsupport.ConnectionObservedState, 0, len(target.connections))
	for _, connection := range target.connections {
		connections = append(connections, testsupport.ConnectionObservedState{
			GroupID:        connection.id.GroupID,
			SessionID:      connection.id.SessionID,
			ConnectionID:   connection.id.ConnectionID,
			Kind:           connection.id.Kind,
			Protocol:       connection.protocol,
			TunnelID:       connection.tunnelID,
			RemotePort:     connection.remotePort,
			ClientAddr:     connection.clientAddr,
			OpenedAtMs:     connection.openedAtMs,
			LastActiveAtMs: connection.lastActiveAtMs,
			IdleTimeoutMs:  connection.idleTimeoutMs,
		})
	}

	missing := make([]testsupport.MissingListenerObservedState, 0, len(target.missingListeners))
	for _, listener := range target.missingListeners {
		missing = append(missing, testsupport.MissingListenerObservedState{
			GroupID:      listener.id.GroupID,
			SessionID:    listener.id.SessionID,
			TunnelID:     listener.id.TunnelID,
			Protocol:     listener.protocol,
			MissingPorts: listener.missingPorts,
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
