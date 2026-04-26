package groupconfig

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/zightch/frp/frps/internal/proxygroups"
	"github.com/zightch/frp/frps/internal/storage"
	"github.com/zightch/frp/frps/internal/system"
)

func decodeProxyGroupRow(row storage.Row) (ProxyGroupView, error) {
	var item ProxyGroupView
	var err error

	if item.ID, err = rowInt64(row, "id"); err != nil {
		return item, fmt.Errorf("id: %w", err)
	}
	if item.Enabled, err = rowBool(row, "enabled"); err != nil {
		return item, fmt.Errorf("enabled: %w", err)
	}
	if item.EffectiveIP, err = system.NormalizeListenIP(rowString(row, "effective_ip")); err != nil {
		return item, fmt.Errorf("effective_ip: %w", err)
	}
	item.Name = rowString(row, "name")
	item.ClientID = rowString(row, "client_id")
	item.ControlTransportSecurity = string(proxygroups.NormalizeControlTransportSecurity(rowString(row, "control_transport_security")))
	if item.ControlTransportSecurity == "" {
		item.ControlTransportSecurity = string(proxygroups.DefaultControlTransportSecurity())
	}
	item.CreatedAt = rowTimeString(row, "created_at")
	item.UpdatedAt = rowTimeString(row, "updated_at")
	return item, nil
}

func decodeTunnelRow(row storage.Row) (TunnelView, error) {
	var item TunnelView
	var err error

	if item.ID, err = rowInt64(row, "id"); err != nil {
		return item, fmt.Errorf("id: %w", err)
	}
	if item.GroupID, err = rowInt64(row, "group_id"); err != nil {
		return item, fmt.Errorf("group_id: %w", err)
	}
	if item.RemoteStart, err = rowInt64(row, "remote_start"); err != nil {
		return item, fmt.Errorf("remote_start: %w", err)
	}
	if item.RemoteEnd, err = rowInt64(row, "remote_end"); err != nil {
		return item, fmt.Errorf("remote_end: %w", err)
	}
	if item.LocalStart, err = rowInt64(row, "local_start"); err != nil {
		return item, fmt.Errorf("local_start: %w", err)
	}
	if item.LocalEnd, err = rowInt64(row, "local_end"); err != nil {
		return item, fmt.Errorf("local_end: %w", err)
	}
	if item.Enabled, err = rowBool(row, "enabled"); err != nil {
		return item, fmt.Errorf("enabled: %w", err)
	}
	if item.GroupEnabled, err = rowBool(row, "group_enabled"); err != nil {
		return item, fmt.Errorf("group_enabled: %w", err)
	}

	item.GroupName = rowString(row, "group_name")
	item.GroupEffectiveIP = rowString(row, "group_effective_ip")
	item.Name = rowString(row, "name")
	item.Protocol = rowString(row, "protocol")
	item.RemoteType = rowString(row, "remote_type")
	item.LocalHost = rowString(row, "local_host")
	item.CreatedAt = rowTimeString(row, "created_at")
	item.UpdatedAt = rowTimeString(row, "updated_at")
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

func rowTimeString(row storage.Row, key string) string {
	value, ok := rowValue(row, key)
	if !ok || value == nil {
		return ""
	}

	switch typed := value.(type) {
	case time.Time:
		return typed.UTC().Format(schemaTimestampLayout)
	case string:
		return strings.TrimSpace(typed)
	case []byte:
		return strings.TrimSpace(string(typed))
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

func schemaTimestamp() string {
	return time.Now().UTC().Format(schemaTimestampLayout)
}

func boolToInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

func validPort(value int64) bool {
	return value >= 1 && value <= math.MaxUint16
}
