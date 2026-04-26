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
		"tunnels",
		"certificate_assets",
		"certificate_asset_relations",
		"certificate_asset_usages",
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

	for _, indexName := range []string{
		"idx_certificate_assets_crt_hash",
		"uk_certificate_assets_name",
		"idx_certificate_asset_relations_parent_asset_id",
		"uk_certificate_asset_relations_child_asset_id",
		"idx_certificate_asset_usages_asset_id",
		"uk_certificate_asset_usages_usage_target",
	} {
		if _, err := application.store.QueryOne(
			"select name from sqlite_master where type = 'index' and name = ?",
			indexName,
		); err != nil {
			t.Fatalf("expected index %s to exist: %v", indexName, err)
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
CREATE TABLE proxy_groups (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	name INTEGER NOT NULL UNIQUE,
	client_id TEXT NOT NULL UNIQUE,
	client_secret_hash TEXT NOT NULL,
	effective_ip TEXT NOT NULL,
	enabled INTEGER NOT NULL DEFAULT 1,
	control_transport_security TEXT NOT NULL DEFAULT 'plain',
	rate_limit INTEGER NOT NULL DEFAULT 0,
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

func TestAppInitDatabaseBackfillsMissingCertificateAssetIndexes(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "frps.sqlite")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open sqlite database: %v", err)
	}
	_, err = db.Exec(`
CREATE TABLE proxy_groups (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	name TEXT NOT NULL UNIQUE,
	client_id TEXT NOT NULL UNIQUE,
	client_secret_hash TEXT NOT NULL,
	effective_ip TEXT NOT NULL,
	enabled INTEGER NOT NULL DEFAULT 1,
	control_transport_security TEXT NOT NULL DEFAULT 'plain',
	rate_limit INTEGER NOT NULL DEFAULT 0,
	created_at TEXT NOT NULL,
	updated_at TEXT NOT NULL
);
CREATE TABLE tunnels (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	group_id INTEGER NOT NULL,
	name TEXT NOT NULL,
	protocol TEXT NOT NULL,
	remote_type TEXT NOT NULL,
	remote_start INTEGER NOT NULL,
	remote_end INTEGER NOT NULL,
	local_host TEXT NOT NULL,
	local_start INTEGER NOT NULL,
	local_end INTEGER NOT NULL,
	listen_tls_mode TEXT NOT NULL DEFAULT 'off',
	listen_tls_load_system_ca INTEGER NOT NULL DEFAULT 0,
	backend_tls_mode TEXT NOT NULL DEFAULT 'off',
	backend_tls_server_name TEXT NOT NULL DEFAULT '',
	backend_tls_load_system_ca INTEGER NOT NULL DEFAULT 0,
	backend_tls_insecure_skip_verify INTEGER NOT NULL DEFAULT 0,
	enabled INTEGER NOT NULL DEFAULT 1,
	created_at TEXT NOT NULL,
	updated_at TEXT NOT NULL,
	UNIQUE(group_id, name)
);
CREATE TABLE certificate_assets (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	name TEXT NOT NULL,
	remark TEXT NOT NULL,
	source TEXT NOT NULL,
	asset_type TEXT NOT NULL,
	format_type TEXT NOT NULL,
	crt TEXT NOT NULL,
	crt_hash TEXT NOT NULL,
	key TEXT NOT NULL,
	created_at TEXT NOT NULL,
	updated_at TEXT NOT NULL
);
CREATE UNIQUE INDEX uk_certificate_assets_name ON certificate_assets (name);
CREATE TABLE certificate_asset_relations (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	child_asset_id INTEGER NOT NULL,
	parent_asset_id INTEGER NOT NULL,
	relation_type TEXT NOT NULL,
	created_at TEXT NOT NULL,
	updated_at TEXT NOT NULL
);
CREATE TABLE certificate_asset_usages (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	target_type TEXT NOT NULL DEFAULT 'global',
	usage_type TEXT NOT NULL,
	target_id INTEGER NOT NULL DEFAULT 0,
	asset_id INTEGER NOT NULL,
	enabled INTEGER NOT NULL DEFAULT 1,
	created_at TEXT NOT NULL,
	updated_at TEXT NOT NULL
);
`)
	if closeErr := db.Close(); closeErr != nil {
		t.Fatalf("close sqlite database: %v", closeErr)
	}
	if err != nil {
		t.Fatalf("create partial certificate_assets schema: %v", err)
	}

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	application := New(config.Config{
		Database: config.DatabaseConfig{
			Type: "sqlite",
			Path: dbPath,
		},
	}, logger, "test")

	if err := application.initDatabase(context.Background()); err != nil {
		t.Fatalf("expected missing certificate asset indexes to be backfilled: %v", err)
	}
	defer application.closeDatabase()

	for _, indexName := range []string{
		"idx_certificate_assets_crt_hash",
		"uk_certificate_assets_name",
		"idx_certificate_asset_relations_parent_asset_id",
		"uk_certificate_asset_relations_child_asset_id",
		"idx_certificate_asset_usages_asset_id",
		"uk_certificate_asset_usages_usage_target",
	} {
		if _, err := application.store.QueryOne(
			"select name from sqlite_master where type = 'index' and name = ?",
			indexName,
		); err != nil {
			t.Fatalf("expected certificate asset index %s to be backfilled: %v", indexName, err)
		}
	}
}
