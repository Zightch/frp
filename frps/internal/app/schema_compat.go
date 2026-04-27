package app

import (
	"context"

	"github.com/zightch/frp/frps/internal/dbschema"
	"github.com/zightch/frp/frps/internal/storage"
)

func ensureDatabaseSchema(ctx context.Context, store *storage.SQL, databaseType string) error {
	return dbschema.Ensure(ctx, store, databaseType)
}
