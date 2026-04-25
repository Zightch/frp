package control

import (
	"context"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/zightch/frp/frps/internal/proxygroups"
	"github.com/zightch/frp/frps/internal/storage"
	"github.com/zightch/frp/frps/internal/system"
	"github.com/zightch/frp/frps/pkg/protocol"
)

const schemaTimestampLayout = "2006-01-02 15:04:05.000000"

var ErrGroupNotFound = errors.New("proxy group not found")

type Repository interface {
	LoadGroupRuntimeByClientID(ctx context.Context, clientID [16]byte) (GroupRuntime, error)
	LoadGroupRuntimeByID(ctx context.Context, groupID int64) (GroupRuntime, error)
	ListGroupRuntimes(ctx context.Context) ([]GroupRuntime, error)
}

type SQLRepository struct {
	store *storage.SQL
}

type GroupRuntime struct {
	ID                       int64
	Name                     string
	Enabled                  bool
	EffectiveIP              string
	ControlTransportSecurity proxygroups.ControlTransportSecurity
	ClientSecretHash         [32]byte
	Snapshot                 ConfigSnapshot
}

type ConfigSnapshot struct {
	Version       uint64
	GeneratedAtMs uint64
	Tunnels       []protocol.TunnelEntry
}

func NewRepository(store *storage.SQL) *SQLRepository {
	return &SQLRepository{store: store}
}

func (r *SQLRepository) LoadGroupRuntimeByClientID(ctx context.Context, clientID [16]byte) (GroupRuntime, error) {
	return r.loadGroupRuntime(
		ctx,
		`
SELECT
	id,
	name,
	client_secret_hash,
	effective_ip,
	enabled,
	control_transport_security,
	updated_at
FROM proxy_groups
WHERE client_id = ?
`,
		hex.EncodeToString(clientID[:]),
	)
}

func (r *SQLRepository) LoadGroupRuntimeByID(ctx context.Context, groupID int64) (GroupRuntime, error) {
	return r.loadGroupRuntime(
		ctx,
		`
SELECT
	id,
	name,
	client_secret_hash,
	effective_ip,
	enabled,
	control_transport_security,
	updated_at
FROM proxy_groups
WHERE id = ?
`,
		groupID,
	)
}

func (r *SQLRepository) ListGroupRuntimes(ctx context.Context) ([]GroupRuntime, error) {
	if r == nil || r.store == nil {
		return nil, fmt.Errorf("repository store is nil")
	}

	result, err := r.store.QueryContext(
		ctx,
		`
SELECT
	id,
	name,
	client_secret_hash,
	effective_ip,
	enabled,
	control_transport_security,
	updated_at
FROM proxy_groups
ORDER BY id
`,
	)
	if err != nil {
		return nil, fmt.Errorf("list proxy groups: %w", err)
	}

	groups := make([]GroupRuntime, 0, len(result.Rows))
	for _, row := range result.Rows {
		group, err := r.decodeGroupRuntimeRow(ctx, row)
		if err != nil {
			return nil, err
		}
		groups = append(groups, group)
	}

	return groups, nil
}

func (r *SQLRepository) loadGroupRuntime(ctx context.Context, query string, arg any) (GroupRuntime, error) {
	if r == nil || r.store == nil {
		return GroupRuntime{}, fmt.Errorf("repository store is nil")
	}

	row, err := r.store.QueryOneContext(
		ctx,
		query,
		arg,
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return GroupRuntime{}, ErrGroupNotFound
		}
		return GroupRuntime{}, fmt.Errorf("load proxy group: %w", err)
	}

	return r.decodeGroupRuntimeRow(ctx, row)
}

func (r *SQLRepository) decodeGroupRuntimeRow(ctx context.Context, row storage.Row) (GroupRuntime, error) {
	group := GroupRuntime{
		Name: strings.TrimSpace(rowString(row, "name")),
	}

	var err error
	if group.ID, err = rowInt64(row, "id"); err != nil {
		return GroupRuntime{}, fmt.Errorf("decode group id: %w", err)
	}
	if group.Enabled, err = rowBool(row, "enabled"); err != nil {
		return GroupRuntime{}, fmt.Errorf("decode group enabled: %w", err)
	}
	group.EffectiveIP = decodeStoredEffectiveIP(rowString(row, "effective_ip"))
	group.ControlTransportSecurity = proxygroups.NormalizeControlTransportSecurity(rowString(row, "control_transport_security"))
	if group.ControlTransportSecurity == "" {
		group.ControlTransportSecurity = proxygroups.DefaultControlTransportSecurity()
	}
	if group.ClientSecretHash, err = decodeHex32(rowString(row, "client_secret_hash")); err != nil {
		return GroupRuntime{}, fmt.Errorf("decode group client secret hash: %w", err)
	}

	groupUpdatedAt := rowTime(row, "updated_at")
	tunnels, latestUpdatedAt, err := r.loadTunnels(ctx, group.ID)
	if err != nil {
		return GroupRuntime{}, err
	}

	generatedAt := latestTime(groupUpdatedAt, latestUpdatedAt)
	if generatedAt.IsZero() {
		generatedAt = time.Now().UTC()
	}

	group.Snapshot = ConfigSnapshot{
		Version:       configVersion(generatedAt),
		GeneratedAtMs: unixMillis(generatedAt),
		Tunnels:       tunnels,
	}
	return group, nil
}

func (r *SQLRepository) loadTunnels(ctx context.Context, groupID int64) ([]protocol.TunnelEntry, time.Time, error) {
	result, err := r.store.QueryContext(
		ctx,
		`
SELECT
	id,
	protocol,
	remote_type,
	remote_start,
	remote_end,
	local_host,
	local_start,
	local_end,
	enabled,
	updated_at
FROM tunnels
WHERE group_id = ?
ORDER BY id
`,
		groupID,
	)
	if err != nil {
		return nil, time.Time{}, fmt.Errorf("load group tunnels: %w", err)
	}

	tunnels := make([]protocol.TunnelEntry, 0, len(result.Rows))
	var latestUpdatedAt time.Time
	for _, row := range result.Rows {
		tunnel, err := decodeTunnelRow(row)
		if err != nil {
			return nil, time.Time{}, fmt.Errorf("decode group tunnel: %w", err)
		}
		tunnels = append(tunnels, tunnel)
		latestUpdatedAt = latestTime(latestUpdatedAt, rowTime(row, "updated_at"))
	}

	return tunnels, latestUpdatedAt, nil
}

func decodeTunnelRow(row storage.Row) (protocol.TunnelEntry, error) {
	var tunnel protocol.TunnelEntry

	id, err := rowInt64(row, "id")
	if err != nil {
		return tunnel, fmt.Errorf("id: %w", err)
	}
	if id <= 0 || id > math.MaxUint32 {
		return tunnel, fmt.Errorf("id %d is out of wire range", id)
	}

	remoteStart, err := rowInt64(row, "remote_start")
	if err != nil {
		return tunnel, fmt.Errorf("remote_start: %w", err)
	}
	remoteEnd, err := rowInt64(row, "remote_end")
	if err != nil {
		return tunnel, fmt.Errorf("remote_end: %w", err)
	}
	localStart, err := rowInt64(row, "local_start")
	if err != nil {
		return tunnel, fmt.Errorf("local_start: %w", err)
	}
	localEnd, err := rowInt64(row, "local_end")
	if err != nil {
		return tunnel, fmt.Errorf("local_end: %w", err)
	}
	enabled, err := rowBool(row, "enabled")
	if err != nil {
		return tunnel, fmt.Errorf("enabled: %w", err)
	}

	proto, err := decodeTunnelProtocol(rowString(row, "protocol"))
	if err != nil {
		return tunnel, err
	}
	flags, err := decodeTunnelFlags(
		strings.TrimSpace(rowString(row, "remote_type")),
		enabled,
		remoteStart,
		remoteEnd,
		localStart,
		localEnd,
	)
	if err != nil {
		return tunnel, err
	}
	host, err := protocol.ParseHost(rowString(row, "local_host"))
	if err != nil {
		return tunnel, fmt.Errorf("local_host: %w", err)
	}

	tunnel = protocol.TunnelEntry{
		TunnelID:    uint32(id),
		Protocol:    proto,
		TunnelFlags: flags,
		RemoteStart: uint16(remoteStart),
		RemoteEnd:   uint16(remoteEnd),
		LocalHost:   host,
		LocalStart:  uint16(localStart),
		LocalEnd:    uint16(localEnd),
	}

	return tunnel, nil
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
