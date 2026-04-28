package runtime

import (
	"time"

	controldomainruntime "github.com/zightch/frp/frps/internal/control/domain/runtime"
	controlsession "github.com/zightch/frp/frps/internal/control/session"
	"github.com/zightch/frp/frps/pkg/protocol"
	"github.com/zightch/frp/frps/pkg/testsupport"
)

type GroupRuntime = controldomainruntime.GroupRuntime
type ConfigSnapshot = controldomainruntime.ConfigSnapshot

func DesiredRuntimeFromGroup(group GroupRuntime) controlsession.DesiredRuntimeSnapshot {
	return controldomainruntime.DesiredRuntimeFromGroup(group)
}

func DesiredRuntimeFromSnapshot(effectiveIP string, snapshot ConfigSnapshot) controlsession.DesiredRuntimeSnapshot {
	return controldomainruntime.DesiredRuntimeFromSnapshot(effectiveIP, snapshot)
}

func DesiredTunnelsFromConfig(tunnels []protocol.TunnelEntry) []controlsession.DesiredTunnelRuntime {
	return controldomainruntime.DesiredTunnelsFromConfig(tunnels)
}

func ConfigSnapshotFromDesired(snapshot controlsession.DesiredRuntimeSnapshot) ConfigSnapshot {
	return controldomainruntime.ConfigSnapshotFromDesired(snapshot)
}

func ConfigTunnelsFromDesired(tunnels []controlsession.DesiredTunnelRuntime) []protocol.TunnelEntry {
	return controldomainruntime.ConfigTunnelsFromDesired(tunnels)
}

func ProtocolValue(value string) uint8 {
	return controldomainruntime.ProtocolValue(value)
}

func ProtocolName(value uint8) string {
	return controldomainruntime.ProtocolName(value)
}

func DesiredSnapshotHasEnabledTunnels(snapshot controlsession.DesiredRuntimeSnapshot) bool {
	return controldomainruntime.DesiredSnapshotHasEnabledTunnels(snapshot)
}

// SessionReadTimeout calculates the session read timeout based on heartbeat interval.
func SessionReadTimeout(heartbeatInterval, minimum time.Duration) time.Duration {
	return controldomainruntime.SessionReadTimeout(heartbeatInterval, minimum)
}

// RuntimeSnapshotForGroup returns the appropriate snapshot for a group runtime.
// If the group is disabled, it returns a snapshot with no tunnels.
func RuntimeSnapshotForGroup(group GroupRuntime) ConfigSnapshot {
	return controldomainruntime.RuntimeSnapshotForGroup(group)
}

// EmptyConfigSnapshot returns a snapshot with no tunnels.
func EmptyConfigSnapshot(snapshot ConfigSnapshot) ConfigSnapshot {
	return controldomainruntime.EmptyConfigSnapshot(snapshot)
}

// PendingRecoveryModeForSnapshot returns the appropriate recovery mode for a pending config snapshot.
func PendingRecoveryModeForSnapshot(snapshot ConfigSnapshot) testsupport.RecoveryMode {
	return controldomainruntime.PendingRecoveryModeForSnapshot(snapshot)
}

// AppliedRecoveryModeForSnapshot returns the appropriate recovery mode for an applied config snapshot.
func AppliedRecoveryModeForSnapshot(snapshot ConfigSnapshot) testsupport.RecoveryMode {
	return controldomainruntime.AppliedRecoveryModeForSnapshot(snapshot)
}

// SamePushedConfigSnapshot checks if two config snapshots are the same for config push purposes.
func SamePushedConfigSnapshot(current, next ConfigSnapshot) bool {
	return controldomainruntime.SamePushedConfigSnapshot(current, next)
}

// SameRuntimeSnapshot checks if two config snapshots have the same runtime tunnels.
func SameRuntimeSnapshot(current, next ConfigSnapshot) bool {
	return controldomainruntime.SameRuntimeSnapshot(current, next)
}

// SameTunnelEntries checks if two tunnel entry slices are the same.
func SameTunnelEntries(left, right []protocol.TunnelEntry) bool {
	return controldomainruntime.SameTunnelEntries(left, right)
}

// SameTunnelEntry checks if two tunnel entries are the same.
func SameTunnelEntry(left, right protocol.TunnelEntry) bool {
	return controldomainruntime.SameTunnelEntry(left, right)
}

// SameHost checks if two host values are the same.
func SameHost(left, right protocol.Host) bool {
	return controldomainruntime.SameHost(left, right)
}
