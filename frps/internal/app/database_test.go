package app

import (
	"context"
	"database/sql"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zightch/frp/frps/internal/config"
	_ "github.com/zightch/frp/frps/internal/storage/drivers"
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

	versionRow, err := application.store.QueryOne("select max(version) as version from schema_migrations")
	if err != nil {
		t.Fatalf("query schema version: %v", err)
	}
	if versionRow["version"] != int64(currentSchemaVersion) {
		t.Fatalf("unexpected schema version: got %#v want %d", versionRow["version"], currentSchemaVersion)
	}

	for _, tableName := range []string{
		"admins",
		"proxy_groups",
		"group_client_ip_rules",
		"group_tunnel_ip_rules",
		"tunnels",
	} {
		if _, err := application.store.QueryOne(
			"select name from sqlite_master where type = 'table' and name = ?",
			tableName,
		); err != nil {
			t.Fatalf("expected table %s to exist: %v", tableName, err)
		}
	}
}

func TestAppInitDatabaseRejectsInvalidSQLiteSchema(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "frps.sqlite")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open sqlite database: %v", err)
	}
	_, err = db.Exec(`
CREATE TABLE admins (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	username INTEGER NOT NULL UNIQUE,
	password_hash TEXT NOT NULL,
	enabled INTEGER NOT NULL DEFAULT 1,
	created_at TEXT NOT NULL,
	updated_at TEXT NOT NULL
)`)
	if closeErr := db.Close(); closeErr != nil {
		t.Fatalf("close sqlite database: %v", closeErr)
	}
	if err != nil {
		t.Fatalf("create invalid admins table: %v", err)
	}

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	application := New(config.Config{
		Database: config.DatabaseConfig{
			Type: "sqlite",
			Path: dbPath,
		},
	}, logger, "test")

	err = application.initDatabase(context.Background())
	if err == nil {
		defer application.closeDatabase()
		t.Fatal("expected invalid schema to fail")
	}
	if !strings.Contains(err.Error(), "column username type mismatch") {
		t.Fatalf("expected username type mismatch, got %v", err)
	}
}
