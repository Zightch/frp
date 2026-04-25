package certusages

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

func ListUsagesWithConn(ctx context.Context, conn storage.Conn) ([]Usage, error) {
	if conn == nil {
		return nil, fmt.Errorf("usage connection is nil")
	}

	result, err := conn.QueryContext(
		ctx,
		`
SELECT
	id,
	usage_type,
	target_id,
	asset_id,
	enabled,
	created_at,
	updated_at
FROM certificate_asset_usages
ORDER BY usage_type
`,
	)
	if err != nil {
		return nil, fmt.Errorf("list certificate usages: %w", err)
	}

	items := make([]Usage, 0, len(result.Rows))
	for _, row := range result.Rows {
		item, err := decodeUsageRow(row)
		if err != nil {
			return nil, fmt.Errorf("decode certificate usage: %w", err)
		}
		items = append(items, item)
	}
	return items, nil
}

func LoadUsageByType(ctx context.Context, conn storage.Conn, usageType UsageType) (Usage, error) {
	if conn == nil {
		return Usage{}, fmt.Errorf("usage connection is nil")
	}

	row, err := conn.QueryOneContext(
		ctx,
		`
SELECT
	id,
	usage_type,
	target_id,
	asset_id,
	enabled,
	created_at,
	updated_at
FROM certificate_asset_usages
WHERE usage_type = ? AND target_id = ?
`,
		string(usageType),
		globalTargetID,
	)
	if err != nil {
		if err == sql.ErrNoRows {
			return Usage{}, err
		}
		return Usage{}, fmt.Errorf("load certificate usage: %w", err)
	}

	item, err := decodeUsageRow(row)
	if err != nil {
		return Usage{}, fmt.Errorf("decode certificate usage: %w", err)
	}
	return item, nil
}

func UpsertUsage(ctx context.Context, conn storage.Conn, usageType UsageType, assetID int64, enabled bool, now time.Time) (Usage, error) {
	if conn == nil {
		return Usage{}, fmt.Errorf("usage connection is nil")
	}

	nowText := formatTimestamp(now)
	current, err := LoadUsageByType(ctx, conn, usageType)
	switch {
	case err == nil:
		if _, err := conn.ExecContext(
			ctx,
			`
UPDATE certificate_asset_usages
SET asset_id = ?, enabled = ?, updated_at = ?
WHERE id = ?
`,
			assetID,
			boolToInt(enabled),
			nowText,
			current.ID,
		); err != nil {
			return Usage{}, fmt.Errorf("update certificate usage: %w", err)
		}
	case err == sql.ErrNoRows:
		result, execErr := conn.ExecContext(
			ctx,
			`
INSERT INTO certificate_asset_usages (
	usage_type,
	target_id,
	asset_id,
	enabled,
	created_at,
	updated_at
) VALUES (?, ?, ?, ?, ?, ?)
`,
			string(usageType),
			globalTargetID,
			assetID,
			boolToInt(enabled),
			nowText,
			nowText,
		)
		if execErr != nil {
			return Usage{}, fmt.Errorf("insert certificate usage: %w", execErr)
		}
		return Usage{
			ID:        result.LastInsertID,
			UsageType: usageType,
			TargetID:  globalTargetID,
			AssetID:   assetID,
			Enabled:   enabled,
			CreatedAt: now.UTC(),
			UpdatedAt: now.UTC(),
		}, nil
	default:
		return Usage{}, err
	}

	return LoadUsageByType(ctx, conn, usageType)
}

func DeleteUsageByType(ctx context.Context, conn storage.Conn, usageType UsageType) error {
	if conn == nil {
		return fmt.Errorf("usage connection is nil")
	}

	if _, err := conn.ExecContext(
		ctx,
		`DELETE FROM certificate_asset_usages WHERE usage_type = ? AND target_id = ?`,
		string(usageType),
		globalTargetID,
	); err != nil {
		return fmt.Errorf("delete certificate usage: %w", err)
	}
	return nil
}

func formatTimestamp(value time.Time) string {
	if value.IsZero() {
		value = time.Now().UTC()
	}
	return value.UTC().Format(schemaTimestampLayout)
}

func decodeUsageRow(row storage.Row) (Usage, error) {
	var item Usage
	var err error

	if item.ID, err = rowInt64(row, "id"); err != nil {
		return Usage{}, fmt.Errorf("id: %w", err)
	}
	item.UsageType = UsageType(strings.ToLower(strings.TrimSpace(rowString(row, "usage_type"))))
	if item.TargetID, err = rowInt64(row, "target_id"); err != nil {
		return Usage{}, fmt.Errorf("target_id: %w", err)
	}
	if item.AssetID, err = rowInt64(row, "asset_id"); err != nil {
		return Usage{}, fmt.Errorf("asset_id: %w", err)
	}
	if item.Enabled, err = rowBool(row, "enabled"); err != nil {
		return Usage{}, fmt.Errorf("enabled: %w", err)
	}
	item.CreatedAt = rowTime(row, "created_at")
	item.UpdatedAt = rowTime(row, "updated_at")
	return item, nil
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

func rowBool(row storage.Row, key string) (bool, error) {
	value, err := rowInt64(row, key)
	if err == nil {
		return value != 0, nil
	}

	switch strings.ToLower(strings.TrimSpace(rowString(row, key))) {
	case "", "0", "false", "no":
		return false, nil
	case "1", "true", "yes":
		return true, nil
	default:
		return false, err
	}
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

func boolToInt(value bool) int {
	if value {
		return 1
	}
	return 0
}
