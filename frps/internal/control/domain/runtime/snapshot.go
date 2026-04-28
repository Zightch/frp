package runtime

import (
	"time"

	"github.com/zightch/frp/frps/pkg/protocol"
	"github.com/zightch/frp/frps/pkg/testsupport"
)

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
	leftFlags := left.TunnelFlags &^ protocol.TunnelFlagRange
	rightFlags := right.TunnelFlags &^ protocol.TunnelFlagRange
	return left.TunnelID == right.TunnelID &&
		left.Protocol == right.Protocol &&
		leftFlags == rightFlags &&
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

// EnabledTunnels returns only the enabled tunnels from a snapshot.
func EnabledTunnels(snapshot ConfigSnapshot) []protocol.TunnelEntry {
	enabled := make([]protocol.TunnelEntry, 0, len(snapshot.Tunnels))
	for _, tunnel := range snapshot.Tunnels {
		if tunnel.TunnelFlags&protocol.TunnelFlagEnabled == 0 {
			continue
		}
		enabled = append(enabled, tunnel)
	}
	return enabled
}

// SelectNonListeningEnabledTunnels returns enabled tunnels that are not in the active tunnel IDs set.
func SelectNonListeningEnabledTunnels(tunnels []protocol.TunnelEntry, activeTunnelIDs map[uint32]struct{}) []protocol.TunnelEntry {
	selected := make([]protocol.TunnelEntry, 0, len(tunnels))
	for _, tunnel := range tunnels {
		if tunnel.TunnelFlags&protocol.TunnelFlagEnabled == 0 {
			continue
		}
		if _, active := activeTunnelIDs[tunnel.TunnelID]; active {
			continue
		}
		selected = append(selected, tunnel)
	}
	return selected
}
