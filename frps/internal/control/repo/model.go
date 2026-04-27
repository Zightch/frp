package repo

import (
	"context"
	"errors"

	"github.com/zightch/frp/frps/internal/proxygroups"
	"github.com/zightch/frp/frps/pkg/protocol"
)

var ErrGroupNotFound = errors.New("proxy group not found")

type Repository interface {
	LoadGroupRuntimeByClientID(ctx context.Context, clientID [16]byte) (GroupRuntime, error)
	LoadGroupRuntimeByID(ctx context.Context, groupID int64) (GroupRuntime, error)
	ListGroupRuntimes(ctx context.Context) ([]GroupRuntime, error)
}

type GroupRuntime struct {
	ID                       int64
	Name                     string
	Enabled                  bool
	EffectiveIP              string
	ControlTransportSecurity proxygroups.ControlTransportSecurity
	ClientSecretHash         [32]byte
	Snapshot                 ConfigSnapshot
}

type ConfigSnapshot struct {
	Version       uint64
	GeneratedAtMs uint64
	Tunnels       []protocol.TunnelEntry
}
