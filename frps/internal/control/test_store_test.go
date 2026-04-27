package control

import (
	"context"
	"testing"

	"github.com/zightch/frp/frps/internal/dbschema"
	"github.com/zightch/frp/frps/internal/storage"
	"github.com/zightch/frp/frps/internal/testdb"
)

func newControlTestStore(t *testing.T) *storage.SQL {
	t.Helper()

	store, dbType := testdb.NewStore(t, t.Name(), "control.sqlite")
	testdb.ResetTables(t, store, dbType, testdb.FRPSTableNames...)
	t.Cleanup(func() {
		testdb.ResetTables(t, store, dbType, testdb.FRPSTableNames...)
	})
	if err := dbschema.Ensure(context.Background(), store, dbType); err != nil {
		t.Fatalf("bootstrap control test schema: %v", err)
	}
	return store
}
