package control

import (
	"net"

	"github.com/zightch/frp/frps/pkg/protocol"
)

type sessionRuntimeStartTarget struct {
	conn     net.Conn
	logger   Logger
	session  *sessionState
	group    GroupRuntime
	snapshot ConfigSnapshot
}

type sessionRuntimeStartPlan struct {
	group               GroupRuntime
	snapshot            ConfigSnapshot
	blocked             bool
	activeRuntime       bool
	clearIssueTunnelIDs []uint32
	targetTunnels       []protocol.TunnelEntry
	bindIP              string
	bindErr             error
	conflictIssues      map[uint32]string
}

type tunnelListenerOperationContext struct {
	groupID       int64
	configVersion uint64
	tunnel        protocol.TunnelEntry
	bindIP        string
	kind          BindKind
}

type tunnelRuntimeServeContext struct {
	logger     Logger
	session    *sessionState
	runtimeIO  sessionRuntimeIOWriter
	tunnel     protocol.TunnelEntry
	remotePort uint16
}

func newSessionRuntimeStartTarget(conn net.Conn, logger Logger, session *sessionState) sessionRuntimeStartTarget {
	group, snapshot := session.currentGroupAndSnapshot()
	return sessionRuntimeStartTarget{
		conn:     conn,
		logger:   logger,
		session:  session,
		group:    group,
		snapshot: snapshot,
	}
}

func newTunnelRuntimeStartContext(groupID int64, configVersion uint64, tunnel protocol.TunnelEntry, bindIP string) tunnelListenerOperationContext {
	return tunnelListenerOperationContext{
		groupID:       groupID,
		configVersion: configVersion,
		tunnel:        tunnel,
		bindIP:        bindIP,
		kind:          BindKindRuntimeStart,
	}
}

func newTunnelRuntimeProbeContext(groupID int64, tunnel protocol.TunnelEntry, bindIP string) tunnelListenerOperationContext {
	return tunnelListenerOperationContext{
		groupID: groupID,
		tunnel:  tunnel,
		bindIP:  bindIP,
		kind:    BindKindRuntimeProbe,
	}
}

func (c tunnelListenerOperationContext) remotePortCount() int {
	return int(c.tunnel.RemoteEnd-c.tunnel.RemoteStart) + 1
}

func (c tunnelListenerOperationContext) listenerBind(remotePort uint16) ListenerBind {
	return ListenerBind{
		GroupID:       c.groupID,
		TunnelID:      c.tunnel.TunnelID,
		ConfigVersion: c.configVersion,
		Kind:          c.kind,
		Key: ListenKey{
			Protocol: protocolName(c.tunnel.Protocol),
			IP:       c.bindIP,
			Port:     remotePort,
		},
	}
}

func (c tunnelListenerOperationContext) listenerStartError(remotePort uint16, err error) error {
	return &tunnelListenerStartError{
		TunnelID:    c.tunnel.TunnelID,
		Protocol:    c.tunnel.Protocol,
		EffectiveIP: c.bindIP,
		RemotePort:  remotePort,
		Cause:       err,
	}
}

func (c tunnelListenerOperationContext) serveContext(server *Server, conn net.Conn, logger Logger, session *sessionState, remotePort uint16) tunnelRuntimeServeContext {
	return tunnelRuntimeServeContext{
		logger:     logger,
		session:    session,
		runtimeIO:  newSessionRuntimeIOWriter(server, conn, session, c.configVersion),
		tunnel:     c.tunnel,
		remotePort: remotePort,
	}
}
