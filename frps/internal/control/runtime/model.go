package runtime

import (
	"net"

	controlbind "github.com/zightch/frp/frps/internal/control/bind"
	controldomainruntime "github.com/zightch/frp/frps/internal/control/domain/runtime"
	controllistener "github.com/zightch/frp/frps/internal/control/runtime/listener"
	controlruntimestate "github.com/zightch/frp/frps/internal/control/runtime/state"
	controlsession "github.com/zightch/frp/frps/internal/control/session"
	"github.com/zightch/frp/frps/pkg/protocol"
	"github.com/zightch/frp/frps/pkg/testsupport"
)

type ObservedListener = controlruntimestate.ObservedListener
type ObservedConnection = controlruntimestate.ObservedConnection
type ObservedState = controlruntimestate.ObservedState

type SessionSnapshot struct {
	GroupID      int64
	SessionID    uint64
	Conn         net.Conn
	Config       ObservedConfigState
	RecoveryMode testsupport.RecoveryMode
	State        controlsession.SessionState
	Runtime      ObservedState
}

// RuntimeGroupSnapshot represents a snapshot of a runtime group.
type RuntimeGroupSnapshot struct {
	Group    GroupRuntime
	Snapshot ConfigSnapshot
}

// FilterTunnelsByID filters tunnels by the given tunnel IDs.
func FilterTunnelsByID(tunnels []protocol.TunnelEntry, tunnelIDs map[uint32]struct{}) []protocol.TunnelEntry {
	return controldomainruntime.FilterTunnelsByID(tunnels, tunnelIDs)
}

// ActiveRuntimeTunnelIDs returns the active runtime tunnel IDs for the session.
func (s SessionSnapshot) ActiveRuntimeTunnelIDs() map[uint32]struct{} {
	if s.Runtime.Frozen {
		return nil
	}
	return s.Runtime.ActiveTunnelIDs
}

// ActiveRuntimeGroup returns the active runtime group snapshot if there are active tunnel IDs.
func (s SessionSnapshot) ActiveRuntimeGroup() (RuntimeGroupSnapshot, bool) {
	activeTunnelIDs := s.ActiveRuntimeTunnelIDs()
	if len(activeTunnelIDs) == 0 {
		return RuntimeGroupSnapshot{}, false
	}

	group := s.Config.Group
	config := s.Config.RuntimeObservedConfig()
	snapshot := config.Snapshot
	snapshot.Tunnels = FilterTunnelsByID(snapshot.Tunnels, activeTunnelIDs)
	if len(snapshot.Tunnels) == 0 {
		return RuntimeGroupSnapshot{}, false
	}

	group.EffectiveIP = config.EffectiveIP
	group.Snapshot = snapshot
	return RuntimeGroupSnapshot{
		Group:    group,
		Snapshot: snapshot,
	}, true
}

// ListenerAddr extracts the bind IP and port from a net.Addr.
func ListenerAddr(addr net.Addr) (string, uint16) {
	return controlruntimestate.ListenerAddr(addr)
}

// ObserveRuntimeListeners builds a list of observed listeners from TCP and UDP listener maps.
func ObserveRuntimeListeners(tcp map[uint32][]net.Listener, udp map[uint32][]controlbind.UDPListener) []ObservedListener {
	return controlruntimestate.ObserveRuntimeListeners(tcp, udp)
}

// CloseStartedTunnelListeners closes the given TCP and UDP listeners.
func CloseStartedTunnelListeners(tcpListeners []net.Listener, udpListeners []controlbind.UDPListener) {
	controllistener.CloseStartedTunnelListeners(tcpListeners, udpListeners)
}
