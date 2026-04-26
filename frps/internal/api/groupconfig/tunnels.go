package groupconfig

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/zightch/frp/frps/internal/api/httpx"
	"github.com/zightch/frp/frps/internal/ports"
	"github.com/zightch/frp/frps/internal/settings/entrycerts"
	"github.com/zightch/frp/frps/internal/storage"
	"github.com/zightch/frp/frps/pkg/protocol"
)

func (s *Service) ListTunnels(ctx context.Context) ([]TunnelView, error) {
	items, err := s.loadTunnelsWithGroupState(ctx, s.store)
	if err != nil {
		return nil, err
	}
	return s.withTunnelStatuses(items), nil
}

func (s *Service) CreateTunnel(ctx context.Context, payload TunnelRequest) (TunnelMutationResult, error) {
	normalized, err := normalizeTunnel(payload)
	if err != nil {
		return TunnelMutationResult{}, err
	}
	if err := s.validateTunnelTLS(ctx, normalized); err != nil {
		return TunnelMutationResult{}, err
	}

	var (
		createdID int64
		group     ProxyGroupView
	)
	err = s.store.WithTxContext(ctx, nil, func(tx *storage.Tx) error {
		group, err = s.loadProxyGroupByID(ctx, tx, normalized.GroupID)
		if err != nil {
			return err
		}
		if err := s.ensureConflictFreeForTunnelCreate(ctx, tx, normalized, group); err != nil {
			return err
		}

		now := schemaTimestamp()
		result, err := tx.ExecContext(
			ctx,
			`
INSERT INTO tunnels (
	group_id,
	name,
	protocol,
	remote_type,
	remote_start,
	remote_end,
	local_host,
	local_start,
	local_end,
	listen_tls_mode,
	listen_tls_load_system_ca,
	backend_tls_mode,
	backend_tls_server_name,
	backend_tls_load_system_ca,
	backend_tls_insecure_skip_verify,
	enabled,
	created_at,
	updated_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
`,
			normalized.GroupID,
			normalized.Name,
			normalized.Protocol,
			normalized.RemoteType,
			normalized.RemoteStart,
			normalized.RemoteEnd,
			normalized.LocalHost,
			normalized.LocalStart,
			normalized.LocalEnd,
			string(normalized.ListenTLSMode),
			boolToInt(normalized.ListenTLSLoadSystemCA),
			string(normalized.BackendTLSMode),
			normalized.BackendTLSServerName,
			boolToInt(normalized.BackendTLSLoadSystemCA),
			boolToInt(normalized.BackendTLSInsecureSkipVerify),
			boolToInt(normalized.Enabled),
			now,
			now,
		)
		if err != nil {
			return wrapUniqueConstraintError(err, "tunnel name already exists in the selected proxy group")
		}

		createdID = result.LastInsertID
		if err := s.replaceTunnelCertificateUsages(ctx, tx, createdID, normalized); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		return TunnelMutationResult{}, err
	}

	item, err := s.loadTunnelByIDWithStatus(ctx, createdID)
	if err != nil {
		return TunnelMutationResult{}, err
	}

	s.refreshGroups(item.GroupID)
	return TunnelMutationResult{
		Item:     item,
		Warnings: tunnelMutationWarnings(group, normalized),
	}, nil
}

func (s *Service) UpdateTunnel(ctx context.Context, id int64, payload TunnelRequest) (TunnelMutationResult, error) {
	normalized, err := normalizeTunnel(payload)
	if err != nil {
		return TunnelMutationResult{}, err
	}
	if err := s.validateTunnelTLS(ctx, normalized); err != nil {
		return TunnelMutationResult{}, err
	}

	var (
		item        TunnelView
		previousGID int64
		group       ProxyGroupView
	)
	err = s.store.WithTxContext(ctx, nil, func(tx *storage.Tx) error {
		row, err := tx.QueryOneContext(ctx, "SELECT group_id FROM tunnels WHERE id = ?", id)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return &Error{Status: 404, Message: "tunnel not found"}
			}
			return fmt.Errorf("load tunnel before update: %w", err)
		}
		previousGID, err = rowInt64(row, "group_id")
		if err != nil {
			return fmt.Errorf("decode tunnel group_id: %w", err)
		}

		group, err = s.loadProxyGroupByID(ctx, tx, normalized.GroupID)
		if err != nil {
			return err
		}
		if err := s.ensureConflictFreeForTunnelUpdate(ctx, tx, id, normalized, group); err != nil {
			return err
		}

		result, err := tx.ExecContext(
			ctx,
			`
UPDATE tunnels
SET
	group_id = ?,
	name = ?,
	protocol = ?,
	remote_type = ?,
	remote_start = ?,
	remote_end = ?,
	local_host = ?,
	local_start = ?,
	local_end = ?,
	listen_tls_mode = ?,
	listen_tls_load_system_ca = ?,
	backend_tls_mode = ?,
	backend_tls_server_name = ?,
	backend_tls_load_system_ca = ?,
	backend_tls_insecure_skip_verify = ?,
	enabled = ?,
	updated_at = ?
WHERE id = ?
`,
			normalized.GroupID,
			normalized.Name,
			normalized.Protocol,
			normalized.RemoteType,
			normalized.RemoteStart,
			normalized.RemoteEnd,
			normalized.LocalHost,
			normalized.LocalStart,
			normalized.LocalEnd,
			string(normalized.ListenTLSMode),
			boolToInt(normalized.ListenTLSLoadSystemCA),
			string(normalized.BackendTLSMode),
			normalized.BackendTLSServerName,
			boolToInt(normalized.BackendTLSLoadSystemCA),
			boolToInt(normalized.BackendTLSInsecureSkipVerify),
			boolToInt(normalized.Enabled),
			schemaTimestamp(),
			id,
		)
		if err != nil {
			return wrapUniqueConstraintError(err, "tunnel name already exists in the selected proxy group")
		}
		if result.RowsAffected == 0 {
			return &Error{Status: 404, Message: "tunnel not found"}
		}
		if err := s.replaceTunnelCertificateUsages(ctx, tx, id, normalized); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		return TunnelMutationResult{}, err
	}

	item, err = s.loadTunnelByIDWithStatus(ctx, id)
	if err != nil {
		return TunnelMutationResult{}, err
	}

	s.refreshGroups(previousGID, item.GroupID)
	return TunnelMutationResult{
		Item:     item,
		Warnings: tunnelMutationWarnings(group, normalized),
	}, nil
}

func (s *Service) DeleteTunnel(ctx context.Context, id int64) error {
	var groupID int64
	err := s.store.WithTxContext(ctx, nil, func(tx *storage.Tx) error {
		row, err := tx.QueryOneContext(ctx, "SELECT group_id FROM tunnels WHERE id = ?", id)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return &Error{Status: 404, Message: "tunnel not found"}
			}
			return fmt.Errorf("load tunnel before delete: %w", err)
		}
		groupID, err = rowInt64(row, "group_id")
		if err != nil {
			return fmt.Errorf("decode tunnel group_id: %w", err)
		}

		if err := s.deleteTunnelCertificateUsages(ctx, tx, id); err != nil {
			return err
		}

		result, err := tx.ExecContext(ctx, "DELETE FROM tunnels WHERE id = ?", id)
		if err != nil {
			return fmt.Errorf("delete tunnel: %w", err)
		}
		if result.RowsAffected == 0 {
			return &Error{Status: 404, Message: "tunnel not found"}
		}
		return nil
	})
	if err != nil {
		return err
	}

	s.refreshGroups(groupID)
	return nil
}

func (s *Service) loadTunnelByIDWithStatus(ctx context.Context, id int64) (TunnelView, error) {
	items, err := s.loadTunnelsWithGroupState(ctx, s.store)
	if err != nil {
		return TunnelView{}, err
	}
	items = s.withTunnelStatuses(items)
	for _, item := range items {
		if item.ID == id {
			return item, nil
		}
	}
	return TunnelView{}, &Error{Status: 404, Message: "tunnel not found"}
}

func (s *Service) loadTunnelsWithGroupState(ctx context.Context, conn storage.Conn) ([]TunnelView, error) {
	result, err := conn.QueryContext(
		ctx,
		`
SELECT
	t.id,
	t.group_id,
	COALESCE(g.name, '') AS group_name,
	COALESCE(g.effective_ip, '') AS group_effective_ip,
	COALESCE(g.enabled, 0) AS group_enabled,
	t.name,
	t.protocol,
	t.remote_type,
	t.remote_start,
	t.remote_end,
	t.local_host,
	t.local_start,
	t.local_end,
	t.listen_tls_mode,
	t.listen_tls_load_system_ca,
	t.backend_tls_mode,
	t.backend_tls_server_name,
	t.backend_tls_load_system_ca,
	t.backend_tls_insecure_skip_verify,
	t.enabled,
	t.created_at,
	t.updated_at
FROM tunnels AS t
LEFT JOIN proxy_groups AS g ON g.id = t.group_id
ORDER BY t.id
`,
	)
	if err != nil {
		return nil, fmt.Errorf("list tunnels: %w", err)
	}
	usageMap, err := s.loadTunnelUsageMap(ctx, conn)
	if err != nil {
		return nil, err
	}

	items := make([]TunnelView, 0, len(result.Rows))
	for _, row := range result.Rows {
		item, err := decodeTunnelRow(row)
		if err != nil {
			return nil, fmt.Errorf("decode tunnel: %w", err)
		}
		s.applyTunnelUsageView(&item, usageMap[item.ID])
		items = append(items, item)
	}
	return items, nil
}

func (s *Service) ensureConflictFreeForTunnelCreate(ctx context.Context, conn storage.Conn, normalized normalizedTunnel, group ProxyGroupView) error {
	if !group.Enabled || !normalized.Enabled {
		return nil
	}

	items, err := s.loadTunnelsWithGroupState(ctx, conn)
	if err != nil {
		return err
	}
	items = append(items, TunnelView{
		ID:               -1,
		GroupID:          group.ID,
		GroupName:        group.Name,
		GroupEffectiveIP: group.EffectiveIP,
		GroupEnabled:     group.Enabled,
		Name:             normalized.Name,
		Protocol:         normalized.Protocol,
		RemoteType:       normalized.RemoteType,
		RemoteStart:      normalized.RemoteStart,
		RemoteEnd:        normalized.RemoteEnd,
		LocalHost:        normalized.LocalHost,
		LocalStart:       normalized.LocalStart,
		LocalEnd:         normalized.LocalEnd,
		Enabled:          normalized.Enabled,
	})
	return conflictErrorForTargets(items, map[int64]struct{}{-1: {}})
}

func (s *Service) ensureConflictFreeForTunnelUpdate(ctx context.Context, conn storage.Conn, tunnelID int64, normalized normalizedTunnel, group ProxyGroupView) error {
	items, err := s.loadTunnelsWithGroupState(ctx, conn)
	if err != nil {
		return err
	}

	for index := range items {
		if items[index].ID != tunnelID {
			continue
		}
		items[index].GroupID = group.ID
		items[index].GroupName = group.Name
		items[index].GroupEffectiveIP = group.EffectiveIP
		items[index].GroupEnabled = group.Enabled
		items[index].Name = normalized.Name
		items[index].Protocol = normalized.Protocol
		items[index].RemoteType = normalized.RemoteType
		items[index].RemoteStart = normalized.RemoteStart
		items[index].RemoteEnd = normalized.RemoteEnd
		items[index].LocalHost = normalized.LocalHost
		items[index].LocalStart = normalized.LocalStart
		items[index].LocalEnd = normalized.LocalEnd
		items[index].Enabled = normalized.Enabled
		return conflictErrorForTargets(items, map[int64]struct{}{tunnelID: {}})
	}

	return &Error{Status: 404, Message: "tunnel not found"}
}

func conflictErrorForTargets(items []TunnelView, targetIDs map[int64]struct{}) error {
	if len(targetIDs) == 0 {
		return nil
	}

	conflicts, itemsByID := detectTunnelConflicts(items)
	for tunnelID := range targetIDs {
		conflict, ok := conflicts[tunnelID]
		if !ok {
			continue
		}
		target := itemsByID[tunnelID]
		other := itemsByID[conflict.OtherOwnerID]
		return &Error{
			Status:  409,
			Message: buildTunnelConflictReason(target, other, conflict),
		}
	}
	return nil
}

func normalizeTunnel(payload TunnelRequest) (normalizedTunnel, error) {
	item := normalizedTunnel{
		GroupID:        payload.GroupID,
		Name:           strings.TrimSpace(payload.Name),
		Protocol:       strings.ToLower(strings.TrimSpace(payload.Protocol)),
		RemoteType:     strings.ToLower(strings.TrimSpace(payload.RemoteType)),
		RemoteStart:    payload.RemoteStart,
		RemoteEnd:      payload.RemoteEnd,
		LocalHost:      strings.TrimSpace(payload.LocalHost),
		LocalStart:     payload.LocalStart,
		LocalEnd:       payload.LocalEnd,
		Enabled:        true,
		ListenTLSMode:  tunnelTLSModeOff,
		BackendTLSMode: tunnelTLSModeOff,
	}
	if payload.Enabled != nil {
		item.Enabled = *payload.Enabled
	}
	if payload.ListenTLSMode != nil {
		item.ListenTLSMode = normalizeTunnelTLSMode(*payload.ListenTLSMode)
	}
	if payload.ListenTLSLoadSystemCA != nil {
		item.ListenTLSLoadSystemCA = *payload.ListenTLSLoadSystemCA
	}
	var err error
	if item.ListenTLSServerCertAssetID, err = normalizeOptionalAssetID(payload.ListenTLSServerCertAssetID, "listen_tls_server_cert_asset_id"); err != nil {
		return normalizedTunnel{}, err
	}
	if item.ListenTLSClientCAAssetIDs, err = normalizeAssetIDs(payload.ListenTLSClientCAAssetIDs, "listen_tls_client_ca_asset_ids"); err != nil {
		return normalizedTunnel{}, err
	}
	if payload.BackendTLSMode != nil {
		item.BackendTLSMode = normalizeTunnelTLSMode(*payload.BackendTLSMode)
	}
	if payload.BackendTLSServerName != nil {
		item.BackendTLSServerName = strings.TrimSpace(*payload.BackendTLSServerName)
	}
	item.BackendTLSLoadSystemCA = item.BackendTLSMode != tunnelTLSModeOff
	if payload.BackendTLSMode == nil {
		item.BackendTLSLoadSystemCA = false
	}
	if payload.BackendTLSLoadSystemCA != nil {
		item.BackendTLSLoadSystemCA = *payload.BackendTLSLoadSystemCA
	}
	if payload.BackendTLSInsecureSkipVerify != nil {
		item.BackendTLSInsecureSkipVerify = *payload.BackendTLSInsecureSkipVerify
	}
	if item.BackendTLSClientCertAssetID, err = normalizeOptionalAssetID(payload.BackendTLSClientCertAssetID, "backend_tls_client_cert_asset_id"); err != nil {
		return normalizedTunnel{}, err
	}
	if item.BackendTLSCAAssetIDs, err = normalizeAssetIDs(payload.BackendTLSCAAssetIDs, "backend_tls_ca_asset_ids"); err != nil {
		return normalizedTunnel{}, err
	}
	if item.GroupID <= 0 {
		return normalizedTunnel{}, &Error{Status: 400, Message: "group_id must be greater than zero"}
	}
	if item.Name == "" {
		return normalizedTunnel{}, &Error{Status: 400, Message: "name is required"}
	}
	switch item.Protocol {
	case "tcp", "udp":
	default:
		return normalizedTunnel{}, &Error{Status: 400, Message: "protocol must be tcp or udp"}
	}
	switch item.RemoteType {
	case "single", "range":
	default:
		return normalizedTunnel{}, &Error{Status: 400, Message: "remote_type must be single or range"}
	}
	if _, err := protocol.ParseHost(item.LocalHost); err != nil {
		return normalizedTunnel{}, &Error{Status: 400, Message: "local_host is invalid"}
	}
	if !validPort(item.RemoteStart) || !validPort(item.RemoteEnd) || !validPort(item.LocalStart) || !validPort(item.LocalEnd) {
		return normalizedTunnel{}, &Error{Status: 400, Message: "ports must be between 1 and 65535"}
	}
	if item.RemoteEnd < item.RemoteStart || item.LocalEnd < item.LocalStart {
		return normalizedTunnel{}, &Error{Status: 400, Message: "port range end must be greater than or equal to start"}
	}
	if item.RemoteType == "single" && (item.RemoteStart != item.RemoteEnd || item.LocalStart != item.LocalEnd) {
		return normalizedTunnel{}, &Error{Status: 400, Message: "single tunnel must use identical start and end ports"}
	}
	if item.RemoteType == "range" && (item.RemoteEnd-item.RemoteStart) != (item.LocalEnd-item.LocalStart) {
		return normalizedTunnel{}, &Error{Status: 400, Message: "remote and local port ranges must be aligned"}
	}
	if item.ListenTLSMode == "" {
		return normalizedTunnel{}, &Error{Status: 400, Message: "listen_tls_mode must be off, tls, or mtls"}
	}
	if item.BackendTLSMode == "" {
		return normalizedTunnel{}, &Error{Status: 400, Message: "backend_tls_mode must be off, tls, or mtls"}
	}
	if item.BackendTLSServerName != "" {
		if _, err := protocol.ParseHost(item.BackendTLSServerName); err != nil {
			return normalizedTunnel{}, &Error{Status: 400, Message: "backend_tls_server_name is invalid"}
		}
	}
	return item, nil
}

func (s *Service) replaceTunnelCertificateUsages(ctx context.Context, conn storage.Conn, tunnelID int64, tunnel normalizedTunnel) error {
	now := time.Now().UTC()
	if err := entrycerts.ReplaceTargetUsages(ctx, conn, entrycerts.TargetTypeTunnel, tunnelID, entrycerts.UsageTypeTunnelListenServerCert, optionalAssetIDSlice(tunnel.ListenTLSServerCertAssetID), true, now); err != nil {
		return fmt.Errorf("save tunnel listen server certificate binding: %w", err)
	}
	if err := entrycerts.ReplaceTargetUsages(ctx, conn, entrycerts.TargetTypeTunnel, tunnelID, entrycerts.UsageTypeTunnelListenClientCA, tunnel.ListenTLSClientCAAssetIDs, true, now); err != nil {
		return fmt.Errorf("save tunnel listen client ca bindings: %w", err)
	}
	if err := entrycerts.ReplaceTargetUsages(ctx, conn, entrycerts.TargetTypeTunnel, tunnelID, entrycerts.UsageTypeTunnelBackendClientCert, optionalAssetIDSlice(tunnel.BackendTLSClientCertAssetID), true, now); err != nil {
		return fmt.Errorf("save tunnel backend client certificate binding: %w", err)
	}
	if err := entrycerts.ReplaceTargetUsages(ctx, conn, entrycerts.TargetTypeTunnel, tunnelID, entrycerts.UsageTypeTunnelBackendCA, tunnel.BackendTLSCAAssetIDs, true, now); err != nil {
		return fmt.Errorf("save tunnel backend ca bindings: %w", err)
	}
	return nil
}

func (s *Service) deleteTunnelCertificateUsages(ctx context.Context, conn storage.Conn, tunnelID int64) error {
	now := time.Now().UTC()
	for _, usageType := range []entrycerts.UsageType{
		entrycerts.UsageTypeTunnelListenServerCert,
		entrycerts.UsageTypeTunnelListenClientCA,
		entrycerts.UsageTypeTunnelBackendClientCert,
		entrycerts.UsageTypeTunnelBackendCA,
	} {
		if err := entrycerts.ReplaceTargetUsages(ctx, conn, entrycerts.TargetTypeTunnel, tunnelID, usageType, nil, true, now); err != nil {
			return fmt.Errorf("delete tunnel certificate bindings: %w", err)
		}
	}
	return nil
}

func (s *Service) loadTunnelUsageMap(ctx context.Context, conn storage.Conn) (map[int64]map[entrycerts.UsageType][]int64, error) {
	usages, err := entrycerts.ListUsagesWithConn(ctx, conn)
	if err != nil {
		return nil, fmt.Errorf("list tunnel certificate bindings: %w", err)
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

func (s *Service) applyTunnelUsageView(item *TunnelView, usages map[entrycerts.UsageType][]int64) {
	if item == nil || len(usages) == 0 {
		return
	}
	if assetIDs := usages[entrycerts.UsageTypeTunnelListenServerCert]; len(assetIDs) > 0 {
		item.ListenTLSServerCertAssetID = int64Ptr(assetIDs[0])
	}
	if assetIDs := usages[entrycerts.UsageTypeTunnelListenClientCA]; len(assetIDs) > 0 {
		item.ListenTLSClientCAAssetIDs = append([]int64(nil), assetIDs...)
	}
	if assetIDs := usages[entrycerts.UsageTypeTunnelBackendClientCert]; len(assetIDs) > 0 {
		item.BackendTLSClientCertAssetID = int64Ptr(assetIDs[0])
	}
	if assetIDs := usages[entrycerts.UsageTypeTunnelBackendCA]; len(assetIDs) > 0 {
		item.BackendTLSCAAssetIDs = append([]int64(nil), assetIDs...)
	}
}

func optionalAssetIDSlice(value *int64) []int64 {
	if value == nil {
		return nil
	}
	return []int64{*value}
}

func int64Ptr(value int64) *int64 {
	target := value
	return &target
}

func (s *Service) withTunnelStatuses(items []TunnelView) []TunnelView {
	conflicts, itemsByID := detectTunnelConflicts(items)
	runtimeIssues := map[int64]string(nil)
	if s != nil && s.runtime != nil {
		runtimeIssues = s.runtime.TunnelRuntimeIssues()
	}

	for index := range items {
		item := &items[index]
		switch {
		case !item.GroupEnabled || !item.Enabled:
			item.Status = tunnelStatusDisabled
			item.StatusReason = ""
		case conflictReasonForTunnel(item.ID, conflicts, itemsByID) != "":
			item.Status = tunnelStatusConflict
			item.StatusReason = conflictReasonForTunnel(item.ID, conflicts, itemsByID)
		case strings.TrimSpace(runtimeIssues[item.ID]) != "":
			item.Status = tunnelStatusAbnormal
			item.StatusReason = strings.TrimSpace(runtimeIssues[item.ID])
		default:
			item.Status = tunnelStatusEnabled
			item.StatusReason = ""
		}
	}

	return items
}

func detectTunnelConflicts(items []TunnelView) (map[int64]ports.Conflict, map[int64]TunnelView) {
	claims := make([]ports.Claim, 0, len(items))
	itemsByID := make(map[int64]TunnelView, len(items))
	for _, item := range items {
		itemsByID[item.ID] = item
		if !item.GroupEnabled || !item.Enabled {
			continue
		}
		claims = append(claims, ports.Claim{
			OwnerID:     item.ID,
			Protocol:    strings.ToLower(strings.TrimSpace(item.Protocol)),
			EffectiveIP: item.GroupEffectiveIP,
			PortStart:   item.RemoteStart,
			PortEnd:     item.RemoteEnd,
		})
	}
	return ports.DetectConflicts(claims), itemsByID
}

func conflictReasonForTunnel(tunnelID int64, conflicts map[int64]ports.Conflict, itemsByID map[int64]TunnelView) string {
	conflict, ok := conflicts[tunnelID]
	if !ok {
		return ""
	}
	target := itemsByID[tunnelID]
	other := itemsByID[conflict.OtherOwnerID]
	return buildTunnelConflictReason(target, other, conflict)
}

func buildTunnelConflictReason(target, other TunnelView, conflict ports.Conflict) string {
	effectiveIP := conflict.OwnerEffectiveIP
	if strings.TrimSpace(conflict.OwnerEffectiveIP) != "" && strings.TrimSpace(conflict.OtherEffectiveIP) != "" && conflict.OwnerEffectiveIP != conflict.OtherEffectiveIP {
		effectiveIP = conflict.OwnerEffectiveIP + " <-> " + conflict.OtherEffectiveIP
	}
	return fmt.Sprintf(
		`与分组"%s"中的隧道"%s"在 %s %s:%s 上冲突`,
		other.GroupName,
		other.Name,
		strings.ToUpper(conflict.Protocol),
		effectiveIP,
		formatPortRange(conflict.ConflictStart, conflict.ConflictEnd),
	)
}

func formatPortRange(start, end int64) string {
	if start == end {
		return strconv.FormatInt(start, 10)
	}
	return fmt.Sprintf("%d-%d", start, end)
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
