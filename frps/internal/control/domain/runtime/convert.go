package runtime

import (
	"strings"

	controlsession "github.com/zightch/frp/frps/internal/control/session"
	"github.com/zightch/frp/frps/pkg/protocol"
)

func DesiredRuntimeFromGroup(group GroupRuntime) controlsession.DesiredRuntimeSnapshot {
	snapshot := group.Snapshot
	return controlsession.DesiredRuntimeSnapshot{
		Version:       snapshot.Version,
		GeneratedAtMs: snapshot.GeneratedAtMs,
		EffectiveIP:   group.EffectiveIP,
		Tunnels:       DesiredTunnelsFromConfig(snapshot.Tunnels),
	}
}

func DesiredRuntimeFromSnapshot(effectiveIP string, snapshot ConfigSnapshot) controlsession.DesiredRuntimeSnapshot {
	return controlsession.DesiredRuntimeSnapshot{
		Version:       snapshot.Version,
		GeneratedAtMs: snapshot.GeneratedAtMs,
		EffectiveIP:   effectiveIP,
		Tunnels:       DesiredTunnelsFromConfig(snapshot.Tunnels),
	}
}

func DesiredTunnelsFromConfig(tunnels []protocol.TunnelEntry) []controlsession.DesiredTunnelRuntime {
	desired := make([]controlsession.DesiredTunnelRuntime, 0, len(tunnels))
	for _, tunnel := range tunnels {
		desired = append(desired, controlsession.DesiredTunnelRuntime{
			TunnelID:              tunnel.TunnelID,
			Protocol:              ProtocolName(tunnel.Protocol),
			Enabled:               tunnel.TunnelFlags&protocol.TunnelFlagEnabled != 0,
			RemoteStart:           tunnel.RemoteStart,
			RemoteEnd:             tunnel.RemoteEnd,
			LocalHost:             tunnel.LocalHost.String(),
			LocalStart:            tunnel.LocalStart,
			LocalEnd:              tunnel.LocalEnd,
			RatePolicyID:          tunnel.RatePolicy.PolicyID,
			RatePolicyMode:        tunnel.RatePolicy.Mode,
			RatePolicyDownlinkBPS: tunnel.RatePolicy.DownlinkBPS,
			RatePolicyUplinkBPS:   tunnel.RatePolicy.UplinkBPS,
		})
	}
	return desired
}

func ConfigSnapshotFromDesired(snapshot controlsession.DesiredRuntimeSnapshot) ConfigSnapshot {
	return ConfigSnapshot{
		Version:       snapshot.Version,
		GeneratedAtMs: snapshot.GeneratedAtMs,
		Tunnels:       ConfigTunnelsFromDesired(snapshot.Tunnels),
	}
}

func ConfigTunnelsFromDesired(tunnels []controlsession.DesiredTunnelRuntime) []protocol.TunnelEntry {
	configured := make([]protocol.TunnelEntry, 0, len(tunnels))
	for _, tunnel := range tunnels {
		host, _ := protocol.ParseHost(tunnel.LocalHost)
		flags := uint8(0)
		if tunnel.Enabled {
			flags |= protocol.TunnelFlagEnabled
		}
		if tunnel.RemoteStart != tunnel.RemoteEnd || tunnel.LocalStart != tunnel.LocalEnd {
			flags |= protocol.TunnelFlagRange
		}
		configured = append(configured, protocol.TunnelEntry{
			TunnelID:    tunnel.TunnelID,
			Protocol:    ProtocolValue(tunnel.Protocol),
			TunnelFlags: flags,
			RemoteStart: tunnel.RemoteStart,
			RemoteEnd:   tunnel.RemoteEnd,
			LocalHost:   host,
			LocalStart:  tunnel.LocalStart,
			LocalEnd:    tunnel.LocalEnd,
			RatePolicy: protocol.TunnelRatePolicy{
				PolicyID:    tunnel.RatePolicyID,
				Mode:        tunnel.RatePolicyMode,
				DownlinkBPS: tunnel.RatePolicyDownlinkBPS,
				UplinkBPS:   tunnel.RatePolicyUplinkBPS,
			},
		})
	}
	return configured
}

func ProtocolValue(value string) uint8 {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "tcp":
		return protocol.ProtocolTCP
	case "udp":
		return protocol.ProtocolUDP
	default:
		return 0
	}
}

func ProtocolName(value uint8) string {
	switch value {
	case protocol.ProtocolTCP:
		return "tcp"
	case protocol.ProtocolUDP:
		return "udp"
	default:
		return ""
	}
}

func DesiredSnapshotHasEnabledTunnels(snapshot controlsession.DesiredRuntimeSnapshot) bool {
	for _, tunnel := range snapshot.Tunnels {
		if tunnel.Enabled {
			return true
		}
	}
	return false
}
