package storage

import (
	"context"
	"database/sql"
	"fmt"
	"hash/crc32"
	"os"
	"strings"
	"testing"

	_ "github.com/zightch/frp/frps/internal/storage/drivers"
)

func TestNewSQLReturnsSingletonByObjectID(t *testing.T) {
	t.Parallel()

	first := NewSQL("group-a")
	second := NewSQL("group-a")
	third := NewSQL("group-b")

	if first != second {
		t.Fatalf("expected same instance for identical object id")
	}

	if first == third {
		t.Fatalf("expected different instances for different object ids")
	}
}

func TestSetConnRejectsNilDatabase(t *testing.T) {
	t.Parallel()

	store := NewSQL(t.Name())
	store.Close()

	if err := store.SetConn(nil); err == nil {
		t.Fatal("expected nil database error")
	}
}

func TestSetConnRejectsReplacingExistingDatabase(t *testing.T) {
	t.Parallel()

	store := NewSQL(t.Name())
	store.Close()

	first := openSQLiteDB(t)
	second := openSQLiteDB(t)
	defer second.Close()

	if err := store.SetConn(first); err != nil {
		t.Fatalf("set first database: %v", err)
	}
	t.Cleanup(store.Close)

	if err := store.SetConn(second); err == nil {
		t.Fatal("expected replacing existing database to fail")
	}
}

func TestExecuteRequiresOpenDatabase(t *testing.T) {
	t.Parallel()

	store := NewSQL(t.Name())
	store.Close()

	if _, err := store.Execute("select 1"); err == nil {
		t.Fatalf("expected error when executing against closed database")
	}
}

func TestExecuteAndQueryReturnStructuredResult(t *testing.T) {
	t.Parallel()

	store, dbType := openTestStore(t)
	tableName := testTableName(t, "test_users")
	dropTableIfExists(t, store, dbType, tableName)

	if _, err := store.Exec(fmt.Sprintf(`
		create table %s (
			id integer primary key,
			name text not null
		)
	`, quoteIdentifier(dbType, tableName))); err != nil {
		t.Fatalf("create table: %v", err)
	}
	t.Cleanup(func() {
		dropTableIfExists(t, store, dbType, tableName)
	})

	insertResult, err := store.Execute(
		fmt.Sprintf(`insert into %s (id, name) values (?, ?), (?, ?)`, quoteIdentifier(dbType, tableName)),
		1, "alice", 2, "bob",
	)
	if err != nil {
		t.Fatalf("insert rows: %v", err)
	}
	if insertResult.RowsAffected != 2 {
		t.Fatalf("expected 2 affected rows, got %d", insertResult.RowsAffected)
	}

	queryResult, err := store.Execute(fmt.Sprintf(`select id, name from %s order by id asc`, quoteIdentifier(dbType, tableName)))
	if err != nil {
		t.Fatalf("query rows: %v", err)
	}
	if len(queryResult.Columns) != 2 {
		t.Fatalf("expected 2 columns, got %d", len(queryResult.Columns))
	}
	if len(queryResult.Rows) != 2 {
		t.Fatalf("expected 2 rows, got %d", len(queryResult.Rows))
	}
	if queryResult.Rows[0]["name"] != "alice" {
		t.Fatalf("unexpected first row name: %#v", queryResult.Rows[0]["name"])
	}
	if queryResult.Rows[1]["name"] != "bob" {
		t.Fatalf("unexpected second row name: %#v", queryResult.Rows[1]["name"])
	}

	firstRow, err := store.QueryOne(fmt.Sprintf(`select id, name from %s where name = ?`, quoteIdentifier(dbType, tableName)), "bob")
	if err != nil {
		t.Fatalf("query one row: %v", err)
	}
	if firstRow["name"] != "bob" {
		t.Fatalf("unexpected query one result: %#v", firstRow["name"])
	}
}

func TestTransactionCommitAndRollback(t *testing.T) {
	t.Parallel()

	store, dbType := openTestStore(t)
	tableName := testTableName(t, "tx_items")
	dropTableIfExists(t, store, dbType, tableName)

	if _, err := store.Exec(fmt.Sprintf(`
		create table %s (
			id integer primary key,
			name text not null
		)
	`, quoteIdentifier(dbType, tableName))); err != nil {
		t.Fatalf("create table: %v", err)
	}
	t.Cleanup(func() {
		dropTableIfExists(t, store, dbType, tableName)
	})

	tx, err := store.Begin()
	if err != nil {
		t.Fatalf("begin transaction: %v", err)
	}
	if _, err := tx.Exec(fmt.Sprintf(`insert into %s (id, name) values (?, ?)`, quoteIdentifier(dbType, tableName)), 1, "commit-me"); err != nil {
		t.Fatalf("insert inside transaction: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit transaction: %v", err)
	}

	tx, err = store.BeginTx(context.Background(), &sql.TxOptions{})
	if err != nil {
		t.Fatalf("begin transaction for rollback: %v", err)
	}
	if _, err := tx.Exec(fmt.Sprintf(`insert into %s (id, name) values (?, ?)`, quoteIdentifier(dbType, tableName)), 2, "rollback-me"); err != nil {
		t.Fatalf("insert rollback transaction: %v", err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatalf("rollback transaction: %v", err)
	}

	countRow, err := store.QueryOne(fmt.Sprintf(`select count(*) as total from %s`, quoteIdentifier(dbType, tableName)))
	if err != nil {
		t.Fatalf("query row count: %v", err)
	}
	if !valueEqualsInt64(countRow["total"], 1) {
		t.Fatalf("expected committed row count to be 1, got %#v", countRow["total"])
	}
}

func TestWithTxRollsBackOnError(t *testing.T) {
	t.Parallel()

	store, dbType := openTestStore(t)
	tableName := testTableName(t, "tx_callbacks")
	dropTableIfExists(t, store, dbType, tableName)

	if _, err := store.Exec(fmt.Sprintf(`
		create table %s (
			id integer primary key,
			name text not null
		)
	`, quoteIdentifier(dbType, tableName))); err != nil {
		t.Fatalf("create table: %v", err)
	}
	t.Cleanup(func() {
		dropTableIfExists(t, store, dbType, tableName)
	})

	expectedErr := store.WithTx(func(tx *Tx) error {
		if _, err := tx.Exec(fmt.Sprintf(`insert into %s (id, name) values (?, ?)`, quoteIdentifier(dbType, tableName)), 1, "rolled-back"); err != nil {
			return err
		}
		return sql.ErrTxDone
	})
	if expectedErr == nil {
		t.Fatal("expected transaction callback error")
	}

	countRow, err := store.QueryOne(fmt.Sprintf(`select count(*) as total from %s`, quoteIdentifier(dbType, tableName)))
	if err != nil {
		t.Fatalf("query callback row count: %v", err)
	}
	if !valueEqualsInt64(countRow["total"], 0) {
		t.Fatalf("expected rollback to keep row count at 0, got %#v", countRow["total"])
	}
}

func openTestStore(t *testing.T) (*SQL, string) {
	t.Helper()

	store := NewSQL(t.Name())
	store.Close()

	db, dbType := openTestDB(t)
	if err := store.SetConn(db); err != nil {
		t.Fatalf("set database: %v", err)
	}

	t.Cleanup(store.Close)

	return store, dbType
}

func openSQLiteDB(t *testing.T) *sql.DB {
	t.Helper()

	db, _ := openTestDB(t)
	return db
}

func openTestDB(t *testing.T) (*sql.DB, string) {
	t.Helper()

	if dsn := strings.TrimSpace(os.Getenv("FRPS_TEST_MYSQL_DSN")); dsn != "" {
		db, err := sql.Open("mysql", dsn)
		if err != nil {
			t.Fatalf("open mysql database: %v", err)
		}
		if err := db.PingContext(context.Background()); err != nil {
			_ = db.Close()
			t.Fatalf("ping mysql database: %v", err)
		}
		t.Cleanup(func() {
			_ = db.Close()
		})
		return db, "mysql"
	}

	db, err := sql.Open("sqlite", fmt.Sprintf("%s/frps.sqlite", t.TempDir()))
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

func testTableName(t *testing.T, base string) string {
	t.Helper()

	sanitized := strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z':
			return r
		case r >= 'A' && r <= 'Z':
			return r + ('a' - 'A')
		case r >= '0' && r <= '9':
			return r
		default:
			return '_'
		}
	}, base)
	return fmt.Sprintf("%s_%08x", sanitized, crc32.ChecksumIEEE([]byte(t.Name())))
}

func dropTableIfExists(t *testing.T, store *SQL, dbType, tableName string) {
	t.Helper()

	if _, err := store.Exec(fmt.Sprintf("drop table if exists %s", quoteIdentifier(dbType, tableName))); err != nil {
		t.Fatalf("drop table %s: %v", tableName, err)
	}
}

func quoteIdentifier(dbType, value string) string {
	switch strings.ToLower(strings.TrimSpace(dbType)) {
	case "mysql":
		return "`" + strings.ReplaceAll(value, "`", "``") + "`"
	default:
		return `"` + strings.ReplaceAll(value, `"`, `""`) + `"`
	}
}

func valueEqualsInt64(value any, expected int64) bool {
	switch typed := value.(type) {
	case int64:
		return typed == expected
	case int32:
		return int64(typed) == expected
	case int:
		return int64(typed) == expected
	case uint64:
		return typed == uint64(expected)
	case uint32:
		return uint64(typed) == uint64(expected)
	case []byte:
		return string(typed) == fmt.Sprintf("%d", expected)
	case string:
		return typed == fmt.Sprintf("%d", expected)
	default:
		return false
	}
}
