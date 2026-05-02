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
	t.id,
	t.name,
	t.protocol,
	t.remote_type,
	t.remote_start,
	t.remote_end,
	t.local_host,
	t.local_start,
	t.local_end,
	t.listen_tls_mode,
	t.backend_tls_mode,
	t.backend_tls_server_name,
	t.backend_tls_load_system_ca,
	t.backend_tls_insecure_skip_verify,
	t.enabled,
	t.updated_at,
	rpb.rate_policy_id AS rate_policy_id,
	rpb.updated_at AS rate_policy_binding_updated_at,
	rp.mode AS rate_policy_mode,
	rp.downlink_bps AS rate_policy_downlink_bps,
	rp.uplink_bps AS rate_policy_uplink_bps,
	rp.updated_at AS rate_policy_updated_at
FROM tunnels t
LEFT JOIN rate_policy_bindings rpb ON rpb.tunnel_id = t.id
LEFT JOIN rate_policies rp ON rp.id = rpb.rate_policy_id
WHERE t.group_id = ?
ORDER BY t.id
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
		tunnelUpdatedAt := rowTime(row, "updated_at")
		latestUpdatedAt = latestTime(latestUpdatedAt, tunnelUpdatedAt)
		latestUpdatedAt = latestTime(latestUpdatedAt, rowTime(row, "rate_policy_binding_updated_at"))
		latestUpdatedAt = latestTime(latestUpdatedAt, rowTime(row, "rate_policy_updated_at"))
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
	listenMode, err := decodeTunnelTLSMode(rowString(row, "listen_tls_mode"))
	if err != nil {
		return tunnel, fmt.Errorf("listen_tls_mode: %w", err)
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

	ratePolicy, err := decodeTunnelRatePolicyRow(row)
	if err != nil {
		return tunnel, fmt.Errorf("rate policy: %w", err)
	}

	revisionUpdatedAt := latestTime(rowTime(row, "updated_at"), rowTime(row, "rate_policy_binding_updated_at"))
	revisionUpdatedAt = latestTime(revisionUpdatedAt, rowTime(row, "rate_policy_updated_at"))

	tunnel = protocol.TunnelEntry{
		TunnelName:                   strings.TrimSpace(rowString(row, "name")),
		TunnelID:                     uint32(id),
		Protocol:                     proto,
		TunnelFlags:                  flags,
		ListenTLSMode:                listenMode,
		RemoteStart:                  uint16(remoteStart),
		RemoteEnd:                    uint16(remoteEnd),
		LocalHost:                    host,
		LocalStart:                   uint16(localStart),
		LocalEnd:                     uint16(localEnd),
		Revision:                     configVersion(revisionUpdatedAt),
		BackendTLSMode:               backendMode,
		BackendTLSLoadSystemCA:       backendLoadSystemCA,
		BackendTLSInsecureSkipVerify: backendInsecureSkipVerify,
		BackendTLSServerName:         strings.TrimSpace(rowString(row, "backend_tls_server_name")),
		BackendTLSCAPEM:              backendCAPEM,
		BackendTLSClientCertPEM:      backendClientCertPEM,
		BackendTLSClientKeyPEM:       backendClientKeyPEM,
		RatePolicy:                   ratePolicy,
	}

	return tunnel, nil
}

func decodeTunnelRatePolicyRow(row storage.Row) (protocol.TunnelRatePolicy, error) {
	var policy protocol.TunnelRatePolicy

	policyID, err := rowInt64(row, "rate_policy_id")
	if err != nil {
		return policy, fmt.Errorf("rate_policy_id: %w", err)
	}
	if policyID == 0 {
		return policy, nil
	}
	if policyID < 0 || policyID > 1<<32-1 {
		return policy, fmt.Errorf("rate_policy_id %d is out of wire range", policyID)
	}

	mode, err := decodeTunnelRatePolicyMode(rowString(row, "rate_policy_mode"))
	if err != nil {
		return policy, fmt.Errorf("rate_policy_mode: %w", err)
	}
	downlinkBPS, err := rowInt64(row, "rate_policy_downlink_bps")
	if err != nil {
		return policy, fmt.Errorf("rate_policy_downlink_bps: %w", err)
	}
	uplinkBPS, err := rowInt64(row, "rate_policy_uplink_bps")
	if err != nil {
		return policy, fmt.Errorf("rate_policy_uplink_bps: %w", err)
	}
	if downlinkBPS <= 0 || uplinkBPS <= 0 {
		return policy, fmt.Errorf("bound rate policy must have positive downlink and uplink bps")
	}

	policy = protocol.TunnelRatePolicy{
		PolicyID:    uint32(policyID),
		Mode:        mode,
		DownlinkBPS: uint64(downlinkBPS),
		UplinkBPS:   uint64(uplinkBPS),
	}
	return policy, nil
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
