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

func TestResolveDatabaseTargetKeepsMySQLDSNAsIs(t *testing.T) {
	t.Parallel()

	driverName, dsn, err := resolveDatabaseTarget(config.DatabaseConfig{
		Type: "mysql",
		DSN:  " user:pass@tcp(127.0.0.1:3306)/frps?parseTime=true ",
	})
	if err != nil {
		t.Fatalf("resolve mysql target: %v", err)
	}

	if driverName != "mysql" {
		t.Fatalf("unexpected driver name: got %q want %q", driverName, "mysql")
	}

	expected := "user:pass@tcp(127.0.0.1:3306)/frps?parseTime=true"
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

	for _, tableName := range []string{
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

	if _, err := application.store.QueryOne(
		"select name from sqlite_master where type = 'table' and name = ?",
		"schema_migrations",
	); err == nil {
		t.Fatal("did not expect schema_migrations table to exist")
	}
}

func TestAppInitDatabaseRejectsInvalidSQLiteSchema(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "frps.sqlite")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open sqlite database: %v", err)
	}
	_, err = db.Exec(`
CREATE TABLE proxy_groups (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	name INTEGER NOT NULL UNIQUE,
	token_id TEXT NOT NULL UNIQUE,
	token_hash TEXT NOT NULL,
	enabled INTEGER NOT NULL DEFAULT 1,
	rate_limit INTEGER NOT NULL DEFAULT 0,
	client_access_mode TEXT NOT NULL DEFAULT 'disabled',
	tunnel_access_mode TEXT NOT NULL DEFAULT 'disabled',
	created_at TEXT NOT NULL,
	updated_at TEXT NOT NULL
)`)
	if closeErr := db.Close(); closeErr != nil {
		t.Fatalf("close sqlite database: %v", closeErr)
	}
	if err != nil {
		t.Fatalf("create invalid proxy_groups table: %v", err)
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
	if !strings.Contains(err.Error(), "development stage does not support schema compatibility or migration") {
		t.Fatalf("expected no-compatibility error, got %v", err)
	}
	if !strings.Contains(err.Error(), "table proxy_groups: column name type mismatch") {
		t.Fatalf("expected proxy_groups name type mismatch, got %v", err)
	}
}

func TestAppInitDatabaseIgnoresUnrelatedSQLiteTables(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "frps.sqlite")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open sqlite database: %v", err)
	}

	if _, err := db.Exec(`CREATE TABLE admins (id INTEGER PRIMARY KEY AUTOINCREMENT, name TEXT NOT NULL)`); err != nil {
		_ = db.Close()
		t.Fatalf("create unrelated admins table: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close sqlite database: %v", err)
	}

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	application := New(config.Config{
		Database: config.DatabaseConfig{
			Type: "sqlite",
			Path: dbPath,
		},
	}, logger, "test")

	if err := application.initDatabase(context.Background()); err != nil {
		t.Fatalf("expected unrelated table to be ignored: %v", err)
	}
	defer application.closeDatabase()
}
