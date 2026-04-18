package storage

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"sync"
	"unicode/utf8"
)

const defaultObjectID = "default"

type Row map[string]any

type Result struct {
	Columns         []string
	Rows            []Row
	RowsAffected    int64
	LastInsertID    int64
	HasLastInsertID bool
}

type Conn interface {
	QueryContext(ctx context.Context, cmd string, args ...any) (Result, error)
	Query(cmd string, args ...any) (Result, error)
	QueryOneContext(ctx context.Context, cmd string, args ...any) (Row, error)
	QueryOne(cmd string, args ...any) (Row, error)
	ExecContext(ctx context.Context, cmd string, args ...any) (Result, error)
	Exec(cmd string, args ...any) (Result, error)
	ExecuteContext(ctx context.Context, cmd string, args ...any) (Result, error)
	Execute(cmd string, args ...any) (Result, error)
}

type SQL struct {
	objectID string

	mu   sync.RWMutex
	conn *sql.DB
}

type Tx struct {
	parent *SQL

	mu   sync.RWMutex
	conn *sql.Tx
}

type queryRunner interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

type execRunner interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

type queryExecRunner interface {
	queryRunner
	execRunner
}

var (
	sqlInstances   = make(map[string]*SQL)
	sqlInstancesMu sync.Mutex
)

var (
	_ Conn = (*SQL)(nil)
	_ Conn = (*Tx)(nil)
)

func NewSQL(objectID string) *SQL {
	objectID = strings.TrimSpace(objectID)
	if objectID == "" {
		objectID = defaultObjectID
	}

	sqlInstancesMu.Lock()
	defer sqlInstancesMu.Unlock()

	instance, exists := sqlInstances[objectID]
	if !exists {
		instance = &SQL{objectID: objectID}
		sqlInstances[objectID] = instance
	}

	return instance
}

func NewSQLWithConn(objectID string, conn *sql.DB) (*SQL, error) {
	store := NewSQL(objectID)
	if err := store.SetConn(conn); err != nil {
		return nil, err
	}

	return store, nil
}

func (s *SQL) ObjectID() string {
	return s.objectID
}

func (s *SQL) Raw() *sql.DB {
	s.mu.RLock()
	defer s.mu.RUnlock()

	return s.conn
}

func (s *SQL) SetConn(conn *sql.DB) error {
	if conn == nil {
		return fmt.Errorf("database conn is nil")
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if s.conn != nil && s.conn != conn {
		return fmt.Errorf("database already set")
	}

	s.conn = conn
	return nil
}

func (s *SQL) Ping() error {
	return s.PingContext(context.Background())
}

func (s *SQL) PingContext(ctx context.Context) error {
	ctx = ensureContext(ctx)

	conn, err := s.rawConn()
	if err != nil {
		return err
	}

	return conn.PingContext(ctx)
}

func (s *SQL) Begin() (*Tx, error) {
	return s.BeginTx(context.Background(), nil)
}

func (s *SQL) BeginTx(ctx context.Context, options *sql.TxOptions) (*Tx, error) {
	ctx = ensureContext(ctx)

	conn, err := s.rawConn()
	if err != nil {
		return nil, err
	}

	tx, err := conn.BeginTx(ctx, options)
	if err != nil {
		return nil, err
	}

	return &Tx{
		parent: s,
		conn:   tx,
	}, nil
}

func (s *SQL) WithTx(fn func(*Tx) error) error {
	return s.WithTxContext(context.Background(), nil, fn)
}

func (s *SQL) WithTxContext(ctx context.Context, options *sql.TxOptions, fn func(*Tx) error) error {
	ctx = ensureContext(ctx)

	tx, err := s.BeginTx(ctx, options)
	if err != nil {
		return err
	}

	if err := fn(tx); err != nil {
		if rollbackErr := tx.Rollback(); rollbackErr != nil && rollbackErr != sql.ErrTxDone {
			return fmt.Errorf("rollback transaction: %w (original error: %v)", rollbackErr, err)
		}
		return err
	}

	return tx.Commit()
}

func (s *SQL) Query(cmd string, args ...any) (Result, error) {
	return s.QueryContext(context.Background(), cmd, args...)
}

func (s *SQL) QueryContext(ctx context.Context, cmd string, args ...any) (Result, error) {
	ctx = ensureContext(ctx)

	conn, err := s.rawConn()
	if err != nil {
		return Result{}, err
	}

	return queryContext(ctx, conn, cmd, args...)
}

func (s *SQL) QueryOne(cmd string, args ...any) (Row, error) {
	return s.QueryOneContext(context.Background(), cmd, args...)
}

func (s *SQL) QueryOneContext(ctx context.Context, cmd string, args ...any) (Row, error) {
	ctx = ensureContext(ctx)

	conn, err := s.rawConn()
	if err != nil {
		return nil, err
	}

	return queryOneContext(ctx, conn, cmd, args...)
}

func (s *SQL) Exec(cmd string, args ...any) (Result, error) {
	return s.ExecContext(context.Background(), cmd, args...)
}

func (s *SQL) ExecContext(ctx context.Context, cmd string, args ...any) (Result, error) {
	ctx = ensureContext(ctx)

	conn, err := s.rawConn()
	if err != nil {
		return Result{}, err
	}

	return execContext(ctx, conn, cmd, args...)
}

func (s *SQL) Execute(cmd string, args ...any) (Result, error) {
	return s.ExecuteContext(context.Background(), cmd, args...)
}

func (s *SQL) ExecuteContext(ctx context.Context, cmd string, args ...any) (Result, error) {
	ctx = ensureContext(ctx)

	conn, err := s.rawConn()
	if err != nil {
		return Result{}, err
	}

	return executeContext(ctx, conn, cmd, args...)
}

func (s *SQL) Close() {
	s.mu.Lock()
	conn := s.conn
	s.conn = nil
	s.mu.Unlock()

	if conn != nil {
		_ = conn.Close()
	}
}

func (s *SQL) rawConn() (*sql.DB, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if s.conn == nil {
		return nil, fmt.Errorf("database not open")
	}

	return s.conn, nil
}

func (tx *Tx) Parent() *SQL {
	return tx.parent
}

func (tx *Tx) Raw() *sql.Tx {
	tx.mu.RLock()
	defer tx.mu.RUnlock()

	return tx.conn
}

func (tx *Tx) Query(cmd string, args ...any) (Result, error) {
	return tx.QueryContext(context.Background(), cmd, args...)
}

func (tx *Tx) QueryContext(ctx context.Context, cmd string, args ...any) (Result, error) {
	ctx = ensureContext(ctx)

	conn, err := tx.rawConn()
	if err != nil {
		return Result{}, err
	}

	return queryContext(ctx, conn, cmd, args...)
}

func (tx *Tx) QueryOne(cmd string, args ...any) (Row, error) {
	return tx.QueryOneContext(context.Background(), cmd, args...)
}

func (tx *Tx) QueryOneContext(ctx context.Context, cmd string, args ...any) (Row, error) {
	ctx = ensureContext(ctx)

	conn, err := tx.rawConn()
	if err != nil {
		return nil, err
	}

	return queryOneContext(ctx, conn, cmd, args...)
}

func (tx *Tx) Exec(cmd string, args ...any) (Result, error) {
	return tx.ExecContext(context.Background(), cmd, args...)
}

func (tx *Tx) ExecContext(ctx context.Context, cmd string, args ...any) (Result, error) {
	ctx = ensureContext(ctx)

	conn, err := tx.rawConn()
	if err != nil {
		return Result{}, err
	}

	return execContext(ctx, conn, cmd, args...)
}

func (tx *Tx) Execute(cmd string, args ...any) (Result, error) {
	return tx.ExecuteContext(context.Background(), cmd, args...)
}

func (tx *Tx) ExecuteContext(ctx context.Context, cmd string, args ...any) (Result, error) {
	ctx = ensureContext(ctx)

	conn, err := tx.rawConn()
	if err != nil {
		return Result{}, err
	}

	return executeContext(ctx, conn, cmd, args...)
}

func (tx *Tx) Commit() error {
	tx.mu.Lock()
	conn := tx.conn
	tx.conn = nil
	tx.mu.Unlock()

	if conn == nil {
		return sql.ErrTxDone
	}

	return conn.Commit()
}

func (tx *Tx) Rollback() error {
	tx.mu.Lock()
	conn := tx.conn
	tx.conn = nil
	tx.mu.Unlock()

	if conn == nil {
		return sql.ErrTxDone
	}

	return conn.Rollback()
}

func (tx *Tx) rawConn() (*sql.Tx, error) {
	tx.mu.RLock()
	defer tx.mu.RUnlock()

	if tx.conn == nil {
		return nil, sql.ErrTxDone
	}

	return tx.conn, nil
}

func (r Result) First() (Row, bool) {
	if len(r.Rows) == 0 {
		return nil, false
	}

	return r.Rows[0], true
}

func executeContext(ctx context.Context, conn queryExecRunner, cmd string, args ...any) (Result, error) {
	if statementReturnsRows(cmd) {
		return queryContext(ctx, conn, cmd, args...)
	}

	return execContext(ctx, conn, cmd, args...)
}

func queryContext(ctx context.Context, conn queryRunner, cmd string, args ...any) (Result, error) {
	cmd = strings.TrimSpace(cmd)
	if cmd == "" {
		return Result{}, fmt.Errorf("query is required")
	}

	rows, err := conn.QueryContext(ctx, cmd, args...)
	if err != nil {
		return Result{}, err
	}
	defer rows.Close()

	return readRows(rows)
}

func queryOneContext(ctx context.Context, conn queryRunner, cmd string, args ...any) (Row, error) {
	result, err := queryContext(ctx, conn, cmd, args...)
	if err != nil {
		return nil, err
	}

	row, ok := result.First()
	if !ok {
		return nil, sql.ErrNoRows
	}

	return row, nil
}

func execContext(ctx context.Context, conn execRunner, cmd string, args ...any) (Result, error) {
	cmd = strings.TrimSpace(cmd)
	if cmd == "" {
		return Result{}, fmt.Errorf("query is required")
	}

	result, err := conn.ExecContext(ctx, cmd, args...)
	if err != nil {
		return Result{}, err
	}

	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return Result{}, err
	}

	execResult := Result{RowsAffected: rowsAffected}
	if lastInsertID, err := result.LastInsertId(); err == nil {
		execResult.LastInsertID = lastInsertID
		execResult.HasLastInsertID = true
	}

	return execResult, nil
}

func readRows(rows *sql.Rows) (Result, error) {
	columns, err := rows.Columns()
	if err != nil {
		return Result{}, err
	}

	result := Result{
		Columns: append([]string(nil), columns...),
		Rows:    make([]Row, 0),
	}

	for rows.Next() {
		values := make([]any, len(columns))
		valuePointers := make([]any, len(columns))
		for index := range values {
			valuePointers[index] = &values[index]
		}

		if err := rows.Scan(valuePointers...); err != nil {
			return Result{}, err
		}

		row := make(Row, len(columns))
		for index, column := range columns {
			row[column] = normalizeValue(values[index])
		}
		result.Rows = append(result.Rows, row)
	}

	if err := rows.Err(); err != nil {
		return Result{}, err
	}

	return result, nil
}

func normalizeValue(value any) any {
	byteValue, ok := value.([]byte)
	if !ok {
		return value
	}

	copiedValue := append([]byte(nil), byteValue...)
	if utf8.Valid(copiedValue) {
		return string(copiedValue)
	}

	return copiedValue
}

func statementReturnsRows(cmd string) bool {
	switch leadingKeyword(cmd) {
	case "select", "show", "describe", "desc", "explain", "pragma", "with":
		return true
	case "insert", "update", "delete":
		return strings.Contains(strings.ToLower(cmd), " returning ")
	default:
		return false
	}
}

func leadingKeyword(cmd string) string {
	cmd = strings.TrimSpace(cmd)
	for cmd != "" {
		switch {
		case strings.HasPrefix(cmd, "--"):
			nextLine := strings.IndexByte(cmd, '\n')
			if nextLine == -1 {
				return ""
			}
			cmd = strings.TrimSpace(cmd[nextLine+1:])
		case strings.HasPrefix(cmd, "/*"):
			commentEnd := strings.Index(cmd, "*/")
			if commentEnd == -1 {
				return ""
			}
			cmd = strings.TrimSpace(cmd[commentEnd+2:])
		default:
			fields := strings.Fields(cmd)
			if len(fields) == 0 {
				return ""
			}
			return strings.ToLower(fields[0])
		}
	}

	return ""
}

func ensureContext(ctx context.Context) context.Context {
	if ctx == nil {
		return context.Background()
	}

	return ctx
}
