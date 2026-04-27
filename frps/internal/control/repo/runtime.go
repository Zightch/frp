package repo

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/zightch/frp/frps/internal/proxygroups"
	"github.com/zightch/frp/frps/internal/settings/entrycerts"
	"github.com/zightch/frp/frps/internal/storage"
	"github.com/zightch/frp/frps/pkg/protocol"
)

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

	row, err := r.store.QueryOneContext(ctx, query, arg)
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
	backend_tls_mode,
	backend_tls_server_name,
	backend_tls_load_system_ca,
	backend_tls_insecure_skip_verify,
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

	usageMap, err := r.loadTunnelUsageMap(ctx)
	if err != nil {
		return nil, time.Time{}, err
	}

	tunnels := make([]protocol.TunnelEntry, 0, len(result.Rows))
	var latestUpdatedAt time.Time
	for _, row := range result.Rows {
		tunnel, err := r.decodeTunnelRow(ctx, row, usageMap)
		if err != nil {
			return nil, time.Time{}, fmt.Errorf("decode group tunnel: %w", err)
		}
		tunnels = append(tunnels, tunnel)
		latestUpdatedAt = latestTime(latestUpdatedAt, rowTime(row, "updated_at"))
	}

	return tunnels, latestUpdatedAt, nil
}

func (r *SQLRepository) decodeTunnelRow(ctx context.Context, row storage.Row, usageMap map[int64]map[entrycerts.UsageType][]int64) (protocol.TunnelEntry, error) {
	var tunnel protocol.TunnelEntry

	id, err := rowInt64(row, "id")
	if err != nil {
		return tunnel, fmt.Errorf("id: %w", err)
	}
	if id <= 0 || id > 1<<32-1 {
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
	backendMode, err := decodeTunnelTLSMode(rowString(row, "backend_tls_mode"))
	if err != nil {
		return tunnel, fmt.Errorf("backend_tls_mode: %w", err)
	}
	backendLoadSystemCA, err := rowBool(row, "backend_tls_load_system_ca")
	if err != nil {
		return tunnel, fmt.Errorf("backend_tls_load_system_ca: %w", err)
	}
	backendInsecureSkipVerify, err := rowBool(row, "backend_tls_insecure_skip_verify")
	if err != nil {
		return tunnel, fmt.Errorf("backend_tls_insecure_skip_verify: %w", err)
	}

	var (
		backendCAPEM         string
		backendClientCertPEM string
		backendClientKeyPEM  string
	)
	if backendMode != protocol.TunnelTLSModeOff {
		backendUsages := usageMap[id]
		if assetIDs := backendUsages[entrycerts.UsageTypeTunnelBackendCA]; len(assetIDs) > 0 {
			if r.entryCerts == nil {
				return tunnel, fmt.Errorf("entry certificate service is unavailable")
			}
			pool, err := r.entryCerts.ResolveCAPoolAssets(ctx, assetIDs)
			if err != nil {
				return tunnel, fmt.Errorf("resolve tunnel backend ca pool: %w", err)
			}
			backendCAPEM = pool.PEM
		}
		if assetIDs := backendUsages[entrycerts.UsageTypeTunnelBackendClientCert]; len(assetIDs) > 0 {
			if r.entryCerts == nil {
				return tunnel, fmt.Errorf("entry certificate service is unavailable")
			}
			binding, err := r.entryCerts.ResolveCertificateAsset(ctx, assetIDs[0])
			if err != nil {
				return tunnel, fmt.Errorf("resolve tunnel backend client certificate: %w", err)
			}
			backendClientCertPEM = binding.CertificatePEM
			backendClientKeyPEM = binding.KeyPEM
		}
	}

	tunnel = protocol.TunnelEntry{
		TunnelID:                     uint32(id),
		Protocol:                     proto,
		TunnelFlags:                  flags,
		RemoteStart:                  uint16(remoteStart),
		RemoteEnd:                    uint16(remoteEnd),
		LocalHost:                    host,
		LocalStart:                   uint16(localStart),
		LocalEnd:                     uint16(localEnd),
		Revision:                     configVersion(rowTime(row, "updated_at")),
		BackendTLSMode:               backendMode,
		BackendTLSLoadSystemCA:       backendLoadSystemCA,
		BackendTLSInsecureSkipVerify: backendInsecureSkipVerify,
		BackendTLSServerName:         strings.TrimSpace(rowString(row, "backend_tls_server_name")),
		BackendTLSCAPEM:              backendCAPEM,
		BackendTLSClientCertPEM:      backendClientCertPEM,
		BackendTLSClientKeyPEM:       backendClientKeyPEM,
	}

	return tunnel, nil
}

func (r *SQLRepository) loadTunnelUsageMap(ctx context.Context) (map[int64]map[entrycerts.UsageType][]int64, error) {
	if r == nil || r.store == nil {
		return nil, fmt.Errorf("repository store is nil")
	}
	usages, err := entrycerts.ListUsagesWithConn(ctx, r.store)
	if err != nil {
		return nil, fmt.Errorf("load tunnel certificate bindings: %w", err)
	}
	result := make(map[int64]map[entrycerts.UsageType][]int64)
	for _, usage := range usages {
		if usage.TargetType != entrycerts.TargetTypeTunnel || !usage.Enabled {
			continue
		}
		byUsage, ok := result[usage.TargetID]
		if !ok {
			byUsage = make(map[entrycerts.UsageType][]int64)
			result[usage.TargetID] = byUsage
		}
		byUsage[usage.UsageType] = append(byUsage[usage.UsageType], usage.AssetID)
	}
	return result, nil
}
