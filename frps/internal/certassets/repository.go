package certassets

import (
	"context"
	"database/sql"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/zightch/frp/frps/internal/storage"
)

const schemaTimestampLayout = "2006-01-02 15:04:05.000000"

type Repository interface {
	ListAssets(ctx context.Context) ([]Asset, error)
}

type SQLRepository struct {
	store *storage.SQL
}

func NewRepository(store *storage.SQL) *SQLRepository {
	return &SQLRepository{store: store}
}

func (r *SQLRepository) ListAssets(ctx context.Context) ([]Asset, error) {
	if r == nil || r.store == nil {
		return nil, fmt.Errorf("repository store is nil")
	}

	return ListAssetsWithConn(ctx, r.store)
}

func ListAssetsWithConn(ctx context.Context, conn storage.Conn) ([]Asset, error) {
	if conn == nil {
		return nil, fmt.Errorf("asset connection is nil")
	}

	result, err := conn.QueryContext(
		ctx,
		buildAssetSelectQuery("ORDER BY id"),
	)
	if err != nil {
		return nil, fmt.Errorf("list certificate assets: %w", err)
	}

	assets := make([]Asset, 0, len(result.Rows))
	for _, row := range result.Rows {
		asset, err := decodeAssetRow(row)
		if err != nil {
			return nil, fmt.Errorf("decode certificate asset: %w", err)
		}
		assets = append(assets, asset)
	}
	return assets, nil
}

func LoadAssetByID(ctx context.Context, conn storage.Conn, id int64) (Asset, error) {
	if conn == nil {
		return Asset{}, fmt.Errorf("asset connection is nil")
	}

	row, err := conn.QueryOneContext(ctx, buildAssetSelectQuery("WHERE id = ?"), id)
	if err != nil {
		if err == sql.ErrNoRows {
			return Asset{}, err
		}
		return Asset{}, fmt.Errorf("load certificate asset: %w", err)
	}

	asset, err := decodeAssetRow(row)
	if err != nil {
		return Asset{}, fmt.Errorf("decode certificate asset: %w", err)
	}
	return asset, nil
}

func ListAssetsByCRTHash(ctx context.Context, conn storage.Conn, crtHash string) ([]Asset, error) {
	if conn == nil {
		return nil, fmt.Errorf("asset connection is nil")
	}

	crtHash = strings.ToLower(strings.TrimSpace(crtHash))
	if crtHash == "" {
		return nil, nil
	}

	result, err := conn.QueryContext(
		ctx,
		buildAssetSelectQuery("WHERE crt_hash = ? ORDER BY id"),
		crtHash,
	)
	if err != nil {
		return nil, fmt.Errorf("list certificate assets by crt_hash: %w", err)
	}

	assets := make([]Asset, 0, len(result.Rows))
	for _, row := range result.Rows {
		asset, err := decodeAssetRow(row)
		if err != nil {
			return nil, fmt.Errorf("decode certificate asset: %w", err)
		}
		assets = append(assets, asset)
	}
	return assets, nil
}

func InsertAsset(ctx context.Context, conn storage.Conn, asset Asset) (int64, error) {
	if conn == nil {
		return 0, fmt.Errorf("asset connection is nil")
	}

	result, err := conn.ExecContext(
		ctx,
		`
INSERT INTO certificate_assets (
	name,
	remark,
	source,
	asset_type,
	format_type,
	crt,
	crt_hash,
	`+"`key`"+`,
	issuer_asset_id,
	created_at,
	updated_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
`,
		asset.Name,
		asset.Remark,
		string(asset.Source),
		string(asset.AssetType),
		string(asset.FormatType),
		asset.CRT,
		asset.CRTHash,
		asset.Key,
		nullableInt64(asset.IssuerAssetID),
		formatTimestamp(asset.CreatedAt),
		formatTimestamp(asset.UpdatedAt),
	)
	if err != nil {
		return 0, fmt.Errorf("insert certificate asset: %w", err)
	}
	return result.LastInsertID, nil
}

func DeleteAssetsByID(ctx context.Context, conn storage.Conn, ids []int64) error {
	if conn == nil {
		return fmt.Errorf("asset connection is nil")
	}
	if len(ids) == 0 {
		return nil
	}

	placeholders := make([]string, 0, len(ids))
	args := make([]any, 0, len(ids))
	for _, id := range ids {
		placeholders = append(placeholders, "?")
		args = append(args, id)
	}

	if _, err := conn.ExecContext(
		ctx,
		fmt.Sprintf("DELETE FROM certificate_assets WHERE id IN (%s)", strings.Join(placeholders, ", ")),
		args...,
	); err != nil {
		return fmt.Errorf("delete certificate assets: %w", err)
	}
	return nil
}

func buildAssetSelectQuery(suffix string) string {
	suffix = strings.TrimSpace(suffix)
	if suffix != "" {
		suffix = "\n" + suffix
	}
	return `
SELECT
	id,
	name,
	remark,
	source,
	asset_type,
	format_type,
	crt,
	crt_hash,
	` + "`key`" + `,
	issuer_asset_id,
	created_at,
	updated_at
FROM certificate_assets` + suffix
}

func nullableInt64(value *int64) any {
	if value == nil {
		return nil
	}
	return *value
}

func formatTimestamp(value time.Time) string {
	if value.IsZero() {
		value = time.Now().UTC()
	}
	return value.UTC().Format(schemaTimestampLayout)
}

func decodeAssetRow(row storage.Row) (Asset, error) {
	var asset Asset

	id, err := rowInt64(row, "id")
	if err != nil {
		return Asset{}, fmt.Errorf("id: %w", err)
	}

	asset.ID = id
	asset.Name = strings.TrimSpace(rowString(row, "name"))
	asset.Remark = rowString(row, "remark")
	asset.Source = Source(strings.ToLower(strings.TrimSpace(rowString(row, "source"))))
	asset.AssetType = AssetType(strings.ToLower(strings.TrimSpace(rowString(row, "asset_type"))))
	asset.FormatType = FormatType(strings.ToLower(strings.TrimSpace(rowString(row, "format_type"))))
	asset.CRT = rowString(row, "crt")
	asset.CRTHash = strings.ToLower(strings.TrimSpace(rowString(row, "crt_hash")))
	asset.Key = rowString(row, "key")
	asset.CreatedAt = rowTime(row, "created_at")
	asset.UpdatedAt = rowTime(row, "updated_at")

	issuerAssetID, err := rowNullableInt64(row, "issuer_asset_id")
	if err != nil {
		return Asset{}, fmt.Errorf("issuer_asset_id: %w", err)
	}
	asset.IssuerAssetID = issuerAssetID

	return asset, nil
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
	case time.Time:
		return typed.UTC().Format(schemaTimestampLayout)
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

func rowNullableInt64(row storage.Row, key string) (*int64, error) {
	value, ok := rowValue(row, key)
	if !ok || value == nil {
		return nil, nil
	}

	switch typed := value.(type) {
	case string:
		if strings.TrimSpace(typed) == "" {
			return nil, nil
		}
	case []byte:
		if strings.TrimSpace(string(typed)) == "" {
			return nil, nil
		}
	}

	number, err := rowInt64(row, key)
	if err != nil {
		return nil, err
	}
	return &number, nil
}

func rowTime(row storage.Row, key string) time.Time {
	value, ok := rowValue(row, key)
	if !ok || value == nil {
		return time.Time{}
	}

	switch typed := value.(type) {
	case time.Time:
		return typed.UTC()
	case string:
		return parseTimestamp(typed)
	case []byte:
		return parseTimestamp(string(typed))
	default:
		return parseTimestamp(fmt.Sprint(typed))
	}
}

func parseTimestamp(value string) time.Time {
	value = strings.TrimSpace(value)
	if value == "" {
		return time.Time{}
	}

	for _, layout := range []string{
		schemaTimestampLayout,
		time.RFC3339Nano,
		"2006-01-02 15:04:05",
	} {
		if parsed, err := time.Parse(layout, value); err == nil {
			return parsed.UTC()
		}
	}
	return time.Time{}
}

func rowValue(row storage.Row, key string) (any, bool) {
	if value, ok := row[key]; ok {
		return value, true
	}

	for candidateKey, candidateValue := range row {
		if strings.EqualFold(candidateKey, key) {
			return candidateValue, true
		}
	}
	return nil, false
}
