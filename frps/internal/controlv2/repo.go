package controlv2

import (
	"context"

	"github.com/zightch/frp/frps/internal/controlv2/session"
)

type RuntimeRepo interface {
	LoadDesiredRuntimeByClientID(ctx context.Context, clientID [16]byte) (session.DesiredRuntimeSnapshot, error)
	LoadDesiredRuntimeByGroupID(ctx context.Context, groupID int64) (session.DesiredRuntimeSnapshot, error)
	ListDesiredRuntimes(ctx context.Context) ([]session.DesiredRuntimeSnapshot, error)
}
