package runtime

import (
	"net"
	"strconv"

	controlbind "github.com/zightch/frp/frps/internal/control/bind"
	controlsession "github.com/zightch/frp/frps/internal/control/session"
	"github.com/zightch/frp/frps/internal/testhooks"
	"github.com/zightch/frp/frps/pkg/protocol"
	"github.com/zightch/frp/frps/pkg/testsupport"
)

type ObservedListener struct {
	TunnelID uint32
	Protocol string
	BindIP   string
	Port     uint16
}

type ObservedConnection struct {
	ConnectionID   uint32
	Kind           string
	Protocol       string
	TunnelID       uint32
	RemotePort     uint16
	ClientAddr     string
	OpenedAtMs     uint64
	LastActiveAtMs uint64
	IdleTimeoutMs  uint32
}

type ObservedState struct {
	Frozen                bool
	ListenersStarted      bool
	Generation            uint64
	ActiveTunnelIDs       map[uint32]struct{}
	AttachedListeners     []ObservedListener
	ActiveStreamCount     uint32
	ActiveUDPSessionCount uint32
	Connections           []ObservedConnection
}

type SessionSnapshot struct {
	GroupID      int64
	SessionID    uint64
	Conn         net.Conn
	DesiredGroup GroupRuntime
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
	if len(tunnelIDs) == 0 {
		return nil
	}
	filtered := make([]protocol.TunnelEntry, 0, len(tunnels))
	for _, tunnel := range tunnels {
		if _, ok := tunnelIDs[tunnel.TunnelID]; !ok {
			continue
		}
		filtered = append(filtered, tunnel)
	}
	return filtered
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

	group := s.DesiredGroup
	config := BuildRuntimeObservedConfig(s.State, group)
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
	if addr == nil {
		return "", 0
	}
	host, portText, err := net.SplitHostPort(addr.String())
	if err != nil {
		return addr.String(), 0
	}
	port, _ := strconv.Atoi(portText)
	return host, uint16(port)
}

// ObserveRuntimeListeners builds a list of observed listeners from TCP and UDP listener maps.
func ObserveRuntimeListeners(tcp map[uint32][]net.Listener, udp map[uint32][]controlbind.UDPListener) []ObservedListener {
	listeners := make([]ObservedListener, 0, len(tcp)+len(udp))
	for tunnelID, tunnelListeners := range tcp {
		for _, listener := range tunnelListeners {
			bindIP, port := ListenerAddr(listener.Addr())
			listeners = append(listeners, ObservedListener{
				TunnelID: tunnelID,
				Protocol: "tcp",
				BindIP:   bindIP,
				Port:     port,
			})
		}
	}
	for tunnelID, tunnelListeners := range udp {
		for _, listener := range tunnelListeners {
			bindIP, port := ListenerAddr(listener.LocalAddr())
			listeners = append(listeners, ObservedListener{
				TunnelID: tunnelID,
				Protocol: "udp",
				BindIP:   bindIP,
				Port:     port,
			})
		}
	}
	return listeners
}

// CloseStartedTunnelListeners closes the given TCP and UDP listeners.
func CloseStartedTunnelListeners(tcpListeners []net.Listener, udpListeners []controlbind.UDPListener) {
	for _, listener := range tcpListeners {
		testhooks.Point(
			"control.listener.before_close",
			testhooks.F("protocol", "tcp"),
			testhooks.F("addr", listener.Addr().String()),
		)
		_ = listener.Close()
		testhooks.Point(
			"control.listener.after_close",
			testhooks.F("protocol", "tcp"),
			testhooks.F("addr", listener.Addr().String()),
		)
	}
	for _, listener := range udpListeners {
		testhooks.Point(
			"control.listener.before_close",
			testhooks.F("protocol", "udp"),
			testhooks.F("addr", listener.LocalAddr().String()),
		)
		_ = listener.Close()
		testhooks.Point(
			"control.listener.after_close",
			testhooks.F("protocol", "udp"),
			testhooks.F("addr", listener.LocalAddr().String()),
		)
	}
}
