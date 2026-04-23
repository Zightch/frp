package control

import (
	"net"
	"strings"

	"github.com/zightch/frp/frps/pkg/protocol"
	"github.com/zightch/frp/frps/pkg/testsupport"
	"github.com/zightch/frp/frps/pkg/transport"
)

type runtimeTargetSelector struct {
	sessions        []runtimeSessionTarget
	sessionsByID    map[runtimeSessionTargetID]runtimeSessionTarget
	sessionsByGroup map[int64]runtimeSessionTarget
}

type runtimeSessionTargetID struct {
	GroupID   int64
	SessionID uint64
}

type runtimeTunnelTargetID struct {
	GroupID   int64
	SessionID uint64
	TunnelID  uint32
}

type runtimeConnectionTargetID struct {
	GroupID      int64
	SessionID    uint64
	ConnectionID uint32
	Kind         string
}

type runtimePendingConfigTarget struct {
	requestID uint32
	group     GroupRuntime
	snapshot  ConfigSnapshot
}

type runtimeSessionTarget struct {
	id                       runtimeSessionTargetID
	group                    GroupRuntime
	conn                     net.Conn
	connID                   string
	effectiveIP              string
	snapshot                 ConfigSnapshot
	lastAckedConfigVersion   uint64
	pending                  *runtimePendingConfigTarget
	recoveryMode             testsupport.RecoveryMode
	runtimeFrozen            bool
	listenersStarted         bool
	runtimeGeneration        uint64
	activeStreamCount        uint32
	activeUDPSessionCount    uint32
	activeTunnelIDs          map[uint32]struct{}
	listeners                []runtimeListenerTarget
	listenersByTunnel        map[uint32][]runtimeListenerTarget
	missingListeners         []runtimeMissingListenerTarget
	missingListenersByTunnel map[uint32]runtimeMissingListenerTarget
	connections              []runtimeConnectionTarget
}

type runtimeListenerTarget struct {
	id            runtimeTunnelTargetID
	protocol      string
	bindIP        string
	port          uint16
	configVersion uint64
	kind          string
}

type runtimeMissingListenerTarget struct {
	id           runtimeTunnelTargetID
	protocol     string
	missingPorts []uint16
}

type runtimeConnectionTarget struct {
	id             runtimeConnectionTargetID
	protocol       string
	tunnelID       uint32
	remotePort     uint16
	clientAddr     string
	openedAtMs     uint64
	lastActiveAtMs uint64
	idleTimeoutMs  uint32
}

type runtimeTunnelTarget struct {
	id             runtimeTunnelTargetID
	tunnel         protocol.TunnelEntry
	staticConflict bool
	runtimeIssue   string
	runtimeKind    string
	finalStatus    string
	finalReason    string
	listeners      []runtimeListenerTarget
	missingPorts   []uint16
}

func newRuntimeTargetSelector(snapshot runtimeRegistrySnapshot) runtimeTargetSelector {
	selector := runtimeTargetSelector{
		sessions:        make([]runtimeSessionTarget, 0, len(snapshot.sessions)),
		sessionsByID:    make(map[runtimeSessionTargetID]runtimeSessionTarget, len(snapshot.sessions)),
		sessionsByGroup: make(map[int64]runtimeSessionTarget, len(snapshot.sessions)),
	}
	for _, session := range snapshot.sessions {
		target := newRuntimeSessionTarget(session)
		if target.id.GroupID == 0 || target.id.SessionID == 0 {
			continue
		}
		selector.sessions = append(selector.sessions, target)
		selector.sessionsByID[target.id] = target
		selector.sessionsByGroup[target.id.GroupID] = target
	}
	return selector
}

func newRuntimeSessionTarget(snapshot runtimeSessionSnapshot) runtimeSessionTarget {
	group := snapshot.config.group
	group.Snapshot = snapshot.config.snapshot
	target := runtimeSessionTarget{
		id: runtimeSessionTargetID{
			GroupID:   group.ID,
			SessionID: snapshot.sessionID,
		},
		group:                    group,
		conn:                     snapshot.conn,
		connID:                   transport.ConnectionID(snapshot.conn),
		effectiveIP:              group.EffectiveIP,
		snapshot:                 snapshot.config.snapshot,
		lastAckedConfigVersion:   snapshot.config.lastAckedConfigValue,
		recoveryMode:             snapshot.config.recoveryMode,
		runtimeFrozen:            snapshot.runtime.frozen,
		listenersStarted:         snapshot.runtime.listenersStarted,
		runtimeGeneration:        snapshot.runtime.generation,
		activeStreamCount:        snapshot.runtime.activeStreamCount,
		activeUDPSessionCount:    snapshot.runtime.activeUDPSessionCount,
		activeTunnelIDs:          snapshot.runtime.activeTunnelIDs,
		listenersByTunnel:        make(map[uint32][]runtimeListenerTarget),
		missingListenersByTunnel: make(map[uint32]runtimeMissingListenerTarget),
		connections:              make([]runtimeConnectionTarget, 0, len(snapshot.runtime.connections)),
	}
	if snapshot.config.pendingRequestID != 0 {
		pendingGroup := snapshot.config.pendingGroup
		pendingGroup.Snapshot = snapshot.config.pendingSnapshot
		target.pending = &runtimePendingConfigTarget{
			requestID: snapshot.config.pendingRequestID,
			group:     pendingGroup,
			snapshot:  snapshot.config.pendingSnapshot,
		}
	}

	for _, attached := range snapshot.runtime.attachedListeners {
		listener := runtimeListenerTarget{
			id: runtimeTunnelTargetID{
				GroupID:   group.ID,
				SessionID: snapshot.sessionID,
				TunnelID:  attached.tunnelID,
			},
			protocol:      attached.protocol,
			bindIP:        attached.bindIP,
			port:          attached.port,
			configVersion: snapshot.runtime.generation,
			kind:          attached.protocol,
		}
		target.listeners = append(target.listeners, listener)
		target.listenersByTunnel[attached.tunnelID] = append(target.listenersByTunnel[attached.tunnelID], listener)
	}

	for _, connection := range snapshot.runtime.connections {
		target.connections = append(target.connections, runtimeConnectionTarget{
			id: runtimeConnectionTargetID{
				GroupID:      group.ID,
				SessionID:    snapshot.sessionID,
				ConnectionID: connection.connectionID,
				Kind:         connection.kind,
			},
			protocol:       connection.protocol,
			tunnelID:       connection.tunnelID,
			remotePort:     connection.remotePort,
			clientAddr:     connection.clientAddr,
			openedAtMs:     connection.openedAtMs,
			lastActiveAtMs: connection.lastActiveAtMs,
			idleTimeoutMs:  connection.idleTimeoutMs,
		})
	}

	target.missingListeners = buildRuntimeMissingListenerTargets(target.id, target.snapshot.Tunnels, target.listenersByTunnel, target.missingListenersByTunnel)
	for _, missing := range target.missingListeners {
		target.missingListenersByTunnel[missing.id.TunnelID] = missing
	}

	return target
}

func buildRuntimeMissingListenerTargets(id runtimeSessionTargetID, tunnels []protocol.TunnelEntry, listenersByTunnel map[uint32][]runtimeListenerTarget, missingByTunnel map[uint32]runtimeMissingListenerTarget) []runtimeMissingListenerTarget {
	missing := make([]runtimeMissingListenerTarget, 0)
	for _, tunnel := range tunnels {
		if tunnel.TunnelFlags&protocol.TunnelFlagEnabled == 0 {
			continue
		}

		actualPorts := make(map[uint16]struct{}, len(listenersByTunnel[tunnel.TunnelID]))
		for _, listener := range listenersByTunnel[tunnel.TunnelID] {
			actualPorts[listener.port] = struct{}{}
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

		target := runtimeMissingListenerTarget{
			id: runtimeTunnelTargetID{
				GroupID:   id.GroupID,
				SessionID: id.SessionID,
				TunnelID:  tunnel.TunnelID,
			},
			protocol:     protocolName(tunnel.Protocol),
			missingPorts: missingPorts,
		}
		missing = append(missing, target)
		missingByTunnel[tunnel.TunnelID] = target
	}
	return missing
}

func (s runtimeTargetSelector) sessionsList() []runtimeSessionTarget {
	return s.sessions
}

func (s runtimeTargetSelector) session(groupID int64) (runtimeSessionTarget, bool) {
	target, ok := s.sessionsByGroup[groupID]
	return target, ok
}

func (s runtimeTargetSelector) sessionByID(id runtimeSessionTargetID) (runtimeSessionTarget, bool) {
	target, ok := s.sessionsByID[id]
	return target, ok
}

func (s runtimeTargetSelector) selectNonListeningEnabledTunnels(group GroupRuntime) []protocol.TunnelEntry {
	if !group.Enabled {
		return nil
	}

	session, ok := s.session(group.ID)
	if !ok {
		return enabledTunnels(group.Snapshot)
	}
	return selectNonListeningEnabledTunnels(group.Snapshot.Tunnels, session.activeTunnelIDs)
}

func (s runtimeTargetSelector) selectTunnels(groups []GroupRuntime, runtimeIssues map[int64]string, staticConflictIDs map[int64]struct{}) []runtimeTunnelTarget {
	targets := make([]runtimeTunnelTarget, 0)
	for _, group := range groups {
		session, hasSession := s.session(group.ID)
		for _, tunnel := range group.Snapshot.Tunnels {
			targetID := runtimeTunnelTargetID{
				GroupID:  group.ID,
				TunnelID: tunnel.TunnelID,
			}
			if hasSession {
				targetID.SessionID = session.id.SessionID
			}

			runtimeReason := strings.TrimSpace(runtimeIssues[int64(tunnel.TunnelID)])
			staticConflict := false
			if _, ok := staticConflictIDs[int64(tunnel.TunnelID)]; ok {
				staticConflict = true
			}

			target := runtimeTunnelTarget{
				id:             targetID,
				tunnel:         tunnel,
				staticConflict: staticConflict,
				runtimeIssue:   runtimeReason,
				runtimeKind:    runtimeIssueKind(runtimeReason),
			}
			target.finalStatus, target.finalReason = observedTunnelStatus(tunnel, staticConflict, runtimeReason)

			if hasSession {
				target.listeners = append(target.listeners, session.listenersByTunnel[tunnel.TunnelID]...)
				if missing, ok := session.missingListenersByTunnel[tunnel.TunnelID]; ok {
					target.missingPorts = append(target.missingPorts, missing.missingPorts...)
				}
			}

			targets = append(targets, target)
		}
	}
	return targets
}

func (t runtimeSessionTarget) hasPendingConfig() bool {
	return t.pending != nil && t.pending.requestID != 0
}

func (t runtimeSessionTarget) observedState() testsupport.SessionObservedState {
	observed := testsupport.SessionObservedState{
		GroupID:                t.id.GroupID,
		SessionID:              t.id.SessionID,
		ConnID:                 t.connID,
		EffectiveIP:            t.effectiveIP,
		SnapshotVersion:        t.snapshot.Version,
		SnapshotTunnelCount:    len(t.snapshot.Tunnels),
		LastAckedConfigVersion: t.lastAckedConfigVersion,
		RuntimeFrozen:          t.runtimeFrozen,
		ListenersStarted:       t.listenersStarted,
		RuntimeGeneration:      t.runtimeGeneration,
		RecoveryMode:           t.recoveryMode,
		ActiveStreams:          t.activeStreamCount,
		ActiveUDPSessions:      t.activeUDPSessionCount,
	}
	if t.pending != nil {
		observed.Pending = &testsupport.PendingConfigObservedState{
			RequestID:   t.pending.requestID,
			Version:     t.pending.snapshot.Version,
			TunnelCount: len(t.pending.snapshot.Tunnels),
			EffectiveIP: t.pending.group.EffectiveIP,
		}
	}
	return observed
}

func (t runtimeSessionTarget) observedListeners() []testsupport.AttachedListenerObservedState {
	listeners := make([]testsupport.AttachedListenerObservedState, 0, len(t.listeners))
	for _, attached := range t.listeners {
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
	return listeners
}

func (t runtimeSessionTarget) observedMissingListeners() []testsupport.MissingListenerObservedState {
	missing := make([]testsupport.MissingListenerObservedState, 0, len(t.missingListeners))
	for _, listener := range t.missingListeners {
		missing = append(missing, testsupport.MissingListenerObservedState{
			GroupID:      listener.id.GroupID,
			SessionID:    listener.id.SessionID,
			TunnelID:     listener.id.TunnelID,
			Protocol:     listener.protocol,
			MissingPorts: listener.missingPorts,
		})
	}
	return missing
}

func (t runtimeSessionTarget) observedConnections() []testsupport.ConnectionObservedState {
	connections := make([]testsupport.ConnectionObservedState, 0, len(t.connections))
	for _, connection := range t.connections {
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
	return connections
}

func (t runtimeTunnelTarget) observedState() testsupport.TunnelObservedState {
	return testsupport.TunnelObservedState{
		GroupID:        t.id.GroupID,
		TunnelID:       t.id.TunnelID,
		StaticConflict: t.staticConflict,
		RuntimeIssue:   t.runtimeIssue,
		RuntimeKind:    t.runtimeKind,
		FinalStatus:    t.finalStatus,
		FinalReason:    t.finalReason,
	}
}
