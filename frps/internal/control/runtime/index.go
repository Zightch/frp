package runtime

import (
	"strings"

	controldomainruntime "github.com/zightch/frp/frps/internal/control/domain/runtime"
	controlsession "github.com/zightch/frp/frps/internal/control/session"
	"github.com/zightch/frp/frps/pkg/protocol"
	"github.com/zightch/frp/frps/pkg/testsupport"
	"github.com/zightch/frp/frps/pkg/transport"
)

// NewRuntimeSnapshotIndex creates a new runtime snapshot index from a session snapshot.
func NewRuntimeSnapshotIndex(sessions []SessionSnapshot) RuntimeSnapshotIndex {
	index := RuntimeSnapshotIndex{
		Sessions:        make([]RuntimeSessionTarget, 0, len(sessions)),
		SessionsByID:    make(map[RuntimeSessionTargetID]RuntimeSessionTarget, len(sessions)),
		SessionsByGroup: make(map[int64]RuntimeSessionTarget, len(sessions)),
	}
	for _, sessionSnapshot := range sessions {
		target := NewRuntimeSessionTarget(sessionSnapshot)
		if target.ID.GroupID == 0 || target.ID.SessionID == 0 {
			continue
		}
		index.Sessions = append(index.Sessions, target)
		index.SessionsByID[target.ID] = target
		index.SessionsByGroup[target.ID.GroupID] = target
	}
	return index
}

// NewRuntimeSessionTarget creates a new runtime session target from a session snapshot.
func NewRuntimeSessionTarget(snapshot SessionSnapshot) RuntimeSessionTarget {
	config := BuildRuntimeObservedConfig(snapshot.State, snapshot.DesiredGroup)
	target := RuntimeSessionTarget{
		ID: RuntimeSessionTargetID{
			GroupID:   snapshot.GroupID,
			SessionID: snapshot.SessionID,
		},
		Conn:              snapshot.Conn,
		ConnID:            transport.ConnectionID(snapshot.Conn),
		EffectiveIP:       config.EffectiveIP,
		Snapshot:          config.Snapshot,
		LastAckedVersion:  config.LastAckedConfigVersion,
		Pending:           config.Pending,
		RecoveryMode:      snapshot.RecoveryMode,
		RuntimeFrozen:     snapshot.Runtime.Frozen,
		ListenersStarted:  snapshot.Runtime.ListenersStarted,
		RuntimeGeneration: snapshot.Runtime.Generation,
		ActiveStreamCount: snapshot.Runtime.ActiveStreamCount,
		ActiveUDPCount:    snapshot.Runtime.ActiveUDPSessionCount,
		ActiveTunnelIDs:   snapshot.Runtime.ActiveTunnelIDs,
		ListenersByTunnel: make(map[uint32][]RuntimeListenerTarget),
		MissingByTunnel:   make(map[uint32]RuntimeMissingListenerTarget),
		Connections:       make([]RuntimeConnectionTarget, 0, len(snapshot.Runtime.Connections)),
	}

	for _, attached := range snapshot.Runtime.AttachedListeners {
		listener := RuntimeListenerTarget{
			ID: RuntimeTunnelTargetID{
				GroupID:   target.ID.GroupID,
				SessionID: snapshot.SessionID,
				TunnelID:  attached.TunnelID,
			},
			Protocol:      attached.Protocol,
			BindIP:        attached.BindIP,
			Port:          attached.Port,
			ConfigVersion: snapshot.Runtime.Generation,
			Kind:          attached.Protocol,
		}
		target.Listeners = append(target.Listeners, listener)
		target.ListenersByTunnel[attached.TunnelID] = append(target.ListenersByTunnel[attached.TunnelID], listener)
	}

	for _, connection := range snapshot.Runtime.Connections {
		target.Connections = append(target.Connections, RuntimeConnectionTarget{
			ID: RuntimeConnectionTargetID{
				GroupID:      target.ID.GroupID,
				SessionID:    snapshot.SessionID,
				ConnectionID: connection.ConnectionID,
				Kind:         connection.Kind,
			},
			Protocol:       connection.Protocol,
			TunnelID:       connection.TunnelID,
			RemotePort:     connection.RemotePort,
			ClientAddr:     connection.ClientAddr,
			OpenedAtMs:     connection.OpenedAtMs,
			LastActiveAtMs: connection.LastActiveAtMs,
			IdleTimeoutMs:  connection.IdleTimeoutMs,
		})
	}

	target.MissingListeners = BuildRuntimeMissingListenerTargets(target.ID, target.Snapshot.Tunnels, target.ListenersByTunnel)
	for _, missing := range target.MissingListeners {
		target.MissingByTunnel[missing.ID.TunnelID] = missing
	}

	return target
}

// BuildRuntimeObservedConfig builds a runtime observed config from session state and desired group.
func BuildRuntimeObservedConfig(state controlsession.SessionState, desiredGroup GroupRuntime) RuntimeObservedConfig {
	config := RuntimeObservedConfig{
		EffectiveIP: desiredGroup.EffectiveIP,
		Snapshot:    desiredGroup.Snapshot,
	}

	if state.Applied != nil {
		config.Snapshot = ConfigSnapshotFromDesired(state.Applied.Snapshot)
		config.EffectiveIP = state.Applied.Snapshot.EffectiveIP
		config.LastAckedConfigVersion = state.Applied.Snapshot.Version
	} else if state.Desired != nil {
		config.Snapshot = ConfigSnapshotFromDesired(*state.Desired)
		config.EffectiveIP = state.Desired.EffectiveIP
	}

	if state.Pending != nil {
		config.Pending = &RuntimePendingConfigTarget{
			RequestID:   state.Pending.RequestID,
			Snapshot:    ConfigSnapshotFromDesired(state.Pending.Snapshot),
			EffectiveIP: state.Pending.Snapshot.EffectiveIP,
		}
	}

	return config
}

// BuildRuntimeMissingListenerTargets builds a list of missing listener targets for a session.
func BuildRuntimeMissingListenerTargets(id RuntimeSessionTargetID, tunnels []protocol.TunnelEntry, listenersByTunnel map[uint32][]RuntimeListenerTarget) []RuntimeMissingListenerTarget {
	missing := make([]RuntimeMissingListenerTarget, 0)
	for _, tunnel := range tunnels {
		if tunnel.TunnelFlags&protocol.TunnelFlagEnabled == 0 {
			continue
		}

		actualPorts := make(map[uint16]struct{}, len(listenersByTunnel[tunnel.TunnelID]))
		for _, listener := range listenersByTunnel[tunnel.TunnelID] {
			actualPorts[listener.Port] = struct{}{}
		}

		missingPorts := make([]uint16, 0)
		for port := tunnel.RemoteStart; port <= tunnel.RemoteEnd; port++ {
			if _, ok := actualPorts[port]; ok {
				if port == tunnel.RemoteEnd {
					break
				}
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

		missing = append(missing, RuntimeMissingListenerTarget{
			ID: RuntimeTunnelTargetID{
				GroupID:   id.GroupID,
				SessionID: id.SessionID,
				TunnelID:  tunnel.TunnelID,
			},
			Protocol:     ProtocolName(tunnel.Protocol),
			MissingPorts: missingPorts,
		})
	}
	return missing
}

// SessionsList returns the list of sessions in the index.
func (s RuntimeSnapshotIndex) SessionsList() []RuntimeSessionTarget {
	return s.Sessions
}

// Session returns the session target for a given group ID.
func (s RuntimeSnapshotIndex) Session(groupID int64) (RuntimeSessionTarget, bool) {
	target, ok := s.SessionsByGroup[groupID]
	return target, ok
}

// SessionByID returns the session target for a given session ID.
func (s RuntimeSnapshotIndex) SessionByID(id RuntimeSessionTargetID) (RuntimeSessionTarget, bool) {
	target, ok := s.SessionsByID[id]
	return target, ok
}

// SelectNonListeningEnabledTunnels returns enabled tunnels that are not currently listening.
func (s RuntimeSnapshotIndex) SelectNonListeningEnabledTunnels(group GroupRuntime) []protocol.TunnelEntry {
	if !group.Enabled {
		return nil
	}

	session, ok := s.Session(group.ID)
	if !ok {
		return EnabledTunnels(group.Snapshot)
	}
	return SelectNonListeningEnabledTunnels(group.Snapshot.Tunnels, session.ActiveTunnelIDs)
}

// SelectTunnels returns tunnel targets for the given groups.
func (s RuntimeSnapshotIndex) SelectTunnels(groups []GroupRuntime, runtimeIssues map[int64]string, staticConflictIDs map[int64]struct{}, statusFn func(protocol.TunnelEntry, bool, string) (string, string), kindFn func(string) string) []RuntimeTunnelTarget {
	targets := make([]RuntimeTunnelTarget, 0)
	for _, group := range groups {
		session, hasSession := s.Session(group.ID)
		for _, tunnel := range group.Snapshot.Tunnels {
			targetID := RuntimeTunnelTargetID{
				GroupID:  group.ID,
				TunnelID: tunnel.TunnelID,
			}
			if hasSession {
				targetID.SessionID = session.ID.SessionID
			}

			runtimeReason := strings.TrimSpace(runtimeIssues[int64(tunnel.TunnelID)])
			staticConflict := false
			if _, ok := staticConflictIDs[int64(tunnel.TunnelID)]; ok {
				staticConflict = true
			}

			target := RuntimeTunnelTarget{
				ID:             targetID,
				Tunnel:         tunnel,
				StaticConflict: staticConflict,
				RuntimeIssue:   runtimeReason,
				RuntimeKind:    kindFn(runtimeReason),
			}
			target.FinalStatus, target.FinalReason = statusFn(tunnel, staticConflict, runtimeReason)

			if hasSession {
				target.Listeners = append(target.Listeners, session.ListenersByTunnel[tunnel.TunnelID]...)
				if missing, ok := session.MissingByTunnel[tunnel.TunnelID]; ok {
					target.MissingPorts = append(target.MissingPorts, missing.MissingPorts...)
				}
			}

			targets = append(targets, target)
		}
	}
	return targets
}

// HasPendingConfig returns true if the session has a pending config.
func (t RuntimeSessionTarget) HasPendingConfig() bool {
	return t.Pending != nil && t.Pending.RequestID != 0
}

// ObservedState returns the observed state for the session target.
func (t RuntimeSessionTarget) ObservedState() testsupport.SessionObservedState {
	observed := testsupport.SessionObservedState{
		GroupID:                t.ID.GroupID,
		SessionID:              t.ID.SessionID,
		ConnID:                 t.ConnID,
		EffectiveIP:            t.EffectiveIP,
		SnapshotVersion:        t.Snapshot.Version,
		SnapshotTunnelCount:    len(t.Snapshot.Tunnels),
		LastAckedConfigVersion: t.LastAckedVersion,
		RuntimeFrozen:          t.RuntimeFrozen,
		ListenersStarted:       t.ListenersStarted,
		RuntimeGeneration:      t.RuntimeGeneration,
		RecoveryMode:           t.RecoveryMode,
		ActiveStreams:          t.ActiveStreamCount,
		ActiveUDPSessions:      t.ActiveUDPCount,
	}
	if t.Pending != nil {
		observed.Pending = &testsupport.PendingConfigObservedState{
			RequestID:   t.Pending.RequestID,
			Version:     t.Pending.Snapshot.Version,
			TunnelCount: len(t.Pending.Snapshot.Tunnels),
			EffectiveIP: t.Pending.EffectiveIP,
		}
	}
	return observed
}

// ObservedListeners returns the observed listeners for the session target.
func (t RuntimeSessionTarget) ObservedListeners() []testsupport.AttachedListenerObservedState {
	listeners := make([]testsupport.AttachedListenerObservedState, 0, len(t.Listeners))
	for _, attached := range t.Listeners {
		listeners = append(listeners, testsupport.AttachedListenerObservedState{
			GroupID:       attached.ID.GroupID,
			SessionID:     attached.ID.SessionID,
			TunnelID:      attached.ID.TunnelID,
			Protocol:      attached.Protocol,
			BindIP:        attached.BindIP,
			Port:          attached.Port,
			ConfigVersion: attached.ConfigVersion,
			Kind:          attached.Kind,
		})
	}
	return listeners
}

// ObservedMissingListeners returns the observed missing listeners for the session target.
func (t RuntimeSessionTarget) ObservedMissingListeners() []testsupport.MissingListenerObservedState {
	missing := make([]testsupport.MissingListenerObservedState, 0, len(t.MissingListeners))
	for _, listener := range t.MissingListeners {
		ports := append([]uint16(nil), listener.MissingPorts...)
		missing = append(missing, testsupport.MissingListenerObservedState{
			GroupID:      listener.ID.GroupID,
			SessionID:    listener.ID.SessionID,
			TunnelID:     listener.ID.TunnelID,
			Protocol:     listener.Protocol,
			MissingPorts: ports,
		})
	}
	return missing
}

// ObservedConnections returns the observed connections for the session target.
func (t RuntimeSessionTarget) ObservedConnections() []testsupport.ConnectionObservedState {
	connections := make([]testsupport.ConnectionObservedState, 0, len(t.Connections))
	for _, connection := range t.Connections {
		connections = append(connections, testsupport.ConnectionObservedState{
			GroupID:        connection.ID.GroupID,
			SessionID:      connection.ID.SessionID,
			ConnectionID:   connection.ID.ConnectionID,
			Kind:           connection.ID.Kind,
			Protocol:       connection.Protocol,
			TunnelID:       connection.TunnelID,
			RemotePort:     connection.RemotePort,
			ClientAddr:     connection.ClientAddr,
			OpenedAtMs:     connection.OpenedAtMs,
			LastActiveAtMs: connection.LastActiveAtMs,
			IdleTimeoutMs:  connection.IdleTimeoutMs,
		})
	}
	return connections
}

// ObservedState returns the observed state for the tunnel target.
func (t RuntimeTunnelTarget) ObservedState() testsupport.TunnelObservedState {
	return testsupport.TunnelObservedState{
		GroupID:        t.ID.GroupID,
		TunnelID:       t.ID.TunnelID,
		StaticConflict: t.StaticConflict,
		RuntimeIssue:   t.RuntimeIssue,
		RuntimeKind:    t.RuntimeKind,
		FinalStatus:    t.FinalStatus,
		FinalReason:    t.FinalReason,
	}
}

// EnabledTunnels returns only the enabled tunnels from a snapshot.
func EnabledTunnels(snapshot ConfigSnapshot) []protocol.TunnelEntry {
	return controldomainruntime.EnabledTunnels(snapshot)
}

// SelectNonListeningEnabledTunnels returns enabled tunnels that are not in the active tunnel IDs set.
func SelectNonListeningEnabledTunnels(tunnels []protocol.TunnelEntry, activeTunnelIDs map[uint32]struct{}) []protocol.TunnelEntry {
	return controldomainruntime.SelectNonListeningEnabledTunnels(tunnels, activeTunnelIDs)
}
