package runtime

import (
	controlbind "github.com/zightch/frp/frps/internal/control/bind"
	"github.com/zightch/frp/frps/pkg/protocol"
)

// TunnelListenerOperationContext holds context for tunnel listener operations.
type TunnelListenerOperationContext struct {
	GroupID       int64
	ConfigVersion uint64
	Tunnel        protocol.TunnelEntry
	BindIP        string
	Kind          controlbind.BindKind
}

// NewTunnelRuntimeStartContext creates a context for runtime start operations.
func NewTunnelRuntimeStartContext(groupID int64, configVersion uint64, tunnel protocol.TunnelEntry, bindIP string) TunnelListenerOperationContext {
	return TunnelListenerOperationContext{
		GroupID:       groupID,
		ConfigVersion: configVersion,
		Tunnel:        tunnel,
		BindIP:        bindIP,
		Kind:          controlbind.BindKindRuntimeStart,
	}
}

// NewTunnelRuntimeProbeContext creates a context for runtime probe operations.
func NewTunnelRuntimeProbeContext(groupID int64, tunnel protocol.TunnelEntry, bindIP string) TunnelListenerOperationContext {
	return TunnelListenerOperationContext{
		GroupID: groupID,
		Tunnel:  tunnel,
		BindIP:  bindIP,
		Kind:    controlbind.BindKindRuntimeProbe,
	}
}

// RemotePortCount returns the number of remote ports for the tunnel.
func (c TunnelListenerOperationContext) RemotePortCount() int {
	return int(c.Tunnel.RemoteEnd-c.Tunnel.RemoteStart) + 1
}

// ListenerBind creates a ListenerBind for the given remote port.
func (c TunnelListenerOperationContext) ListenerBind(remotePort uint16) controlbind.ListenerBind {
	return controlbind.ListenerBind{
		GroupID:       c.GroupID,
		TunnelID:      c.Tunnel.TunnelID,
		ConfigVersion: c.ConfigVersion,
		Kind:          c.Kind,
		Key: controlbind.ListenKey{
			Protocol: ProtocolName(c.Tunnel.Protocol),
			IP:       c.BindIP,
			Port:     remotePort,
		},
	}
}

// ListenerStartError creates a tunnel listener start error for the given remote port.
func (c TunnelListenerOperationContext) ListenerStartError(remotePort uint16, err error) error {
	return &TunnelListenerStartError{
		TunnelID:    c.Tunnel.TunnelID,
		Protocol:    c.Tunnel.Protocol,
		EffectiveIP: c.BindIP,
		RemotePort:  remotePort,
		Cause:       err,
	}
}

// TunnelListenerStartError represents an error when starting a tunnel listener.
type TunnelListenerStartError struct {
	TunnelID    uint32
	Protocol    uint8
	EffectiveIP string
	RemotePort  uint16
	Cause       error
}

// Error implements the error interface.
func (e *TunnelListenerStartError) Error() string {
	if e == nil {
		return ""
	}
	return BuildTunnelListenerStartReason(e.Protocol, e.EffectiveIP, e.RemotePort, e.Cause)
}

// Unwrap implements the errors.Unwrap interface.
func (e *TunnelListenerStartError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Cause
}
