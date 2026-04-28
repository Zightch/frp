package listener

import (
	"net"

	controlbind "github.com/zightch/frp/frps/internal/control/bind"
	"github.com/zightch/frp/frps/pkg/protocol"
)

type UDPListener = controlbind.UDPListener

type TunnelListenerBatch struct {
	TCPListeners []net.Listener
	TCPRuntimes  []TCPTunnelListener
	UDPListeners []UDPListener
	UDPRuntimes  []UDPTunnelListener
}

type TCPTunnelListener struct {
	ConfigVersion uint64
	Tunnel        protocol.TunnelEntry
	RemotePort    uint16
	Listener      net.Listener
}

type UDPTunnelListener struct {
	ConfigVersion uint64
	Tunnel        protocol.TunnelEntry
	RemotePort    uint16
	Listener      UDPListener
}
