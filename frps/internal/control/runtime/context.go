package runtime

import (
	controllistener "github.com/zightch/frp/frps/internal/control/runtime/listener"
	"github.com/zightch/frp/frps/pkg/protocol"
)

type TunnelListenerOperationContext = controllistener.TunnelListenerOperationContext
type TunnelListenerStartError = controllistener.TunnelListenerStartError

func NewTunnelRuntimeStartContext(groupID int64, configVersion uint64, tunnel protocol.TunnelEntry, bindIP string) TunnelListenerOperationContext {
	return controllistener.NewTunnelRuntimeStartContext(groupID, configVersion, tunnel, bindIP)
}

func NewTunnelRuntimeProbeContext(groupID int64, tunnel protocol.TunnelEntry, bindIP string) TunnelListenerOperationContext {
	return controllistener.NewTunnelRuntimeProbeContext(groupID, tunnel, bindIP)
}
