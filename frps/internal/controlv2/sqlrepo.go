package controlv2

import (
	"context"
	"fmt"

	"github.com/zightch/frp/frps/internal/control"
	"github.com/zightch/frp/frps/internal/controlv2/session"
	"github.com/zightch/frp/frps/internal/storage"
	"github.com/zightch/frp/frps/pkg/protocol"
)

type RepositoryAdapter struct {
	repo control.Repository
}

func NewRepositoryAdapter(repo control.Repository) *RepositoryAdapter {
	return &RepositoryAdapter{repo: repo}
}

func NewSQLRepository(store *storage.SQL) *RepositoryAdapter {
	return NewRepositoryAdapter(control.NewRepository(store))
}

func (r *RepositoryAdapter) LoadDesiredRuntimeByClientID(ctx context.Context, clientID [16]byte) (DesiredRuntimeRecord, error) {
	if r == nil || r.repo == nil {
		return DesiredRuntimeRecord{}, fmt.Errorf("controlv2 runtime repo is nil")
	}

	group, err := r.repo.LoadGroupRuntimeByClientID(ctx, clientID)
	if err != nil {
		return DesiredRuntimeRecord{}, err
	}
	return adaptGroupRuntime(group), nil
}

func (r *RepositoryAdapter) LoadDesiredRuntimeByGroupID(ctx context.Context, groupID int64) (DesiredRuntimeRecord, error) {
	if r == nil || r.repo == nil {
		return DesiredRuntimeRecord{}, fmt.Errorf("controlv2 runtime repo is nil")
	}

	group, err := r.repo.LoadGroupRuntimeByID(ctx, groupID)
	if err != nil {
		return DesiredRuntimeRecord{}, err
	}
	return adaptGroupRuntime(group), nil
}

func (r *RepositoryAdapter) ListDesiredRuntimes(ctx context.Context) ([]DesiredRuntimeRecord, error) {
	if r == nil || r.repo == nil {
		return nil, fmt.Errorf("controlv2 runtime repo is nil")
	}

	groups, err := r.repo.ListGroupRuntimes(ctx)
	if err != nil {
		return nil, err
	}

	records := make([]DesiredRuntimeRecord, 0, len(groups))
	for _, group := range groups {
		records = append(records, adaptGroupRuntime(group))
	}
	return records, nil
}

func adaptGroupRuntime(group control.GroupRuntime) DesiredRuntimeRecord {
	return DesiredRuntimeRecord{
		GroupID: group.ID,
		Snapshot: session.DesiredRuntimeSnapshot{
			Version:       group.Snapshot.Version,
			GeneratedAtMs: group.Snapshot.GeneratedAtMs,
			EffectiveIP:   group.EffectiveIP,
			Tunnels:       adaptDesiredTunnels(group.Snapshot.Tunnels),
		},
	}
}

func adaptDesiredTunnels(tunnels []protocol.TunnelEntry) []session.DesiredTunnelRuntime {
	desired := make([]session.DesiredTunnelRuntime, 0, len(tunnels))
	for _, tunnel := range tunnels {
		desired = append(desired, session.DesiredTunnelRuntime{
			TunnelID:    tunnel.TunnelID,
			Protocol:    desiredTunnelProtocol(tunnel.Protocol),
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

func desiredTunnelProtocol(value uint8) string {
	switch value {
	case protocol.ProtocolTCP:
		return "tcp"
	case protocol.ProtocolUDP:
		return "udp"
	default:
		return ""
	}
}
