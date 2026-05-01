package testdb

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/zightch/frp/frps/internal/storage"
	_ "github.com/zightch/frp/frps/internal/storage/drivers"
)

const MySQLDSNEnv = "FRPS_TEST_MYSQL_DSN"

var mysqlSuiteMu sync.Mutex

var FRPSTableNames = []string{
	"certificate_asset_usages",
	"certificate_asset_relations",
	"certificate_assets",
	"rate_policy_bindings",
	"rate_policies",
	"tunnels",
	"proxy_groups",
}

func MySQLDSN() string {
	return strings.TrimSpace(os.Getenv(MySQLDSNEnv))
}

func Enabled() bool {
	return MySQLDSN() != ""
}

func Open(t *testing.T, fileName string) (*sql.DB, string) {
	t.Helper()

	if dsn := MySQLDSN(); dsn != "" {
		mysqlSuiteMu.Lock()
		db, err := sql.Open("mysql", dsn)
		if err != nil {
			mysqlSuiteMu.Unlock()
			t.Fatalf("open mysql database: %v", err)
		}
		if err := db.PingContext(context.Background()); err != nil {
			_ = db.Close()
			mysqlSuiteMu.Unlock()
			t.Fatalf("ping mysql database: %v", err)
		}
		t.Cleanup(func() {
			_ = db.Close()
			mysqlSuiteMu.Unlock()
		})
		return db, "mysql"
	}

	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), fileName))
	if err != nil {
		t.Fatalf("open sqlite database: %v", err)
	}
	if err := db.PingContext(context.Background()); err != nil {
		_ = db.Close()
		t.Fatalf("ping sqlite database: %v", err)
	}
	t.Cleanup(func() {
		_ = db.Close()
	})
	return db, "sqlite"
}

func NewStore(t *testing.T, objectID, fileName string) (*storage.SQL, string) {
	t.Helper()

	db, dbType := Open(t, fileName)
	store, err := storage.NewSQLWithConn(objectID, db)
	if err != nil {
		t.Fatalf("create store: %v", err)
	}
	t.Cleanup(store.Close)
	return store, dbType
}

func ResetTables(t *testing.T, conn storage.Conn, dbType string, tableNames ...string) {
	t.Helper()

	for index := len(tableNames) - 1; index >= 0; index-- {
		tableName := strings.TrimSpace(tableNames[index])
		if tableName == "" {
			continue
		}
		statement := fmt.Sprintf("DROP TABLE IF EXISTS %s", quoteIdentifier(dbType, tableName))
		if _, err := conn.ExecContext(context.Background(), statement); err != nil {
			t.Fatalf("drop table %s: %v", tableName, err)
		}
	}
}

func quoteIdentifier(dbType, value string) string {
	escaped := strings.TrimSpace(value)
	if escaped == "" {
		return escaped
	}
	switch strings.ToLower(strings.TrimSpace(dbType)) {
	case "mysql":
		return "`" + strings.ReplaceAll(escaped, "`", "``") + "`"
	default:
		return `"` + strings.ReplaceAll(escaped, `"`, `""`) + `"`
	}
}
