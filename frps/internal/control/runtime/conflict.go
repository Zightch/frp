package runtime

import (
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"syscall"

	"github.com/zightch/frp/frps/internal/ports"
	"github.com/zightch/frp/frps/internal/system"
	"github.com/zightch/frp/frps/pkg/protocol"
)

// RuntimeClaimOwner identifies the owner of a runtime port claim.
type RuntimeClaimOwner struct {
	GroupID     int64
	GroupName   string
	TunnelID    uint32
	EffectiveIP string
}

// BuildRuntimeClaims builds port claims for the given tunnels and bind IP.
// It returns the claims, a map of owner IDs to their owner info, and the order of target tunnel IDs.
func BuildRuntimeClaims(groupID int64, groupName string, tunnels []protocol.TunnelEntry, bindIP string) ([]ports.Claim, map[int64]RuntimeClaimOwner, []uint32) {
	claims := make([]ports.Claim, 0, len(tunnels))
	owners := make(map[int64]RuntimeClaimOwner)
	targetOrder := make([]uint32, 0, len(tunnels))
	for _, tunnel := range tunnels {
		if tunnel.TunnelFlags&protocol.TunnelFlagEnabled == 0 {
			continue
		}
		ownerID := int64(tunnel.TunnelID)
		claims = append(claims, ports.Claim{
			OwnerID:     ownerID,
			Protocol:    ProtocolName(tunnel.Protocol),
			EffectiveIP: bindIP,
			PortStart:   int64(tunnel.RemoteStart),
			PortEnd:     int64(tunnel.RemoteEnd),
		})
		owners[ownerID] = RuntimeClaimOwner{
			GroupID:     groupID,
			GroupName:   groupName,
			TunnelID:    tunnel.TunnelID,
			EffectiveIP: bindIP,
		}
		targetOrder = append(targetOrder, tunnel.TunnelID)
	}
	return claims, owners, targetOrder
}

// BuildRuntimeConflictReason builds a human-readable reason string for a port conflict.
func BuildRuntimeConflictReason(target, other RuntimeClaimOwner, conflict ports.Conflict) string {
	return fmt.Sprintf(
		`与分组"%s"的 tunnel_id=%d 在 %s (%s) %s 上冲突，无法启动监听`,
		other.GroupName,
		other.TunnelID,
		strings.ToUpper(conflict.Protocol),
		FormatConflictEffectiveIPs(conflict.OwnerEffectiveIP, conflict.OtherEffectiveIP),
		FormatConflictPortRange(conflict.ConflictStart, conflict.ConflictEnd),
	)
}

// BuildTunnelListenerStartReason builds a human-readable reason string for a tunnel listener start failure.
func BuildTunnelListenerStartReason(protocolValue uint8, effectiveIP string, remotePort uint16, cause error) string {
	addr := net.JoinHostPort(effectiveIP, strconv.Itoa(int(remotePort)))
	if IsListenPortConflictError(cause) {
		return fmt.Sprintf("%s 监听 %s 端口冲突，无法启动", strings.ToUpper(ProtocolName(protocolValue)), addr)
	}
	return fmt.Sprintf("%s 监听 %s 启动失败: %v", strings.ToUpper(ProtocolName(protocolValue)), addr, cause)
}

// IsListenPortConflictError checks if the error indicates a port conflict.
func IsListenPortConflictError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, syscall.EADDRINUSE) {
		return true
	}
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "address already in use") ||
		strings.Contains(message, "only one usage of each socket address") ||
		strings.Contains(message, "10048")
}

// FormatConflictEffectiveIPs formats the effective IPs for a conflict message.
func FormatConflictEffectiveIPs(ownerIP, otherIP string) string {
	if strings.TrimSpace(ownerIP) == "" {
		return strings.TrimSpace(otherIP)
	}
	if strings.TrimSpace(otherIP) == "" || ownerIP == otherIP {
		return ownerIP
	}
	return ownerIP + " <-> " + otherIP
}

// FormatConflictPortRange formats a port range for display.
func FormatConflictPortRange(start, end int64) string {
	if start == end {
		return strconv.FormatInt(start, 10)
	}
	return fmt.Sprintf("%d-%d", start, end)
}

// DetectConfiguredConflictTunnelIDs detects tunnel IDs that have configured port conflicts.
func DetectConfiguredConflictTunnelIDs(tunnelsByGroup map[int64][]protocol.TunnelEntry, effectiveIPsByGroup map[int64]string) map[int64]struct{} {
	claims := make([]ports.Claim, 0)
	for groupID, tunnels := range tunnelsByGroup {
		effectiveIP := effectiveIPsByGroup[groupID]
		for _, tunnel := range tunnels {
			if tunnel.TunnelFlags&protocol.TunnelFlagEnabled == 0 {
				continue
			}
			claims = append(claims, ports.Claim{
				OwnerID:     int64(tunnel.TunnelID),
				Protocol:    ProtocolName(tunnel.Protocol),
				EffectiveIP: effectiveIP,
				PortStart:   int64(tunnel.RemoteStart),
				PortEnd:     int64(tunnel.RemoteEnd),
			})
		}
	}

	conflicts := ports.DetectConflicts(claims)
	conflicted := make(map[int64]struct{}, len(conflicts))
	for tunnelID := range conflicts {
		conflicted[tunnelID] = struct{}{}
	}
	return conflicted
}

// RuntimeGroupData represents the data needed for runtime group conflict detection.
type RuntimeGroupData struct {
	GroupID     int64
	GroupName   string
	EffectiveIP string
	Tunnels     []protocol.TunnelEntry
}

// DetectRuntimePortConflictIssuesWithData detects runtime port conflict issues for the given tunnels.
// It takes explicit runtime group data instead of a RuntimeOperator interface.
// It returns a map of tunnel IDs to their conflict reason strings.
func DetectRuntimePortConflictIssuesWithData(group RuntimeGroupData, bindIP string, tunnels []protocol.TunnelEntry, activeGroups []RuntimeGroupData) map[uint32]string {
	claims, owners, targetOrder := BuildRuntimeClaims(group.GroupID, group.GroupName, tunnels, bindIP)
	if len(targetOrder) == 0 {
		return nil
	}

	for _, active := range activeGroups {
		otherBindIP, ok := NormalizeRuntimeListenIP(active.EffectiveIP)
		if !ok {
			continue
		}
		otherClaims, otherOwners, _ := BuildRuntimeClaims(active.GroupID, active.GroupName, active.Tunnels, otherBindIP)
		claims = append(claims, otherClaims...)
		for ownerID, owner := range otherOwners {
			owners[ownerID] = owner
		}
	}

	conflicts := ports.DetectConflicts(claims)
	if len(conflicts) == 0 {
		return nil
	}

	issues := make(map[uint32]string)
	for _, tunnelID := range targetOrder {
		conflict, ok := conflicts[int64(tunnelID)]
		if !ok {
			continue
		}
		target, ok := owners[int64(tunnelID)]
		if !ok {
			continue
		}
		other, ok := owners[conflict.OtherOwnerID]
		if !ok {
			continue
		}
		reason := BuildRuntimeConflictReason(target, other, conflict)
		issues[tunnelID] = reason
	}
	return issues
}

// DetectRuntimePortConflictIssues detects runtime port conflict issues for the given tunnels.
// It returns a map of tunnel IDs to their conflict reason strings.
func DetectRuntimePortConflictIssues(op RuntimeOperator, group GroupRuntime, bindIP string, tunnels []protocol.TunnelEntry) map[uint32]string {
	claims, owners, targetOrder := BuildRuntimeClaims(group.ID, group.Name, tunnels, bindIP)
	if len(targetOrder) == 0 {
		return nil
	}

	for _, active := range op.ActiveRuntimeGroups(nil) {
		otherBindIP, ok := NormalizeRuntimeListenIP(active.Group.EffectiveIP)
		if !ok {
			continue
		}
		otherClaims, otherOwners, _ := BuildRuntimeClaims(active.Group.ID, active.Group.Name, active.Snapshot.Tunnels, otherBindIP)
		claims = append(claims, otherClaims...)
		for ownerID, owner := range otherOwners {
			owners[ownerID] = owner
		}
	}

	conflicts := ports.DetectConflicts(claims)
	if len(conflicts) == 0 {
		return nil
	}

	issues := make(map[uint32]string)
	for _, tunnelID := range targetOrder {
		conflict, ok := conflicts[int64(tunnelID)]
		if !ok {
			continue
		}
		target, ok := owners[int64(tunnelID)]
		if !ok {
			continue
		}
		other, ok := owners[conflict.OtherOwnerID]
		if !ok {
			continue
		}
		reason := BuildRuntimeConflictReason(target, other, conflict)
		issues[tunnelID] = reason
	}
	return issues
}

// NormalizeRuntimeListenIP normalizes a raw listen IP string and returns the normalized form.
func NormalizeRuntimeListenIP(raw string) (string, bool) {
	normalized, err := system.NormalizeListenIP(raw)
	if err != nil {
		return "", false
	}
	return normalized, true
}
