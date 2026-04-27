package control

import (
	"fmt"

	controlruntime "github.com/zightch/frp/frps/internal/control/runtime"
	controlsession "github.com/zightch/frp/frps/internal/control/session"
	"github.com/zightch/frp/frps/pkg/protocol"
)

func desiredRuntimeFromGroup(group GroupRuntime) controlsession.DesiredRuntimeSnapshot {
	return controlruntime.DesiredRuntimeFromGroup(group)
}

func desiredRuntimeFromObservedSnapshot(effectiveIP string, snapshot ConfigSnapshot) controlsession.DesiredRuntimeSnapshot {
	return controlruntime.DesiredRuntimeFromSnapshot(effectiveIP, snapshot)
}

func desiredTunnelsFromConfig(tunnels []protocol.TunnelEntry) []controlsession.DesiredTunnelRuntime {
	return controlruntime.DesiredTunnelsFromConfig(tunnels)
}

func configSnapshotFromDesired(snapshot controlsession.DesiredRuntimeSnapshot) ConfigSnapshot {
	return controlruntime.ConfigSnapshotFromDesired(snapshot)
}

func configTunnelsFromDesired(tunnels []controlsession.DesiredTunnelRuntime) []protocol.TunnelEntry {
	return controlruntime.ConfigTunnelsFromDesired(tunnels)
}

func protocolValue(value string) uint8 {
	return controlruntime.ProtocolValue(value)
}

func protocolName(value uint8) string {
	name := controlruntime.ProtocolName(value)
	if name != "" {
		return name
	}
	return fmt.Sprintf("protocol(%d)", value)
}

func desiredSnapshotHasEnabledTunnels(snapshot controlsession.DesiredRuntimeSnapshot) bool {
	return controlruntime.DesiredSnapshotHasEnabledTunnels(snapshot)
}
