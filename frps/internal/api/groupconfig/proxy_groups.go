package groupconfig

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"

	"github.com/zightch/frp/frps/internal/proxygroups"
	"github.com/zightch/frp/frps/internal/settings/entrycerts"
	"github.com/zightch/frp/frps/internal/storage"
	"github.com/zightch/frp/frps/internal/system"
)

func (s *Service) ListProxyGroups(ctx context.Context) ([]ProxyGroupView, error) {
	result, err := s.store.QueryContext(
		ctx,
		`
SELECT
	id,
	name,
	client_id,
	effective_ip,
	enabled,
	control_transport_security,
	created_at,
	updated_at
FROM proxy_groups
ORDER BY id
`,
	)
	if err != nil {
		return nil, fmt.Errorf("list proxy groups: %w", err)
	}

	items := make([]ProxyGroupView, 0, len(result.Rows))
	for _, row := range result.Rows {
		item, err := decodeProxyGroupRow(row)
		if err != nil {
			return nil, fmt.Errorf("decode proxy group: %w", err)
		}
		items = append(items, s.withProxyGroupStatus(item))
	}
	return items, nil
}

func (s *Service) CreateProxyGroup(ctx context.Context, payload ProxyGroupCreateRequest) (ProxyGroupView, string, error) {
	normalized, err := s.normalizeCreateProxyGroup(payload)
	if err != nil {
		return ProxyGroupView{}, "", err
	}
	if err := s.validateControlTransportSecurity(ctx, normalized.ControlTransportSecurity); err != nil {
		return ProxyGroupView{}, "", err
	}

	var (
		item ProxyGroupView
		key  string
	)

	err = s.store.WithTxContext(ctx, nil, func(tx *storage.Tx) error {
		now := schemaTimestamp()
		clientID, clientSecret, clientSecretHash, err := generateClientCredentials()
		if err != nil {
			return err
		}

		result, err := tx.ExecContext(
			ctx,
			`
INSERT INTO proxy_groups (
	name,
	client_id,
	client_secret_hash,
	effective_ip,
	enabled,
	control_transport_security,
	rate_limit,
	created_at,
	updated_at
) VALUES (?, ?, ?, ?, ?, ?, 0, ?, ?)
`,
			normalized.Name,
			clientID,
			clientSecretHash,
			normalized.EffectiveIP,
			boolToInt(normalized.Enabled),
			string(normalized.ControlTransportSecurity),
			now,
			now,
		)
		if err != nil {
			return wrapUniqueConstraintError(err, "proxy group name already exists")
		}

		item, err = s.loadProxyGroupByID(ctx, tx, result.LastInsertID)
		if err != nil {
			return err
		}
		key = composeClientKey(clientID, clientSecret)
		return nil
	})
	if err != nil {
		return ProxyGroupView{}, "", err
	}

	return item, key, nil
}

func (s *Service) UpdateProxyGroup(ctx context.Context, id int64, payload ProxyGroupPatchRequest) (ProxyGroupView, error) {
	var item ProxyGroupView
	err := s.store.WithTxContext(ctx, nil, func(tx *storage.Tx) error {
		current, err := s.loadProxyGroupByID(ctx, tx, id)
		if err != nil {
			return err
		}

		normalized, err := s.normalizeProxyGroupPatch(current, payload)
		if err != nil {
			return err
		}
		if err := s.validateControlTransportSecurity(ctx, normalized.ControlTransportSecurity); err != nil {
			return err
		}
		if err := s.ensureConflictFreeForGroupUpdate(ctx, tx, id, current, normalized); err != nil {
			return err
		}

		result, err := tx.ExecContext(
			ctx,
			`
UPDATE proxy_groups
SET name = ?, effective_ip = ?, enabled = ?, control_transport_security = ?, updated_at = ?
WHERE id = ?
`,
			normalized.Name,
			normalized.EffectiveIP,
			boolToInt(normalized.Enabled),
			string(normalized.ControlTransportSecurity),
			schemaTimestamp(),
			id,
		)
		if err != nil {
			return wrapUniqueConstraintError(err, "proxy group name already exists")
		}
		if result.RowsAffected == 0 {
			return &Error{Status: 404, Message: "proxy group not found"}
		}

		item, err = s.loadProxyGroupByID(ctx, tx, id)
		return err
	})
	if err != nil {
		return ProxyGroupView{}, err
	}

	s.refreshGroups(id)
	return item, nil
}

func (s *Service) DeleteProxyGroup(ctx context.Context, id int64) error {
	err := s.store.WithTxContext(ctx, nil, func(tx *storage.Tx) error {
		tunnelRows, err := tx.QueryContext(ctx, "SELECT id FROM tunnels WHERE group_id = ?", id)
		if err != nil {
			return fmt.Errorf("load proxy group tunnels before delete: %w", err)
		}
		for _, row := range tunnelRows.Rows {
			tunnelID, err := rowInt64(row, "id")
			if err != nil {
				return fmt.Errorf("decode proxy group tunnel id: %w", err)
			}
			if err := s.deleteTunnelCertificateUsages(ctx, tx, tunnelID); err != nil {
				return err
			}
		}

		for _, statement := range []string{
			"DELETE FROM tunnels WHERE group_id = ?",
		} {
			if _, err := tx.ExecContext(ctx, statement, id); err != nil {
				return fmt.Errorf("delete proxy group dependencies: %w", err)
			}
		}

		result, err := tx.ExecContext(ctx, "DELETE FROM proxy_groups WHERE id = ?", id)
		if err != nil {
			return fmt.Errorf("delete proxy group: %w", err)
		}
		if result.RowsAffected == 0 {
			return &Error{Status: 404, Message: "proxy group not found"}
		}
		return nil
	})
	if err != nil {
		return err
	}

	s.refreshGroups(id)
	return nil
}

func (s *Service) RotateProxyGroupKey(ctx context.Context, id int64) (ProxyGroupView, string, error) {
	var (
		item ProxyGroupView
		key  string
	)

	err := s.store.WithTxContext(ctx, nil, func(tx *storage.Tx) error {
		current, err := s.loadProxyGroupByID(ctx, tx, id)
		if err != nil {
			return err
		}
		clientSecret, clientSecretHash, err := generateClientSecret()
		if err != nil {
			return err
		}

		result, err := tx.ExecContext(
			ctx,
			`
UPDATE proxy_groups
SET client_secret_hash = ?, updated_at = ?
WHERE id = ?
`,
			clientSecretHash,
			schemaTimestamp(),
			id,
		)
		if err != nil {
			return wrapUniqueConstraintError(err, "proxy group key rotation failed")
		}
		if result.RowsAffected == 0 {
			return &Error{Status: 404, Message: "proxy group not found"}
		}

		item, err = s.loadProxyGroupByID(ctx, tx, id)
		if err != nil {
			return err
		}
		key = composeClientKey(current.ClientID, clientSecret)
		return nil
	})
	if err != nil {
		return ProxyGroupView{}, "", err
	}

	s.refreshGroups(id)
	return item, key, nil
}

func (s *Service) loadProxyGroupByID(ctx context.Context, conn storage.Conn, id int64) (ProxyGroupView, error) {
	row, err := conn.QueryOneContext(
		ctx,
		`
SELECT
	id,
	name,
	client_id,
	effective_ip,
	enabled,
	control_transport_security,
	created_at,
	updated_at
FROM proxy_groups
WHERE id = ?
`,
		id,
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ProxyGroupView{}, &Error{Status: 404, Message: "proxy group not found"}
		}
		return ProxyGroupView{}, fmt.Errorf("load proxy group: %w", err)
	}

	item, err := decodeProxyGroupRow(row)
	if err != nil {
		return ProxyGroupView{}, fmt.Errorf("decode proxy group: %w", err)
	}
	return s.withProxyGroupStatus(item), nil
}

func (s *Service) ensureConflictFreeForGroupUpdate(ctx context.Context, conn storage.Conn, groupID int64, current ProxyGroupView, normalized normalizedProxyGroup) error {
	if current.Enabled == normalized.Enabled && current.EffectiveIP == normalized.EffectiveIP {
		return nil
	}

	items, err := s.loadTunnelsWithGroupState(ctx, conn)
	if err != nil {
		return err
	}

	targetIDs := make(map[int64]struct{})
	for index := range items {
		if items[index].GroupID != groupID {
			continue
		}
		items[index].GroupEnabled = normalized.Enabled
		items[index].GroupEffectiveIP = normalized.EffectiveIP
		targetIDs[items[index].ID] = struct{}{}
	}

	return conflictErrorForTargets(items, targetIDs)
}

func (s *Service) normalizeCreateProxyGroup(payload ProxyGroupCreateRequest) (normalizedProxyGroup, error) {
	item := normalizedProxyGroup{
		Name:                     strings.TrimSpace(payload.Name),
		Enabled:                  true,
		ControlTransportSecurity: proxygroups.DefaultControlTransportSecurity(),
	}
	if payload.Enabled != nil {
		item.Enabled = *payload.Enabled
	}
	if strings.TrimSpace(payload.ControlTransportSecurity) != "" {
		item.ControlTransportSecurity = proxygroups.NormalizeControlTransportSecurity(payload.ControlTransportSecurity)
	}
	if item.Name == "" {
		return normalizedProxyGroup{}, &Error{Status: 400, Message: "name is required"}
	}
	if item.ControlTransportSecurity == "" {
		return normalizedProxyGroup{}, &Error{Status: 400, Message: "control_transport_security must be plain or tls_required"}
	}

	normalizedIP, err := s.normalizeSubmittedEffectiveIP(payload.EffectiveIP)
	if err != nil {
		return normalizedProxyGroup{}, err
	}
	item.EffectiveIP = normalizedIP
	return item, nil
}

func (s *Service) normalizeProxyGroupPatch(current ProxyGroupView, payload ProxyGroupPatchRequest) (normalizedProxyGroup, error) {
	item := normalizedProxyGroup{
		Name:                     current.Name,
		EffectiveIP:              current.EffectiveIP,
		Enabled:                  current.Enabled,
		ControlTransportSecurity: proxygroups.NormalizeControlTransportSecurity(current.ControlTransportSecurity),
	}
	if item.ControlTransportSecurity == "" {
		item.ControlTransportSecurity = proxygroups.DefaultControlTransportSecurity()
	}
	hasChange := false

	if payload.Name != nil {
		item.Name = strings.TrimSpace(*payload.Name)
		hasChange = true
	}
	if payload.EffectiveIP != nil {
		normalizedIP, err := s.normalizeSubmittedEffectiveIP(*payload.EffectiveIP)
		if err != nil {
			return normalizedProxyGroup{}, err
		}
		item.EffectiveIP = normalizedIP
		hasChange = true
	}
	if payload.Enabled != nil {
		item.Enabled = *payload.Enabled
		hasChange = true
	}
	if payload.ControlTransportSecurity != nil {
		item.ControlTransportSecurity = proxygroups.NormalizeControlTransportSecurity(*payload.ControlTransportSecurity)
		hasChange = true
	}

	if !hasChange {
		return normalizedProxyGroup{}, &Error{Status: 400, Message: "at least one of name, effective_ip, enabled, control_transport_security is required"}
	}
	if item.Name == "" {
		return normalizedProxyGroup{}, &Error{Status: 400, Message: "name is required"}
	}
	if item.ControlTransportSecurity == "" {
		return normalizedProxyGroup{}, &Error{Status: 400, Message: "control_transport_security must be plain or tls_required"}
	}
	return item, nil
}

func (s *Service) normalizeSubmittedEffectiveIP(raw string) (string, error) {
	normalizedIP, err := system.NormalizeListenIP(raw)
	if err != nil {
		return "", &Error{Status: 400, Message: "effective_ip must be a valid IP literal"}
	}
	if system.IsSpecialListenIP(normalizedIP) {
		return normalizedIP, nil
	}
	if s.network == nil {
		return "", &Error{Status: 503, Message: "local network snapshot is unavailable"}
	}
	if !s.network.Current().HasIP(normalizedIP) {
		return "", &Error{Status: 400, Message: "effective_ip must be a current local IP"}
	}
	return normalizedIP, nil
}

func (s *Service) validateControlTransportSecurity(ctx context.Context, security proxygroups.ControlTransportSecurity) error {
	if security != proxygroups.ControlTransportSecurityTLSRequired {
		return nil
	}
	if s == nil || s.entryCerts == nil {
		return &Error{Status: 503, Message: "entry certificate service is unavailable"}
	}

	enabled, err := s.entryCerts.IsEnabled(ctx, entrycerts.UsageTypeFrpcTLS)
	if err != nil {
		return fmt.Errorf("load frpc tls entry certificate: %w", err)
	}
	if !enabled {
		return &Error{
			Status:  409,
			Message: "frpc tls entry certificate must be bound before frpc tls can be enabled",
			Code:    "frpc_tls_required",
		}
	}
	return nil
}

func (s *Service) withProxyGroupStatus(item ProxyGroupView) ProxyGroupView {
	item.Status, item.StatusReason = s.deriveProxyGroupStatus(item.Enabled, item.EffectiveIP)
	return item
}

func (s *Service) deriveProxyGroupStatus(enabled bool, effectiveIP string) (string, string) {
	if !enabled {
		return proxyGroupStatusDisabled, ""
	}
	if system.IsSpecialListenIP(effectiveIP) {
		return proxyGroupStatusEnabled, ""
	}
	if s == nil || s.network == nil {
		return proxyGroupStatusAbnormal, proxyGroupStatusReasonSnapshotUnavailable
	}
	if s.network.Current().HasIP(effectiveIP) {
		return proxyGroupStatusEnabled, ""
	}
	return proxyGroupStatusAbnormal, proxyGroupStatusReasonMissingLocalIP
}

func generateClientCredentials() (string, string, string, error) {
	clientID, err := generateClientID()
	if err != nil {
		return "", "", "", err
	}
	clientSecret, clientSecretHash, err := generateClientSecret()
	if err != nil {
		return "", "", "", err
	}
	return clientID, clientSecret, clientSecretHash, nil
}

func composeClientKey(clientID, clientSecret string) string {
	return strings.TrimSpace(clientID) + strings.TrimSpace(clientSecret)
}

func generateClientID() (string, error) {
	var clientID [16]byte
	if _, err := rand.Read(clientID[:]); err != nil {
		return "", fmt.Errorf("generate client id: %w", err)
	}
	return hex.EncodeToString(clientID[:]), nil
}

func generateClientSecret() (string, string, error) {
	var clientSecret [32]byte
	if _, err := rand.Read(clientSecret[:]); err != nil {
		return "", "", fmt.Errorf("generate client secret: %w", err)
	}

	clientSecretHash := sha256.Sum256(clientSecret[:])
	return hex.EncodeToString(clientSecret[:]), hex.EncodeToString(clientSecretHash[:]), nil
}
