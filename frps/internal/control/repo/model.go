package repo

import (
	"context"
	"errors"

	controldomainruntime "github.com/zightch/frp/frps/internal/control/domain/runtime"
)

var ErrGroupNotFound = errors.New("proxy group not found")

type Repository interface {
	LoadGroupRuntimeByClientID(ctx context.Context, clientID [16]byte) (GroupRuntime, error)
	LoadGroupRuntimeByID(ctx context.Context, groupID int64) (GroupRuntime, error)
	ListGroupRuntimes(ctx context.Context) ([]GroupRuntime, error)
}

type GroupRuntime = controldomainruntime.GroupRuntime
type ConfigSnapshot = controldomainruntime.ConfigSnapshot
