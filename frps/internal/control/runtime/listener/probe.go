package listener

import "github.com/zightch/frp/frps/pkg/protocol"

type TunnelListenerStarter interface {
	StartTunnelListeners(opCtx TunnelListenerOperationContext) (TunnelListenerBatch, error)
}

func ProbeTunnelRuntimeIssue(
	starter TunnelListenerStarter,
	groupID int64,
	bindIP string,
	tunnel protocol.TunnelEntry,
) string {
	if starter == nil {
		return ""
	}
	return ProbeTunnelRuntimeIssueWithData(groupID, bindIP, tunnel, starter.StartTunnelListeners)
}

func ProbeTunnelRuntimeIssueWithData(
	groupID int64,
	bindIP string,
	tunnel protocol.TunnelEntry,
	startListeners func(opCtx TunnelListenerOperationContext) (TunnelListenerBatch, error),
) string {
	if startListeners == nil {
		return ""
	}

	opCtx := NewTunnelRuntimeProbeContext(groupID, tunnel, bindIP)
	switch tunnel.Protocol {
	case protocol.ProtocolTCP:
		listeners, err := startListeners(opCtx)
		if err != nil {
			return BuildTunnelListenerStartReason(opCtx.Tunnel.Protocol, opCtx.BindIP, 0, err)
		}
		CloseStartedTunnelListeners(listeners.TCPListeners, nil)
	case protocol.ProtocolUDP:
		listeners, err := startListeners(opCtx)
		if err != nil {
			return BuildTunnelListenerStartReason(opCtx.Tunnel.Protocol, opCtx.BindIP, 0, err)
		}
		CloseStartedTunnelListeners(nil, listeners.UDPListeners)
	}
	return ""
}
