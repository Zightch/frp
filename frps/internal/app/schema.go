package app

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/zightch/frp/frps/internal/storage"
)

type schemaDefinition struct {
	bootstrapStatements []string
	tables              []tableSpec
}

type tableSpec struct {
	name          string
	columns       []columnSpec
	indexes       [][]string
	uniqueIndexes [][]string
}

type columnSpec struct {
	name          string
	columnType    string
	nullable      bool
	primaryKey    bool
	autoIncrement bool
}

type tableState struct {
	columns       map[string]columnState
	indexes       [][]string
	uniqueIndexes [][]string
}

type columnState struct {
	columnType    string
	nullable      bool
	primaryKey    bool
	autoIncrement bool
}

var schemaDefinitions = map[string]schemaDefinition{
	"sqlite": {
		bootstrapStatements: []string{
			`
CREATE TABLE IF NOT EXISTS proxy_groups (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	name TEXT NOT NULL UNIQUE,
	client_id TEXT NOT NULL UNIQUE,
	client_secret_hash TEXT NOT NULL,
	effective_ip TEXT NOT NULL,
	enabled INTEGER NOT NULL DEFAULT 1,
	rate_limit INTEGER NOT NULL DEFAULT 0,
	created_at TEXT NOT NULL,
	updated_at TEXT NOT NULL
)`,
			`
CREATE TABLE IF NOT EXISTS tunnels (
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
	enabled INTEGER NOT NULL DEFAULT 1,
	created_at TEXT NOT NULL,
	updated_at TEXT NOT NULL,
	UNIQUE(group_id, name)
)`,
			`
CREATE TABLE IF NOT EXISTS certificate_assets (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	name TEXT NOT NULL,
	remark TEXT NOT NULL,
	source TEXT NOT NULL,
	asset_type TEXT NOT NULL,
	format_type TEXT NOT NULL,
	crt TEXT NOT NULL,
	crt_hash TEXT NOT NULL,
	key TEXT NOT NULL,
	issuer_asset_id INTEGER,
	created_at TEXT NOT NULL,
	updated_at TEXT NOT NULL
)`,
			`CREATE UNIQUE INDEX IF NOT EXISTS uk_certificate_assets_name ON certificate_assets (name)`,
			`CREATE INDEX IF NOT EXISTS idx_certificate_assets_crt_hash ON certificate_assets (crt_hash)`,
			`CREATE INDEX IF NOT EXISTS idx_certificate_assets_issuer_asset_id ON certificate_assets (issuer_asset_id)`,
		},
		tables: []tableSpec{
			{
				name: "proxy_groups",
				columns: []columnSpec{
					{name: "id", columnType: "integer", nullable: false, primaryKey: true},
					{name: "name", columnType: "text", nullable: false},
					{name: "client_id", columnType: "text", nullable: false},
					{name: "client_secret_hash", columnType: "text", nullable: false},
					{name: "effective_ip", columnType: "text", nullable: false},
					{name: "enabled", columnType: "integer", nullable: false},
					{name: "rate_limit", columnType: "integer", nullable: false},
					{name: "created_at", columnType: "text", nullable: false},
					{name: "updated_at", columnType: "text", nullable: false},
				},
				uniqueIndexes: [][]string{
					{"name"},
					{"client_id"},
				},
			},
			{
				name: "tunnels",
				columns: []columnSpec{
					{name: "id", columnType: "integer", nullable: false, primaryKey: true},
					{name: "group_id", columnType: "integer", nullable: false},
					{name: "name", columnType: "text", nullable: false},
					{name: "protocol", columnType: "text", nullable: false},
					{name: "remote_type", columnType: "text", nullable: false},
					{name: "remote_start", columnType: "integer", nullable: false},
					{name: "remote_end", columnType: "integer", nullable: false},
					{name: "local_host", columnType: "text", nullable: false},
					{name: "local_start", columnType: "integer", nullable: false},
					{name: "local_end", columnType: "integer", nullable: false},
					{name: "enabled", columnType: "integer", nullable: false},
					{name: "created_at", columnType: "text", nullable: false},
					{name: "updated_at", columnType: "text", nullable: false},
				},
				uniqueIndexes: [][]string{
					{"group_id", "name"},
				},
			},
			{
				name: "certificate_assets",
				columns: []columnSpec{
					{name: "id", columnType: "integer", nullable: false, primaryKey: true},
					{name: "name", columnType: "text", nullable: false},
					{name: "remark", columnType: "text", nullable: false},
					{name: "source", columnType: "text", nullable: false},
					{name: "asset_type", columnType: "text", nullable: false},
					{name: "format_type", columnType: "text", nullable: false},
					{name: "crt", columnType: "text", nullable: false},
					{name: "crt_hash", columnType: "text", nullable: false},
					{name: "key", columnType: "text", nullable: false},
					{name: "issuer_asset_id", columnType: "integer", nullable: true},
					{name: "created_at", columnType: "text", nullable: false},
					{name: "updated_at", columnType: "text", nullable: false},
				},
				indexes: [][]string{
					{"crt_hash"},
					{"issuer_asset_id"},
				},
				uniqueIndexes: [][]string{
					{"name"},
				},
			},
		},
	},
	"mysql": {
		bootstrapStatements: []string{
			`
CREATE TABLE IF NOT EXISTS proxy_groups (
	id BIGINT NOT NULL AUTO_INCREMENT,
	name VARCHAR(128) NOT NULL,
	client_id CHAR(32) NOT NULL,
	client_secret_hash CHAR(64) NOT NULL,
	effective_ip VARCHAR(45) NOT NULL,
	enabled TINYINT(1) NOT NULL DEFAULT 1,
	rate_limit BIGINT NOT NULL DEFAULT 0,
	created_at DATETIME(6) NOT NULL,
	updated_at DATETIME(6) NOT NULL,
	PRIMARY KEY (id),
	UNIQUE KEY uk_proxy_groups_name (name),
	UNIQUE KEY uk_proxy_groups_client_id (client_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci`,
			`
CREATE TABLE IF NOT EXISTS tunnels (
	id BIGINT NOT NULL AUTO_INCREMENT,
	group_id BIGINT NOT NULL,
	name VARCHAR(128) NOT NULL,
	protocol VARCHAR(16) NOT NULL,
	remote_type VARCHAR(16) NOT NULL,
	remote_start BIGINT NOT NULL,
	remote_end BIGINT NOT NULL,
	local_host VARCHAR(255) NOT NULL,
	local_start BIGINT NOT NULL,
	local_end BIGINT NOT NULL,
	enabled TINYINT(1) NOT NULL DEFAULT 1,
	created_at DATETIME(6) NOT NULL,
	updated_at DATETIME(6) NOT NULL,
	PRIMARY KEY (id),
	UNIQUE KEY uk_tunnels_group_name (group_id, name)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci`,
			`
CREATE TABLE IF NOT EXISTS certificate_assets (
	id BIGINT NOT NULL AUTO_INCREMENT,
	name VARCHAR(128) NOT NULL,
	remark TEXT NOT NULL,
	source VARCHAR(16) NOT NULL,
	asset_type VARCHAR(16) NOT NULL,
	format_type VARCHAR(16) NOT NULL,
	crt MEDIUMTEXT NOT NULL,
	crt_hash CHAR(64) NOT NULL,
	` + "`key`" + ` MEDIUMTEXT NOT NULL,
	issuer_asset_id BIGINT NULL,
	created_at DATETIME(6) NOT NULL,
	updated_at DATETIME(6) NOT NULL,
	PRIMARY KEY (id),
	UNIQUE KEY uk_certificate_assets_name (name),
	KEY idx_certificate_assets_crt_hash (crt_hash),
	KEY idx_certificate_assets_issuer_asset_id (issuer_asset_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci`,
		},
		tables: []tableSpec{
			{
				name: "proxy_groups",
				columns: []columnSpec{
					{name: "id", columnType: "bigint", nullable: false, primaryKey: true, autoIncrement: true},
					{name: "name", columnType: "varchar(128)", nullable: false},
					{name: "client_id", columnType: "char(32)", nullable: false},
					{name: "client_secret_hash", columnType: "char(64)", nullable: false},
					{name: "effective_ip", columnType: "varchar(45)", nullable: false},
					{name: "enabled", columnType: "tinyint(1)", nullable: false},
					{name: "rate_limit", columnType: "bigint", nullable: false},
					{name: "created_at", columnType: "datetime(6)", nullable: false},
					{name: "updated_at", columnType: "datetime(6)", nullable: false},
				},
				uniqueIndexes: [][]string{
					{"name"},
					{"client_id"},
				},
			},
			{
				name: "tunnels",
				columns: []columnSpec{
					{name: "id", columnType: "bigint", nullable: false, primaryKey: true, autoIncrement: true},
					{name: "group_id", columnType: "bigint", nullable: false},
					{name: "name", columnType: "varchar(128)", nullable: false},
					{name: "protocol", columnType: "varchar(16)", nullable: false},
					{name: "remote_type", columnType: "varchar(16)", nullable: false},
					{name: "remote_start", columnType: "bigint", nullable: false},
					{name: "remote_end", columnType: "bigint", nullable: false},
					{name: "local_host", columnType: "varchar(255)", nullable: false},
					{name: "local_start", columnType: "bigint", nullable: false},
					{name: "local_end", columnType: "bigint", nullable: false},
					{name: "enabled", columnType: "tinyint(1)", nullable: false},
					{name: "created_at", columnType: "datetime(6)", nullable: false},
					{name: "updated_at", columnType: "datetime(6)", nullable: false},
				},
				uniqueIndexes: [][]string{
					{"group_id", "name"},
				},
			},
			{
				name: "certificate_assets",
				columns: []columnSpec{
					{name: "id", columnType: "bigint", nullable: false, primaryKey: true, autoIncrement: true},
					{name: "name", columnType: "varchar(128)", nullable: false},
					{name: "remark", columnType: "text", nullable: false},
					{name: "source", columnType: "varchar(16)", nullable: false},
					{name: "asset_type", columnType: "varchar(16)", nullable: false},
					{name: "format_type", columnType: "varchar(16)", nullable: false},
					{name: "crt", columnType: "mediumtext", nullable: false},
					{name: "crt_hash", columnType: "char(64)", nullable: false},
					{name: "key", columnType: "mediumtext", nullable: false},
					{name: "issuer_asset_id", columnType: "bigint", nullable: true},
					{name: "created_at", columnType: "datetime(6)", nullable: false},
					{name: "updated_at", columnType: "datetime(6)", nullable: false},
				},
				indexes: [][]string{
					{"crt_hash"},
					{"issuer_asset_id"},
				},
				uniqueIndexes: [][]string{
					{"name"},
				},
			},
		},
	},
}

func ensureDatabaseSchema(ctx context.Context, store *storage.SQL, databaseType string) error {
	definition, ok := schemaDefinitions[strings.ToLower(strings.TrimSpace(databaseType))]
	if !ok {
		return fmt.Errorf("unsupported database type %q", databaseType)
	}

	for _, statement := range definition.bootstrapStatements {
		if _, err := store.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("bootstrap required tables: %w", err)
		}
	}

	if err := validateSchemaDefinition(ctx, store, databaseType, definition); err != nil {
		return fmt.Errorf(
			"database schema validation failed; development stage does not support schema compatibility or migration: %w",
			err,
		)
	}

	return nil
}

func validateSchemaDefinition(ctx context.Context, store *storage.SQL, databaseType string, definition schemaDefinition) error {
	for _, table := range definition.tables {
		state, err := loadTableState(ctx, store, databaseType, table.name)
		if err != nil {
			return fmt.Errorf("load schema for table %s: %w", table.name, err)
		}

		if err := validateTable(table, state); err != nil {
			return fmt.Errorf("table %s: %w", table.name, err)
		}
	}

	return nil
}

func validateTable(expected tableSpec, actual tableState) error {
	if len(actual.columns) == 0 {
		return fmt.Errorf("table is missing")
	}

	if len(actual.columns) != len(expected.columns) {
		return fmt.Errorf("column count mismatch: got %d want %d", len(actual.columns), len(expected.columns))
	}

	for _, column := range expected.columns {
		actualColumn, ok := actual.columns[column.name]
		if !ok {
			return fmt.Errorf("missing column %s", column.name)
		}

		if actualColumn.columnType != column.columnType {
			return fmt.Errorf(
				"column %s type mismatch: got %s want %s",
				column.name,
				actualColumn.columnType,
				column.columnType,
			)
		}

		if actualColumn.nullable != column.nullable {
			return fmt.Errorf(
				"column %s nullability mismatch: got %t want %t",
				column.name,
				actualColumn.nullable,
				column.nullable,
			)
		}

		if actualColumn.primaryKey != column.primaryKey {
			return fmt.Errorf(
				"column %s primary key mismatch: got %t want %t",
				column.name,
				actualColumn.primaryKey,
				column.primaryKey,
			)
		}

		if column.autoIncrement && !actualColumn.autoIncrement {
			return fmt.Errorf("column %s must be auto increment", column.name)
		}
	}

	expectedUnique := make([]string, 0, len(expected.uniqueIndexes))
	for _, columns := range expected.uniqueIndexes {
		expectedUnique = append(expectedUnique, uniqueIndexSignature(columns))
	}
	slices.Sort(expectedUnique)

	expectedIndexes := make([]string, 0, len(expected.indexes))
	for _, columns := range expected.indexes {
		expectedIndexes = append(expectedIndexes, uniqueIndexSignature(columns))
	}
	slices.Sort(expectedIndexes)

	actualUnique := make([]string, 0, len(actual.uniqueIndexes))
	for _, columns := range actual.uniqueIndexes {
		actualUnique = append(actualUnique, uniqueIndexSignature(columns))
	}
	slices.Sort(actualUnique)

	actualIndexes := make([]string, 0, len(actual.indexes))
	for _, columns := range actual.indexes {
		actualIndexes = append(actualIndexes, uniqueIndexSignature(columns))
	}
	slices.Sort(actualIndexes)

	if !slices.Equal(actualUnique, expectedUnique) {
		return fmt.Errorf("unique index mismatch: got %v want %v", actualUnique, expectedUnique)
	}
	if !slices.Equal(actualIndexes, expectedIndexes) {
		return fmt.Errorf("index mismatch: got %v want %v", actualIndexes, expectedIndexes)
	}

	return nil
}

func loadTableState(ctx context.Context, store *storage.SQL, databaseType, tableName string) (tableState, error) {
	switch strings.ToLower(strings.TrimSpace(databaseType)) {
	case "sqlite":
		return loadSQLiteTableState(ctx, store, tableName)
	case "mysql":
		return loadMySQLTableState(ctx, store, tableName)
	default:
		return tableState{}, fmt.Errorf("unsupported database type %q", databaseType)
	}
}

func loadSQLiteTableState(ctx context.Context, store *storage.SQL, tableName string) (tableState, error) {
	result, err := store.QueryContext(
		ctx,
		fmt.Sprintf("PRAGMA table_info(%s)", sqliteIdentifier(tableName)),
	)
	if err != nil {
		return tableState{}, err
	}
	if len(result.Rows) == 0 {
		return tableState{}, nil
	}

	state := tableState{columns: make(map[string]columnState, len(result.Rows))}
	for _, row := range result.Rows {
		columnName := strings.ToLower(strings.TrimSpace(rowString(row, "name")))
		columnType := strings.ToLower(strings.TrimSpace(rowString(row, "type")))
		primaryKey, err := rowBool(row, "pk")
		if err != nil {
			return tableState{}, fmt.Errorf("column %s primary key metadata: %w", columnName, err)
		}

		nullable := true
		notNull, err := rowBool(row, "notnull")
		if err != nil {
			return tableState{}, fmt.Errorf("column %s nullability metadata: %w", columnName, err)
		}
		if notNull || primaryKey {
			nullable = false
		}

		state.columns[columnName] = columnState{
			columnType: columnType,
			nullable:   nullable,
			primaryKey: primaryKey,
		}
	}

	indexes, err := store.QueryContext(
		ctx,
		fmt.Sprintf("PRAGMA index_list(%s)", sqliteIdentifier(tableName)),
	)
	if err != nil {
		return tableState{}, err
	}

	for _, row := range indexes.Rows {
		unique, err := rowBool(row, "unique")
		if err != nil {
			continue
		}

		if strings.EqualFold(rowString(row, "origin"), "pk") {
			continue
		}

		indexName := rowString(row, "name")
		indexColumns, err := store.QueryContext(
			ctx,
			fmt.Sprintf("PRAGMA index_info(%s)", sqliteIdentifier(indexName)),
		)
		if err != nil {
			return tableState{}, err
		}

		columns := make([]string, 0, len(indexColumns.Rows))
		for _, indexRow := range indexColumns.Rows {
			columns = append(columns, strings.ToLower(strings.TrimSpace(rowString(indexRow, "name"))))
		}

		if len(columns) > 0 {
			if unique {
				state.uniqueIndexes = append(state.uniqueIndexes, columns)
			} else {
				state.indexes = append(state.indexes, columns)
			}
		}
	}

	return state, nil
}

func loadMySQLTableState(ctx context.Context, store *storage.SQL, tableName string) (tableState, error) {
	columns, err := store.QueryContext(
		ctx,
		`
SELECT
	COLUMN_NAME,
	COLUMN_TYPE,
	IS_NULLABLE,
	COLUMN_KEY,
	EXTRA
FROM information_schema.columns
WHERE table_schema = DATABASE() AND table_name = ?
ORDER BY ORDINAL_POSITION
`,
		tableName,
	)
	if err != nil {
		return tableState{}, err
	}
	if len(columns.Rows) == 0 {
		return tableState{}, nil
	}

	state := tableState{columns: make(map[string]columnState, len(columns.Rows))}
	for _, row := range columns.Rows {
		columnName := strings.ToLower(strings.TrimSpace(rowString(row, "COLUMN_NAME")))
		columnType := strings.ToLower(strings.TrimSpace(rowString(row, "COLUMN_TYPE")))
		nullable := !strings.EqualFold(rowString(row, "IS_NULLABLE"), "NO")
		primaryKey := strings.EqualFold(rowString(row, "COLUMN_KEY"), "PRI")
		autoIncrement := strings.Contains(strings.ToLower(rowString(row, "EXTRA")), "auto_increment")

		state.columns[columnName] = columnState{
			columnType:    columnType,
			nullable:      nullable,
			primaryKey:    primaryKey,
			autoIncrement: autoIncrement,
		}
	}

	indexes, err := store.QueryContext(
		ctx,
		`
SELECT
	INDEX_NAME,
	NON_UNIQUE,
	SEQ_IN_INDEX,
	COLUMN_NAME
FROM information_schema.statistics
WHERE table_schema = DATABASE() AND table_name = ?
ORDER BY INDEX_NAME, SEQ_IN_INDEX
`,
		tableName,
	)
	if err != nil {
		return tableState{}, err
	}

	indexMap := make(map[string][]string)
	indexUnique := make(map[string]bool)
	for _, row := range indexes.Rows {
		if strings.EqualFold(rowString(row, "INDEX_NAME"), "PRIMARY") {
			continue
		}

		nonUnique, err := rowBool(row, "NON_UNIQUE")
		if err != nil {
			continue
		}

		indexName := rowString(row, "INDEX_NAME")
		indexMap[indexName] = append(
			indexMap[indexName],
			strings.ToLower(strings.TrimSpace(rowString(row, "COLUMN_NAME"))),
		)
		indexUnique[indexName] = !nonUnique
	}

	indexNames := make([]string, 0, len(indexMap))
	for indexName := range indexMap {
		indexNames = append(indexNames, indexName)
	}
	slices.Sort(indexNames)

	for _, indexName := range indexNames {
		if indexUnique[indexName] {
			state.uniqueIndexes = append(state.uniqueIndexes, indexMap[indexName])
		} else {
			state.indexes = append(state.indexes, indexMap[indexName])
		}
	}

	return state, nil
}

func rowString(row storage.Row, key string) string {
	value, ok := rowValue(row, key)
	if !ok || value == nil {
		return ""
	}

	switch typed := value.(type) {
	case string:
		return typed
	case []byte:
		return string(typed)
	default:
		return fmt.Sprint(typed)
	}
}

func rowInt64(row storage.Row, key string) (int64, error) {
	value, ok := rowValue(row, key)
	if !ok || value == nil {
		return 0, nil
	}

	switch typed := value.(type) {
	case int:
		return int64(typed), nil
	case int8:
		return int64(typed), nil
	case int16:
		return int64(typed), nil
	case int32:
		return int64(typed), nil
	case int64:
		return typed, nil
	case uint:
		return int64(typed), nil
	case uint8:
		return int64(typed), nil
	case uint16:
		return int64(typed), nil
	case uint32:
		return int64(typed), nil
	case uint64:
		return int64(typed), nil
	case bool:
		if typed {
			return 1, nil
		}
		return 0, nil
	case string:
		return strconv.ParseInt(strings.TrimSpace(typed), 10, 64)
	case []byte:
		return strconv.ParseInt(strings.TrimSpace(string(typed)), 10, 64)
	default:
		return 0, fmt.Errorf("unsupported numeric value %T", value)
	}
}

func rowValue(row storage.Row, key string) (any, bool) {
	value, ok := row[key]
	if ok {
		return value, true
	}

	for candidateKey, candidateValue := range row {
		if strings.EqualFold(candidateKey, key) {
			return candidateValue, true
		}
	}

	return nil, false
}

func rowBool(row storage.Row, key string) (bool, error) {
	value, err := rowInt64(row, key)
	if err == nil {
		return value != 0, nil
	}

	text := strings.ToLower(strings.TrimSpace(rowString(row, key)))
	switch text {
	case "", "0", "false", "no":
		return false, nil
	case "1", "true", "yes":
		return true, nil
	default:
		return false, err
	}
}

func uniqueIndexSignature(columns []string) string {
	normalized := make([]string, 0, len(columns))
	for _, column := range columns {
		normalized = append(normalized, strings.ToLower(strings.TrimSpace(column)))
	}
	return strings.Join(normalized, ",")
}

func sqliteIdentifier(name string) string {
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}
