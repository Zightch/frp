package controlv2

import (
	"context"

	"github.com/zightch/frp/frps/internal/controlv2/session"
)

type DesiredRuntimeRecord struct {
	GroupID  int64
	Snapshot session.DesiredRuntimeSnapshot
}

type RuntimeRepo interface {
	LoadDesiredRuntimeByClientID(ctx context.Context, clientID [16]byte) (DesiredRuntimeRecord, error)
	LoadDesiredRuntimeByGroupID(ctx context.Context, groupID int64) (DesiredRuntimeRecord, error)
	ListDesiredRuntimes(ctx context.Context) ([]DesiredRuntimeRecord, error)
}
