package ratepolicy

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/zightch/frp/frps/internal/api/httpx"
	"github.com/zightch/frp/frps/internal/storage"
)

func decodeRatePolicyRow(row storage.Row) (RatePolicyView, error) {
	var item RatePolicyView
	var err error

	if item.ID, err = rowInt64(row, "id"); err != nil {
		return RatePolicyView{}, err
	}
	if item.DownlinkBPS, err = rowInt64(row, "downlink_bps"); err != nil {
		return RatePolicyView{}, err
	}
	if item.UplinkBPS, err = rowInt64(row, "uplink_bps"); err != nil {
		return RatePolicyView{}, err
	}
	if item.BindingCount, err = rowInt64(row, "binding_count"); err != nil {
		return RatePolicyView{}, err
	}

	item.Name = rowString(row, "name")
	item.Mode = rowString(row, "mode")
	item.CreatedAt = rowString(row, "created_at")
	item.UpdatedAt = rowString(row, "updated_at")
	return item, nil
}

func decodeRatePolicyBindingRow(row storage.Row) (RatePolicyBindingView, error) {
	var item RatePolicyBindingView
	var err error

	if item.ID, err = rowInt64(row, "id"); err != nil {
		return RatePolicyBindingView{}, err
	}
	if item.RatePolicyID, err = rowInt64(row, "rate_policy_id"); err != nil {
		return RatePolicyBindingView{}, err
	}
	if item.TunnelID, err = rowInt64(row, "tunnel_id"); err != nil {
		return RatePolicyBindingView{}, err
	}
	if item.GroupID, err = rowInt64(row, "group_id"); err != nil {
		return RatePolicyBindingView{}, err
	}
	if item.RemoteStart, err = rowInt64(row, "remote_start"); err != nil {
		return RatePolicyBindingView{}, err
	}
	if item.RemoteEnd, err = rowInt64(row, "remote_end"); err != nil {
		return RatePolicyBindingView{}, err
	}

	item.GroupName = rowString(row, "group_name")
	item.TunnelName = rowString(row, "tunnel_name")
	item.Protocol = rowString(row, "protocol")
	item.RemoteType = rowString(row, "remote_type")
	item.CreatedAt = rowString(row, "created_at")
	item.UpdatedAt = rowString(row, "updated_at")
	return item, nil
}

func rowString(row storage.Row, key string) string {
	value, ok := row[key]
	if !ok || value == nil {
		return ""
	}
	switch typed := value.(type) {
	case string:
		return typed
	case []byte:
		return string(typed)
	default:
		return fmt.Sprintf("%v", typed)
	}
}

func rowInt64(row storage.Row, key string) (int64, error) {
	value, ok := row[key]
	if !ok || value == nil {
		return 0, fmt.Errorf("missing integer field %s", key)
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
	case float32:
		return int64(typed), nil
	case float64:
		return int64(typed), nil
	case []byte:
		parsed, err := strconv.ParseInt(string(typed), 10, 64)
		if err != nil {
			return 0, fmt.Errorf("parse integer field %s: %w", key, err)
		}
		return parsed, nil
	case string:
		parsed, err := strconv.ParseInt(strings.TrimSpace(typed), 10, 64)
		if err != nil {
			return 0, fmt.Errorf("parse integer field %s: %w", key, err)
		}
		return parsed, nil
	default:
		return 0, fmt.Errorf("unexpected integer field %s type %T", key, value)
	}
}

func schemaTimestamp() string {
	return time.Now().UTC().Format(SchemaTimestampLayout)
}

func wrapUniqueConstraintError(err error, message string) error {
	if err == nil {
		return nil
	}
	if httpx.IsUniqueConstraintError(err) {
		return &Error{Status: 409, Message: message}
	}
	return err
}
