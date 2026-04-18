package storage

import (
	"context"
	"database/sql"
	"path/filepath"
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

func TestSQLiteExecuteAndQueryReturnStructuredResult(t *testing.T) {
	t.Parallel()

	store := openSQLiteStore(t)

	if _, err := store.Exec(`
		create table test_users (
			id integer primary key autoincrement,
			name text not null
		)
	`); err != nil {
		t.Fatalf("create table: %v", err)
	}

	insertResult, err := store.Execute(`insert into test_users (name) values (?), (?)`, "alice", "bob")
	if err != nil {
		t.Fatalf("insert rows: %v", err)
	}
	if insertResult.RowsAffected != 2 {
		t.Fatalf("expected 2 affected rows, got %d", insertResult.RowsAffected)
	}

	queryResult, err := store.Execute(`select id, name from test_users order by id asc`)
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

	firstRow, err := store.QueryOne(`select id, name from test_users where name = ?`, "bob")
	if err != nil {
		t.Fatalf("query one row: %v", err)
	}
	if firstRow["name"] != "bob" {
		t.Fatalf("unexpected query one result: %#v", firstRow["name"])
	}
}

func TestSQLiteTransactionCommitAndRollback(t *testing.T) {
	t.Parallel()

	store := openSQLiteStore(t)

	if _, err := store.Exec(`
		create table tx_items (
			id integer primary key autoincrement,
			name text not null
		)
	`); err != nil {
		t.Fatalf("create table: %v", err)
	}

	tx, err := store.Begin()
	if err != nil {
		t.Fatalf("begin transaction: %v", err)
	}
	if _, err := tx.Exec(`insert into tx_items (name) values (?)`, "commit-me"); err != nil {
		t.Fatalf("insert inside transaction: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit transaction: %v", err)
	}

	tx, err = store.BeginTx(context.Background(), &sql.TxOptions{})
	if err != nil {
		t.Fatalf("begin transaction for rollback: %v", err)
	}
	if _, err := tx.Exec(`insert into tx_items (name) values (?)`, "rollback-me"); err != nil {
		t.Fatalf("insert rollback transaction: %v", err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatalf("rollback transaction: %v", err)
	}

	countRow, err := store.QueryOne(`select count(*) as total from tx_items`)
	if err != nil {
		t.Fatalf("query row count: %v", err)
	}
	if countRow["total"] != int64(1) {
		t.Fatalf("expected committed row count to be 1, got %#v", countRow["total"])
	}
}

func TestWithTxRollsBackOnError(t *testing.T) {
	t.Parallel()

	store := openSQLiteStore(t)

	if _, err := store.Exec(`
		create table tx_callbacks (
			id integer primary key autoincrement,
			name text not null
		)
	`); err != nil {
		t.Fatalf("create table: %v", err)
	}

	expectedErr := store.WithTx(func(tx *Tx) error {
		if _, err := tx.Exec(`insert into tx_callbacks (name) values (?)`, "rolled-back"); err != nil {
			return err
		}
		return sql.ErrTxDone
	})
	if expectedErr == nil {
		t.Fatal("expected transaction callback error")
	}

	countRow, err := store.QueryOne(`select count(*) as total from tx_callbacks`)
	if err != nil {
		t.Fatalf("query callback row count: %v", err)
	}
	if countRow["total"] != int64(0) {
		t.Fatalf("expected rollback to keep row count at 0, got %#v", countRow["total"])
	}
}

func openSQLiteStore(t *testing.T) *SQL {
	t.Helper()

	store := NewSQL(t.Name())
	store.Close()

	db := openSQLiteDB(t)
	if err := store.SetConn(db); err != nil {
		t.Fatalf("set sqlite database: %v", err)
	}

	t.Cleanup(store.Close)

	return store
}

func openSQLiteDB(t *testing.T) *sql.DB {
	t.Helper()

	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "frps.sqlite"))
	if err != nil {
		t.Fatalf("open sqlite database: %v", err)
	}
	if err := db.PingContext(context.Background()); err != nil {
		_ = db.Close()
		t.Fatalf("ping sqlite database: %v", err)
	}

	return db
}
