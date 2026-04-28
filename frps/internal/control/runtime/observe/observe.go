package observe

import (
	"sort"
	"strings"

	controlruntime "github.com/zightch/frp/frps/internal/control/runtime"
	"github.com/zightch/frp/frps/pkg/protocol"
	"github.com/zightch/frp/frps/pkg/testsupport"
)

const (
	tunnelStatusEnabled  = "启用"
	tunnelStatusDisabled = "禁用"
	tunnelStatusConflict = "冲突"
	tunnelStatusAbnormal = "异常"
)

// LifecycleSnapshot is the server lifecycle state needed by observed projection.
type LifecycleSnapshot struct {
	InitialRuntimeScanDone bool
	ControlListenerOpen    bool
}

// Input contains all data needed to build the server observed state.
type Input struct {
	Lifecycle     LifecycleSnapshot
	GroupSlots    map[int64]uint64
	Sessions      []controlruntime.SessionSnapshot
	Groups        []controlruntime.GroupRuntime
	RuntimeIssues map[int64]string
}

// Build projects runtime snapshots and repository groups into public observed state.
func Build(input Input) testsupport.ServerObservedState {
	viewIndex := controlruntime.NewRuntimeSnapshotIndex(input.Sessions)
	state := testsupport.ServerObservedState{
		InitialRuntimeScanDone: input.Lifecycle.InitialRuntimeScanDone,
		ControlListenerOpen:    input.Lifecycle.ControlListenerOpen,
		LoginGateOpen:          input.Lifecycle.InitialRuntimeScanDone && input.Lifecycle.ControlListenerOpen,
		GroupSlots:             input.GroupSlots,
	}

	for _, session := range viewIndex.SessionsList() {
		state.Sessions = append(state.Sessions, session.ObservedState())
		state.Listeners = append(state.Listeners, session.ObservedListeners()...)
		state.MissingListeners = append(state.MissingListeners, session.ObservedMissingListeners()...)
		state.Connections = append(state.Connections, session.ObservedConnections()...)
	}

	staticConflictIDs := DetectStaticConflictTunnelIDs(input.Groups)
	for _, tunnel := range viewIndex.SelectTunnels(input.Groups, input.RuntimeIssues, staticConflictIDs, TunnelStatus, RuntimeIssueKind) {
		state.Tunnels = append(state.Tunnels, tunnel.ObservedState())
	}

	Sort(&state)
	return state
}

// DetectStaticConflictTunnelIDs detects configured tunnel conflicts for observed projection.
func DetectStaticConflictTunnelIDs(groups []controlruntime.GroupRuntime) map[int64]struct{} {
	tunnelsByGroup := make(map[int64][]protocol.TunnelEntry)
	effectiveIPsByGroup := make(map[int64]string)
	for _, group := range groups {
		tunnelsByGroup[group.ID] = group.Snapshot.Tunnels
		effectiveIPsByGroup[group.ID] = group.EffectiveIP
	}
	return controlruntime.DetectConfiguredConflictTunnelIDs(tunnelsByGroup, effectiveIPsByGroup)
}

// TunnelStatus maps tunnel config/runtime facts into the public status and reason.
func TunnelStatus(tunnel protocol.TunnelEntry, staticConflict bool, runtimeReason string) (string, string) {
	if tunnel.TunnelFlags&protocol.TunnelFlagEnabled == 0 {
		return tunnelStatusDisabled, ""
	}
	if staticConflict {
		return tunnelStatusConflict, "配置冲突"
	}
	if runtimeReason != "" {
		return tunnelStatusAbnormal, runtimeReason
	}
	return tunnelStatusEnabled, ""
}

// RuntimeIssueKind maps a runtime issue reason into a stable observed category.
func RuntimeIssueKind(reason string) string {
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

// Sort applies deterministic ordering to all observed collections.
func Sort(state *testsupport.ServerObservedState) {
	if state == nil {
		return
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
}
