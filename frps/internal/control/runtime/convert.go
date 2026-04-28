package runtime

import (
	"strings"
	"time"

	controlrepo "github.com/zightch/frp/frps/internal/control/repo"
	controlsession "github.com/zightch/frp/frps/internal/control/session"
	"github.com/zightch/frp/frps/pkg/protocol"
	"github.com/zightch/frp/frps/pkg/testsupport"
)

type GroupRuntime = controlrepo.GroupRuntime
type ConfigSnapshot = controlrepo.ConfigSnapshot

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
			TunnelID:    tunnel.TunnelID,
			Protocol:    ProtocolName(tunnel.Protocol),
			Enabled:     tunnel.TunnelFlags&protocol.TunnelFlagEnabled != 0,
			RemoteStart: tunnel.RemoteStart,
			RemoteEnd:   tunnel.RemoteEnd,
			LocalHost:   tunnel.LocalHost.String(),
			LocalStart:  tunnel.LocalStart,
			LocalEnd:    tunnel.LocalEnd,
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

// SessionReadTimeout calculates the session read timeout based on heartbeat interval.
func SessionReadTimeout(heartbeatInterval, minimum time.Duration) time.Duration {
	timeout := heartbeatInterval * 3
	if timeout < minimum {
		return minimum
	}
	return timeout
}

// RuntimeSnapshotForGroup returns the appropriate snapshot for a group runtime.
// If the group is disabled, it returns a snapshot with no tunnels.
func RuntimeSnapshotForGroup(group GroupRuntime) ConfigSnapshot {
	snapshot := group.Snapshot
	if group.Enabled {
		return snapshot
	}
	snapshot.Tunnels = nil
	return snapshot
}

// EmptyConfigSnapshot returns a snapshot with no tunnels.
func EmptyConfigSnapshot(snapshot ConfigSnapshot) ConfigSnapshot {
	snapshot.Tunnels = nil
	return snapshot
}

// PendingRecoveryModeForSnapshot returns the appropriate recovery mode for a pending config snapshot.
func PendingRecoveryModeForSnapshot(snapshot ConfigSnapshot) testsupport.RecoveryMode {
	if len(snapshot.Tunnels) == 0 {
		return testsupport.RecoveryModePendingEmptyConfig
	}
	return testsupport.RecoveryModePendingFullConfig
}

// AppliedRecoveryModeForSnapshot returns the appropriate recovery mode for an applied config snapshot.
func AppliedRecoveryModeForSnapshot(snapshot ConfigSnapshot) testsupport.RecoveryMode {
	if len(snapshot.Tunnels) == 0 {
		return testsupport.RecoveryModeEmptyConfig
	}
	return testsupport.RecoveryModeRunning
}

// SamePushedConfigSnapshot checks if two config snapshots are the same for config push purposes.
func SamePushedConfigSnapshot(current, next ConfigSnapshot) bool {
	return current.Version == next.Version &&
		current.GeneratedAtMs == next.GeneratedAtMs &&
		SameRuntimeSnapshot(current, next)
}

// SameRuntimeSnapshot checks if two config snapshots have the same runtime tunnels.
func SameRuntimeSnapshot(current, next ConfigSnapshot) bool {
	return SameTunnelEntries(current.Tunnels, next.Tunnels)
}

// SameTunnelEntries checks if two tunnel entry slices are the same.
func SameTunnelEntries(left, right []protocol.TunnelEntry) bool {
	if len(left) == 0 && len(right) == 0 {
		return true
	}
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if !SameTunnelEntry(left[index], right[index]) {
			return false
		}
	}
	return true
}

// SameTunnelEntry checks if two tunnel entries are the same.
func SameTunnelEntry(left, right protocol.TunnelEntry) bool {
	return left.TunnelID == right.TunnelID &&
		left.Protocol == right.Protocol &&
		left.TunnelFlags == right.TunnelFlags &&
		left.RemoteStart == right.RemoteStart &&
		left.RemoteEnd == right.RemoteEnd &&
		SameHost(left.LocalHost, right.LocalHost) &&
		left.LocalStart == right.LocalStart &&
		left.LocalEnd == right.LocalEnd &&
		left.Revision == right.Revision &&
		left.BackendTLSMode == right.BackendTLSMode &&
		left.BackendTLSLoadSystemCA == right.BackendTLSLoadSystemCA &&
		left.BackendTLSInsecureSkipVerify == right.BackendTLSInsecureSkipVerify &&
		left.BackendTLSServerName == right.BackendTLSServerName &&
		left.BackendTLSCAPEM == right.BackendTLSCAPEM &&
		left.BackendTLSClientCertPEM == right.BackendTLSClientCertPEM &&
		left.BackendTLSClientKeyPEM == right.BackendTLSClientKeyPEM
}

// SameHost checks if two host values are the same.
func SameHost(left, right protocol.Host) bool {
	if left.Type != right.Type || left.Name != right.Name {
		return false
	}
	switch left.Type {
	case protocol.HostTypeIPv4, protocol.HostTypeIPv6:
		return left.IP.Equal(right.IP)
	default:
		return true
	}
}
