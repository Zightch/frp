package certassets

import (
	"context"
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

	result, err := r.store.QueryContext(
		ctx,
		`
SELECT
	id,
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
FROM certificate_assets
ORDER BY id
`,
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
