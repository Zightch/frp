package repo

import (
	"encoding/hex"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/zightch/frp/frps/internal/settings/entrycerts"
	"github.com/zightch/frp/frps/internal/storage"
	"github.com/zightch/frp/frps/internal/system"
	"github.com/zightch/frp/frps/pkg/protocol"
)

const schemaTimestampLayout = "2006-01-02 15:04:05.000000"

type SQLRepository struct {
	store      *storage.SQL
	entryCerts *entrycerts.Service
}

func NewSQLRepository(store *storage.SQL) *SQLRepository {
	return &SQLRepository{
		store:      store,
		entryCerts: entrycerts.NewService(store, entrycerts.ServiceOptions{}),
	}
}

func decodeTunnelProtocol(value string) (uint8, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "tcp":
		return protocol.ProtocolTCP, nil
	case "udp":
		return protocol.ProtocolUDP, nil
	default:
		return 0, fmt.Errorf("unsupported tunnel protocol %q", value)
	}
}

func decodeTunnelTLSMode(value string) (uint8, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", "off":
		return protocol.TunnelTLSModeOff, nil
	case "tls":
		return protocol.TunnelTLSModeTLS, nil
	case "mtls":
		return protocol.TunnelTLSModeMTLS, nil
	default:
		return 0, fmt.Errorf("unsupported tunnel tls mode %q", value)
	}
}

func decodeTunnelFlags(remoteType string, enabled bool, remoteStart, remoteEnd, localStart, localEnd int64) (uint8, error) {
	if !validPort(remoteStart) || !validPort(remoteEnd) || !validPort(localStart) || !validPort(localEnd) {
		return 0, fmt.Errorf("tunnel ports must be between 1 and 65535")
	}
	if remoteEnd < remoteStart || localEnd < localStart {
		return 0, fmt.Errorf("tunnel range end must be greater than or equal to start")
	}

	var flags uint8
	if enabled {
		flags |= protocol.TunnelFlagEnabled
	}

	switch strings.ToLower(strings.TrimSpace(remoteType)) {
	case "single":
		if remoteStart != remoteEnd || localStart != localEnd {
			return 0, fmt.Errorf("single tunnel must use identical start and end ports")
		}
	case "range":
		if (remoteEnd - remoteStart) != (localEnd - localStart) {
			return 0, fmt.Errorf("remote and local port ranges must be aligned")
		}
		flags |= protocol.TunnelFlagRange
	default:
		return 0, fmt.Errorf("unsupported remote_type %q", remoteType)
	}

	return flags, nil
}

func validPort(value int64) bool {
	return value >= 1 && value <= math.MaxUint16
}

func decodeStoredEffectiveIP(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}

	normalized, err := system.NormalizeListenIP(value)
	if err != nil {
		return value
	}
	return normalized
}

func decodeHex32(value string) ([32]byte, error) {
	var decoded [32]byte
	raw, err := hex.DecodeString(strings.TrimSpace(value))
	if err != nil {
		return decoded, err
	}
	if len(raw) != len(decoded) {
		return decoded, fmt.Errorf("expected 32 bytes, got %d", len(raw))
	}
	copy(decoded[:], raw)
	return decoded, nil
}

func configVersion(updatedAt time.Time) uint64 {
	if updatedAt.IsZero() {
		return 1
	}
	if updatedAt.UTC().UnixMicro() <= 0 {
		return 1
	}
	return uint64(updatedAt.UTC().UnixMicro())
}

func unixMillis(value time.Time) uint64 {
	if value.IsZero() || value.UTC().UnixMilli() < 0 {
		return 0
	}
	return uint64(value.UTC().UnixMilli())
}

func latestTime(left, right time.Time) time.Time {
	if left.Before(right) {
		return right
	}
	return left
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
