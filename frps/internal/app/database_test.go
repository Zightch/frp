package app

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"testing"

	"github.com/zightch/frp/frps/internal/config"
)

func TestNormalizeMySQLDSNAcceptsShortcutAddress(t *testing.T) {
	t.Parallel()

	dsn, err := normalizeMySQLDSN("frps:123456@staticplant.top:3306/frps")
	if err != nil {
		t.Fatalf("normalize mysql dsn: %v", err)
	}

	expected := "frps:123456@tcp(staticplant.top:3306)/frps"
	if dsn != expected {
		t.Fatalf("unexpected mysql dsn: got %q want %q", dsn, expected)
	}
}

func TestOpenDatabaseCreatesSQLiteDirectory(t *testing.T) {
	t.Parallel()

	db, err := openDatabase(context.Background(), config.DatabaseConfig{
		Type: "sqlite",
		Path: filepath.Join(t.TempDir(), "data", "frps.sqlite"),
	})
	if err != nil {
		t.Fatalf("open sqlite database: %v", err)
	}
	defer db.Close()
}

func TestAppInitDatabaseInjectsStore(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	application := New(config.Config{
		Database: config.DatabaseConfig{
			Type: "sqlite",
			Path: filepath.Join(t.TempDir(), "frps.sqlite"),
		},
	}, logger, "test")

	if err := application.initDatabase(context.Background()); err != nil {
		t.Fatalf("init database: %v", err)
	}
	defer application.closeDatabase()

	if application.store == nil {
		t.Fatal("expected app store to be initialized")
	}

	row, err := application.store.QueryOne("select 1 as value")
	if err != nil {
		t.Fatalf("query through injected store: %v", err)
	}

	if row["value"] != int64(1) {
		t.Fatalf("unexpected query result: %#v", row["value"])
	}
}
