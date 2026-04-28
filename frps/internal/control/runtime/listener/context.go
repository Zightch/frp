package listener

import (
	controlbind "github.com/zightch/frp/frps/internal/control/bind"
	controldomainruntime "github.com/zightch/frp/frps/internal/control/domain/runtime"
	"github.com/zightch/frp/frps/pkg/protocol"
)

type TunnelListenerOperationContext struct {
	GroupID       int64
	ConfigVersion uint64
	Tunnel        protocol.TunnelEntry
	BindIP        string
	Kind          controlbind.BindKind
}

func NewTunnelRuntimeStartContext(groupID int64, configVersion uint64, tunnel protocol.TunnelEntry, bindIP string) TunnelListenerOperationContext {
	return TunnelListenerOperationContext{
		GroupID:       groupID,
		ConfigVersion: configVersion,
		Tunnel:        tunnel,
		BindIP:        bindIP,
		Kind:          controlbind.BindKindRuntimeStart,
	}
}

func NewTunnelRuntimeProbeContext(groupID int64, tunnel protocol.TunnelEntry, bindIP string) TunnelListenerOperationContext {
	return TunnelListenerOperationContext{
		GroupID: groupID,
		Tunnel:  tunnel,
		BindIP:  bindIP,
		Kind:    controlbind.BindKindRuntimeProbe,
	}
}

func (c TunnelListenerOperationContext) RemotePortCount() int {
	return int(c.Tunnel.RemoteEnd-c.Tunnel.RemoteStart) + 1
}

func (c TunnelListenerOperationContext) ListenerBind(remotePort uint16) controlbind.ListenerBind {
	return controlbind.ListenerBind{
		GroupID:       c.GroupID,
		TunnelID:      c.Tunnel.TunnelID,
		ConfigVersion: c.ConfigVersion,
		Kind:          c.Kind,
		Key: controlbind.ListenKey{
			Protocol: controldomainruntime.ProtocolName(c.Tunnel.Protocol),
			IP:       c.BindIP,
			Port:     remotePort,
		},
	}
}

func (c TunnelListenerOperationContext) ListenerStartError(remotePort uint16, err error) error {
	return &TunnelListenerStartError{
		TunnelID:    c.Tunnel.TunnelID,
		Protocol:    c.Tunnel.Protocol,
		EffectiveIP: c.BindIP,
		RemotePort:  remotePort,
		Cause:       err,
	}
}

type TunnelListenerStartError struct {
	TunnelID    uint32
	Protocol    uint8
	EffectiveIP string
	RemotePort  uint16
	Cause       error
}

func (e *TunnelListenerStartError) Error() string {
	if e == nil {
		return ""
	}
	return BuildTunnelListenerStartReason(e.Protocol, e.EffectiveIP, e.RemotePort, e.Cause)
}

func (e *TunnelListenerStartError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Cause
}
