package app

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/zightch/frp/frps/internal/config"
	"github.com/zightch/frp/frps/internal/storage"
	_ "github.com/zightch/frp/frps/internal/storage/drivers"
)

const appStoreObjectID = "frps"

func (a *App) initDatabase(ctx context.Context) error {
	db, err := openDatabase(ctx, a.config.Database)
	if err != nil {
		return err
	}

	store := storage.NewSQL(appStoreObjectID)
	if err := store.SetConn(db); err != nil {
		_ = db.Close()
		return fmt.Errorf("attach database store: %w", err)
	}

	if err := ensureDatabaseSchema(ctx, store, a.config.Database.Type); err != nil {
		store.Close()
		return fmt.Errorf("ensure database schema: %w", err)
	}

	a.store = store
	a.logger.Info(
		"database ready",
		"type", a.config.Database.Type,
		"schema_version", currentSchemaVersion,
	)
	return nil
}

func (a *App) closeDatabase() {
	if a.store == nil {
		return
	}

	a.store.Close()
	a.store = nil
	a.logger.Info("database closed", "type", a.config.Database.Type)
}

func openDatabase(ctx context.Context, cfg config.DatabaseConfig) (*sql.DB, error) {
	driverName, dataSourceName, err := resolveDatabaseTarget(cfg)
	if err != nil {
		return nil, err
	}

	db, err := sql.Open(driverName, dataSourceName)
	if err != nil {
		return nil, fmt.Errorf("open %s database: %w", driverName, err)
	}

	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("ping %s database: %w", driverName, err)
	}

	return db, nil
}

func resolveDatabaseTarget(cfg config.DatabaseConfig) (string, string, error) {
	switch strings.ToLower(strings.TrimSpace(cfg.Type)) {
	case "sqlite":
		return resolveSQLiteDataSource(cfg)
	case "mysql":
		dsn, err := normalizeMySQLDSN(cfg.DSN)
		if err != nil {
			return "", "", err
		}
		return "mysql", dsn, nil
	default:
		return "", "", fmt.Errorf("unsupported database type %q", cfg.Type)
	}
}

func resolveSQLiteDataSource(cfg config.DatabaseConfig) (string, string, error) {
	dsn := strings.TrimSpace(cfg.DSN)
	if dsn != "" {
		return "sqlite", dsn, nil
	}

	path := strings.TrimSpace(cfg.Path)
	if path == "" {
		return "", "", fmt.Errorf("sqlite path is required")
	}

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", "", fmt.Errorf("create sqlite data directory: %w", err)
	}

	return "sqlite", path, nil
}

func normalizeMySQLDSN(dsn string) (string, error) {
	dsn = strings.TrimSpace(dsn)
	if dsn == "" {
		return "", fmt.Errorf("mysql dsn is required")
	}

	if strings.Contains(dsn, "@tcp(") || strings.Contains(dsn, "@unix(") {
		return dsn, nil
	}

	atIndex := strings.LastIndex(dsn, "@")
	if atIndex == -1 {
		return dsn, nil
	}

	hostAndDatabase := dsn[atIndex+1:]
	slashIndex := strings.Index(hostAndDatabase, "/")
	if slashIndex <= 0 {
		return dsn, nil
	}

	address := hostAndDatabase[:slashIndex]
	if strings.ContainsAny(address, "()") {
		return dsn, nil
	}

	return dsn[:atIndex+1] + "tcp(" + address + ")" + hostAndDatabase[slashIndex:], nil
}
