package api

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/zightch/frp/frps/internal/storage"
	"github.com/zightch/frp/frps/internal/system"
	"github.com/zightch/frp/frps/pkg/protocol"
)

const schemaTimestampLayout = "2006-01-02 15:04:05.000000"

const (
	proxyGroupStatusEnabled  = "启用"
	proxyGroupStatusDisabled = "禁用"
	proxyGroupStatusAbnormal = "异常"

	proxyGroupStatusReasonMissingLocalIP      = "已配置 IP 当前不存在于本机"
	proxyGroupStatusReasonSnapshotUnavailable = "本机 IP 列表暂不可用"
)

type managementService struct {
	store   *storage.SQL
	network system.SnapshotReader
}

type proxyGroupView struct {
	ID           int64  `json:"id"`
	Name         string `json:"name"`
	TokenID      string `json:"token_id"`
	EffectiveIP  string `json:"effective_ip"`
	Enabled      bool   `json:"enabled"`
	Status       string `json:"status"`
	StatusReason string `json:"status_reason,omitempty"`
	CreatedAt    string `json:"created_at"`
	UpdatedAt    string `json:"updated_at"`
}

type tunnelView struct {
	ID          int64  `json:"id"`
	GroupID     int64  `json:"group_id"`
	GroupName   string `json:"group_name"`
	Name        string `json:"name"`
	Protocol    string `json:"protocol"`
	RemoteType  string `json:"remote_type"`
	RemoteStart int64  `json:"remote_start"`
	RemoteEnd   int64  `json:"remote_end"`
	LocalHost   string `json:"local_host"`
	LocalStart  int64  `json:"local_start"`
	LocalEnd    int64  `json:"local_end"`
	Enabled     bool   `json:"enabled"`
	CreatedAt   string `json:"created_at"`
	UpdatedAt   string `json:"updated_at"`
}

type proxyGroupRequest struct {
	Name        string `json:"name"`
	EffectiveIP string `json:"effective_ip"`
	Enabled     *bool  `json:"enabled"`
}

type tunnelRequest struct {
	GroupID     int64  `json:"group_id"`
	Name        string `json:"name"`
	Protocol    string `json:"protocol"`
	RemoteType  string `json:"remote_type"`
	RemoteStart int64  `json:"remote_start"`
	RemoteEnd   int64  `json:"remote_end"`
	LocalHost   string `json:"local_host"`
	LocalStart  int64  `json:"local_start"`
	LocalEnd    int64  `json:"local_end"`
	Enabled     *bool  `json:"enabled"`
}

type apiError struct {
	Status  int
	Message string
}

type normalizedProxyGroup struct {
	Name        string
	EffectiveIP string
	Enabled     bool
}

type normalizedTunnel struct {
	GroupID     int64
	Name        string
	Protocol    string
	RemoteType  string
	RemoteStart int64
	RemoteEnd   int64
	LocalHost   string
	LocalStart  int64
	LocalEnd    int64
	Enabled     bool
}

func (e *apiError) Error() string {
	if e == nil {
		return ""
	}
	return e.Message
}

func newManagementService(store *storage.SQL, network system.SnapshotReader) *managementService {
	if store == nil {
		return nil
	}
	return &managementService{store: store, network: network}
}

func (s *Server) handleProxyGroups(writer http.ResponseWriter, request *http.Request) {
	if !s.requireManagementSession(writer, request) {
		return
	}

	manager := s.requireManager(writer)
	if manager == nil {
		return
	}

	switch request.Method {
	case http.MethodGet:
		items, err := manager.listProxyGroups(request.Context())
		if err != nil {
			writeError(writer, err)
			return
		}
		writeJSON(writer, http.StatusOK, map[string]any{"items": items})
	case http.MethodPost:
		var payload proxyGroupRequest
		if err := decodeJSONBody(request, &payload); err != nil {
			writeError(writer, err)
			return
		}

		item, token, err := manager.createProxyGroup(request.Context(), payload)
		if err != nil {
			writeError(writer, err)
			return
		}
		writeJSON(writer, http.StatusCreated, map[string]any{
			"item":  item,
			"token": token,
		})
	default:
		writeMethodNotAllowed(writer)
	}
}

func (s *Server) handleProxyGroupResource(writer http.ResponseWriter, request *http.Request) {
	if !s.requireManagementSession(writer, request) {
		return
	}

	manager := s.requireManager(writer)
	if manager == nil {
		return
	}

	id, suffix, err := parseResourcePath(request.URL.Path, "/api/v1/proxy-groups/")
	if err != nil {
		writeError(writer, err)
		return
	}

	switch {
	case suffix == "" && request.Method == http.MethodPatch:
		var payload proxyGroupRequest
		if err := decodeJSONBody(request, &payload); err != nil {
			writeError(writer, err)
			return
		}

		item, err := manager.updateProxyGroup(request.Context(), id, payload)
		if err != nil {
			writeError(writer, err)
			return
		}
		writeJSON(writer, http.StatusOK, map[string]any{"item": item})
	case suffix == "" && request.Method == http.MethodDelete:
		if err := manager.deleteProxyGroup(request.Context(), id); err != nil {
			writeError(writer, err)
			return
		}
		writeJSON(writer, http.StatusOK, map[string]any{"deleted": true})
	case suffix == "token" && request.Method == http.MethodPost:
		item, token, err := manager.resetProxyGroupToken(request.Context(), id)
		if err != nil {
			writeError(writer, err)
			return
		}
		writeJSON(writer, http.StatusOK, map[string]any{
			"item":  item,
			"token": token,
		})
	default:
		writeMethodNotAllowed(writer)
	}
}

func (s *Server) handleTunnels(writer http.ResponseWriter, request *http.Request) {
	if !s.requireManagementSession(writer, request) {
		return
	}

	manager := s.requireManager(writer)
	if manager == nil {
		return
	}

	switch request.Method {
	case http.MethodGet:
		items, err := manager.listTunnels(request.Context())
		if err != nil {
			writeError(writer, err)
			return
		}
		writeJSON(writer, http.StatusOK, map[string]any{"items": items})
	case http.MethodPost:
		var payload tunnelRequest
		if err := decodeJSONBody(request, &payload); err != nil {
			writeError(writer, err)
			return
		}

		item, err := manager.createTunnel(request.Context(), payload)
		if err != nil {
			writeError(writer, err)
			return
		}
		writeJSON(writer, http.StatusCreated, map[string]any{"item": item})
	default:
		writeMethodNotAllowed(writer)
	}
}

func (s *Server) handleTunnelResource(writer http.ResponseWriter, request *http.Request) {
	if !s.requireManagementSession(writer, request) {
		return
	}

	manager := s.requireManager(writer)
	if manager == nil {
		return
	}

	id, suffix, err := parseResourcePath(request.URL.Path, "/api/v1/tunnels/")
	if err != nil {
		writeError(writer, err)
		return
	}
	if suffix != "" {
		writeError(writer, &apiError{Status: http.StatusNotFound, Message: "resource not found"})
		return
	}

	switch request.Method {
	case http.MethodPatch:
		var payload tunnelRequest
		if err := decodeJSONBody(request, &payload); err != nil {
			writeError(writer, err)
			return
		}

		item, err := manager.updateTunnel(request.Context(), id, payload)
		if err != nil {
			writeError(writer, err)
			return
		}
		writeJSON(writer, http.StatusOK, map[string]any{"item": item})
	case http.MethodDelete:
		if err := manager.deleteTunnel(request.Context(), id); err != nil {
			writeError(writer, err)
			return
		}
		writeJSON(writer, http.StatusOK, map[string]any{"deleted": true})
	default:
		writeMethodNotAllowed(writer)
	}
}

func (s *Server) requireManager(writer http.ResponseWriter) *managementService {
	if s.manager == nil {
		writeError(writer, &apiError{Status: http.StatusServiceUnavailable, Message: "management store is unavailable"})
		return nil
	}
	return s.manager
}

func (m *managementService) listProxyGroups(ctx context.Context) ([]proxyGroupView, error) {
	result, err := m.store.QueryContext(
		ctx,
		`
SELECT
	id,
	name,
	token_id,
	effective_ip,
	enabled,
	created_at,
	updated_at
FROM proxy_groups
ORDER BY id
`,
	)
	if err != nil {
		return nil, fmt.Errorf("list proxy groups: %w", err)
	}

	items := make([]proxyGroupView, 0, len(result.Rows))
	for _, row := range result.Rows {
		item, err := decodeProxyGroupRow(row)
		if err != nil {
			return nil, fmt.Errorf("decode proxy group: %w", err)
		}
		items = append(items, m.withProxyGroupStatus(item))
	}
	return items, nil
}

func (m *managementService) createProxyGroup(ctx context.Context, payload proxyGroupRequest) (proxyGroupView, string, error) {
	normalized, err := m.normalizeProxyGroup(payload)
	if err != nil {
		return proxyGroupView{}, "", err
	}

	var (
		item  proxyGroupView
		token string
	)

	err = m.store.WithTxContext(ctx, nil, func(tx *storage.Tx) error {
		now := schemaTimestamp()
		tokenValue, tokenID, tokenHash, err := generateToken()
		if err != nil {
			return err
		}

		result, err := tx.ExecContext(
			ctx,
			`
INSERT INTO proxy_groups (
	name,
	token_id,
	token_hash,
	effective_ip,
	enabled,
	rate_limit,
	created_at,
	updated_at
) VALUES (?, ?, ?, ?, ?, 0, ?, ?)
`,
			normalized.Name,
			tokenID,
			tokenHash,
			normalized.EffectiveIP,
			boolToInt(normalized.Enabled),
			now,
			now,
		)
		if err != nil {
			return writeConflictError(err, "proxy group name already exists")
		}

		item, err = m.loadProxyGroupByID(ctx, tx, result.LastInsertID)
		if err != nil {
			return err
		}
		token = tokenValue
		return nil
	})
	if err != nil {
		return proxyGroupView{}, "", err
	}

	return item, token, nil
}

func (m *managementService) updateProxyGroup(ctx context.Context, id int64, payload proxyGroupRequest) (proxyGroupView, error) {
	normalized, err := m.normalizeProxyGroup(payload)
	if err != nil {
		return proxyGroupView{}, err
	}

	var item proxyGroupView
	err = m.store.WithTxContext(ctx, nil, func(tx *storage.Tx) error {
		result, err := tx.ExecContext(
			ctx,
			`
UPDATE proxy_groups
SET name = ?, effective_ip = ?, enabled = ?, updated_at = ?
WHERE id = ?
`,
			normalized.Name,
			normalized.EffectiveIP,
			boolToInt(normalized.Enabled),
			schemaTimestamp(),
			id,
		)
		if err != nil {
			return writeConflictError(err, "proxy group name already exists")
		}
		if result.RowsAffected == 0 {
			return &apiError{Status: http.StatusNotFound, Message: "proxy group not found"}
		}

		item, err = m.loadProxyGroupByID(ctx, tx, id)
		return err
	})
	if err != nil {
		return proxyGroupView{}, err
	}

	return item, nil
}

func (m *managementService) deleteProxyGroup(ctx context.Context, id int64) error {
	return m.store.WithTxContext(ctx, nil, func(tx *storage.Tx) error {
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
			return &apiError{Status: http.StatusNotFound, Message: "proxy group not found"}
		}
		return nil
	})
}

func (m *managementService) resetProxyGroupToken(ctx context.Context, id int64) (proxyGroupView, string, error) {
	var (
		item  proxyGroupView
		token string
	)

	err := m.store.WithTxContext(ctx, nil, func(tx *storage.Tx) error {
		tokenValue, tokenID, tokenHash, err := generateToken()
		if err != nil {
			return err
		}

		result, err := tx.ExecContext(
			ctx,
			`
UPDATE proxy_groups
SET token_id = ?, token_hash = ?, updated_at = ?
WHERE id = ?
`,
			tokenID,
			tokenHash,
			schemaTimestamp(),
			id,
		)
		if err != nil {
			return writeConflictError(err, "proxy group token reset failed")
		}
		if result.RowsAffected == 0 {
			return &apiError{Status: http.StatusNotFound, Message: "proxy group not found"}
		}

		item, err = m.loadProxyGroupByID(ctx, tx, id)
		if err != nil {
			return err
		}
		token = tokenValue
		return nil
	})
	if err != nil {
		return proxyGroupView{}, "", err
	}

	return item, token, nil
}

func (m *managementService) listTunnels(ctx context.Context) ([]tunnelView, error) {
	result, err := m.store.QueryContext(
		ctx,
		`
SELECT
	t.id,
	t.group_id,
	COALESCE(g.name, '') AS group_name,
	t.name,
	t.protocol,
	t.remote_type,
	t.remote_start,
	t.remote_end,
	t.local_host,
	t.local_start,
	t.local_end,
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

	items := make([]tunnelView, 0, len(result.Rows))
	for _, row := range result.Rows {
		item, err := decodeTunnelRow(row)
		if err != nil {
			return nil, fmt.Errorf("decode tunnel: %w", err)
		}
		items = append(items, item)
	}
	return items, nil
}

func (m *managementService) createTunnel(ctx context.Context, payload tunnelRequest) (tunnelView, error) {
	normalized, err := normalizeTunnel(payload)
	if err != nil {
		return tunnelView{}, err
	}

	var item tunnelView
	err = m.store.WithTxContext(ctx, nil, func(tx *storage.Tx) error {
		if err := ensureProxyGroupExists(ctx, tx, normalized.GroupID); err != nil {
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
	enabled,
	created_at,
	updated_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
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
			boolToInt(normalized.Enabled),
			now,
			now,
		)
		if err != nil {
			return writeConflictError(err, "tunnel name already exists in the selected proxy group")
		}

		item, err = m.loadTunnelByID(ctx, tx, result.LastInsertID)
		return err
	})
	if err != nil {
		return tunnelView{}, err
	}

	return item, nil
}

func (m *managementService) updateTunnel(ctx context.Context, id int64, payload tunnelRequest) (tunnelView, error) {
	normalized, err := normalizeTunnel(payload)
	if err != nil {
		return tunnelView{}, err
	}

	var item tunnelView
	err = m.store.WithTxContext(ctx, nil, func(tx *storage.Tx) error {
		if err := ensureProxyGroupExists(ctx, tx, normalized.GroupID); err != nil {
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
			boolToInt(normalized.Enabled),
			schemaTimestamp(),
			id,
		)
		if err != nil {
			return writeConflictError(err, "tunnel name already exists in the selected proxy group")
		}
		if result.RowsAffected == 0 {
			return &apiError{Status: http.StatusNotFound, Message: "tunnel not found"}
		}

		item, err = m.loadTunnelByID(ctx, tx, id)
		return err
	})
	if err != nil {
		return tunnelView{}, err
	}

	return item, nil
}

func (m *managementService) deleteTunnel(ctx context.Context, id int64) error {
	result, err := m.store.ExecContext(ctx, "DELETE FROM tunnels WHERE id = ?", id)
	if err != nil {
		return fmt.Errorf("delete tunnel: %w", err)
	}
	if result.RowsAffected == 0 {
		return &apiError{Status: http.StatusNotFound, Message: "tunnel not found"}
	}
	return nil
}

func (m *managementService) loadProxyGroupByID(ctx context.Context, conn storage.Conn, id int64) (proxyGroupView, error) {
	row, err := conn.QueryOneContext(
		ctx,
		`
SELECT
	id,
	name,
	token_id,
	effective_ip,
	enabled,
	created_at,
	updated_at
FROM proxy_groups
WHERE id = ?
`,
		id,
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return proxyGroupView{}, &apiError{Status: http.StatusNotFound, Message: "proxy group not found"}
		}
		return proxyGroupView{}, fmt.Errorf("load proxy group: %w", err)
	}

	item, err := decodeProxyGroupRow(row)
	if err != nil {
		return proxyGroupView{}, fmt.Errorf("decode proxy group: %w", err)
	}
	return m.withProxyGroupStatus(item), nil
}

func (m *managementService) loadTunnelByID(ctx context.Context, conn storage.Conn, id int64) (tunnelView, error) {
	row, err := conn.QueryOneContext(
		ctx,
		`
SELECT
	t.id,
	t.group_id,
	COALESCE(g.name, '') AS group_name,
	t.name,
	t.protocol,
	t.remote_type,
	t.remote_start,
	t.remote_end,
	t.local_host,
	t.local_start,
	t.local_end,
	t.enabled,
	t.created_at,
	t.updated_at
FROM tunnels AS t
LEFT JOIN proxy_groups AS g ON g.id = t.group_id
WHERE t.id = ?
`,
		id,
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return tunnelView{}, &apiError{Status: http.StatusNotFound, Message: "tunnel not found"}
		}
		return tunnelView{}, fmt.Errorf("load tunnel: %w", err)
	}

	item, err := decodeTunnelRow(row)
	if err != nil {
		return tunnelView{}, fmt.Errorf("decode tunnel: %w", err)
	}
	return item, nil
}

func ensureProxyGroupExists(ctx context.Context, conn storage.Conn, id int64) error {
	if id <= 0 {
		return &apiError{Status: http.StatusBadRequest, Message: "group_id must be greater than zero"}
	}
	if _, err := conn.QueryOneContext(ctx, "SELECT id FROM proxy_groups WHERE id = ?", id); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return &apiError{Status: http.StatusBadRequest, Message: "group_id does not exist"}
		}
		return fmt.Errorf("load proxy group: %w", err)
	}
	return nil
}

func (m *managementService) normalizeProxyGroup(payload proxyGroupRequest) (normalizedProxyGroup, error) {
	item := normalizedProxyGroup{
		Name:    strings.TrimSpace(payload.Name),
		Enabled: true,
	}
	if payload.Enabled != nil {
		item.Enabled = *payload.Enabled
	}
	if item.Name == "" {
		return normalizedProxyGroup{}, &apiError{Status: http.StatusBadRequest, Message: "name is required"}
	}

	normalizedIP, err := system.NormalizeListenIP(payload.EffectiveIP)
	if err != nil {
		return normalizedProxyGroup{}, &apiError{Status: http.StatusBadRequest, Message: "effective_ip must be a valid IP literal"}
	}
	if !system.IsSpecialListenIP(normalizedIP) {
		if m.network == nil {
			return normalizedProxyGroup{}, &apiError{Status: http.StatusServiceUnavailable, Message: "local network snapshot is unavailable"}
		}
		if !m.network.Current().HasIP(normalizedIP) {
			return normalizedProxyGroup{}, &apiError{Status: http.StatusBadRequest, Message: "effective_ip must be a current local IP"}
		}
	}
	item.EffectiveIP = normalizedIP

	return item, nil
}

func normalizeTunnel(payload tunnelRequest) (normalizedTunnel, error) {
	item := normalizedTunnel{
		GroupID:     payload.GroupID,
		Name:        strings.TrimSpace(payload.Name),
		Protocol:    strings.ToLower(strings.TrimSpace(payload.Protocol)),
		RemoteType:  strings.ToLower(strings.TrimSpace(payload.RemoteType)),
		RemoteStart: payload.RemoteStart,
		RemoteEnd:   payload.RemoteEnd,
		LocalHost:   strings.TrimSpace(payload.LocalHost),
		LocalStart:  payload.LocalStart,
		LocalEnd:    payload.LocalEnd,
		Enabled:     true,
	}
	if payload.Enabled != nil {
		item.Enabled = *payload.Enabled
	}
	if item.GroupID <= 0 {
		return normalizedTunnel{}, &apiError{Status: http.StatusBadRequest, Message: "group_id must be greater than zero"}
	}
	if item.Name == "" {
		return normalizedTunnel{}, &apiError{Status: http.StatusBadRequest, Message: "name is required"}
	}
	switch item.Protocol {
	case "tcp", "udp":
	default:
		return normalizedTunnel{}, &apiError{Status: http.StatusBadRequest, Message: "protocol must be tcp or udp"}
	}
	switch item.RemoteType {
	case "single", "range":
	default:
		return normalizedTunnel{}, &apiError{Status: http.StatusBadRequest, Message: "remote_type must be single or range"}
	}
	if _, err := protocol.ParseHost(item.LocalHost); err != nil {
		return normalizedTunnel{}, &apiError{Status: http.StatusBadRequest, Message: "local_host is invalid"}
	}
	if !validPort(item.RemoteStart) || !validPort(item.RemoteEnd) || !validPort(item.LocalStart) || !validPort(item.LocalEnd) {
		return normalizedTunnel{}, &apiError{Status: http.StatusBadRequest, Message: "ports must be between 1 and 65535"}
	}
	if item.RemoteEnd < item.RemoteStart || item.LocalEnd < item.LocalStart {
		return normalizedTunnel{}, &apiError{Status: http.StatusBadRequest, Message: "port range end must be greater than or equal to start"}
	}
	if item.RemoteType == "single" && (item.RemoteStart != item.RemoteEnd || item.LocalStart != item.LocalEnd) {
		return normalizedTunnel{}, &apiError{Status: http.StatusBadRequest, Message: "single tunnel must use identical start and end ports"}
	}
	if item.RemoteType == "range" && (item.RemoteEnd-item.RemoteStart) != (item.LocalEnd-item.LocalStart) {
		return normalizedTunnel{}, &apiError{Status: http.StatusBadRequest, Message: "remote and local port ranges must be aligned"}
	}
	return item, nil
}

func decodeProxyGroupRow(row storage.Row) (proxyGroupView, error) {
	var item proxyGroupView
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
	item.TokenID = rowString(row, "token_id")
	item.CreatedAt = rowTimeString(row, "created_at")
	item.UpdatedAt = rowTimeString(row, "updated_at")
	return item, nil
}

func (m *managementService) withProxyGroupStatus(item proxyGroupView) proxyGroupView {
	item.Status, item.StatusReason = m.deriveProxyGroupStatus(item.Enabled, item.EffectiveIP)
	return item
}

func (m *managementService) deriveProxyGroupStatus(enabled bool, effectiveIP string) (string, string) {
	if !enabled {
		return proxyGroupStatusDisabled, ""
	}
	if system.IsSpecialListenIP(effectiveIP) {
		return proxyGroupStatusEnabled, ""
	}
	if m == nil || m.network == nil {
		return proxyGroupStatusAbnormal, proxyGroupStatusReasonSnapshotUnavailable
	}
	if m.network.Current().HasIP(effectiveIP) {
		return proxyGroupStatusEnabled, ""
	}
	return proxyGroupStatusAbnormal, proxyGroupStatusReasonMissingLocalIP
}

func decodeTunnelRow(row storage.Row) (tunnelView, error) {
	var item tunnelView
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

	item.GroupName = rowString(row, "group_name")
	item.Name = rowString(row, "name")
	item.Protocol = rowString(row, "protocol")
	item.RemoteType = rowString(row, "remote_type")
	item.LocalHost = rowString(row, "local_host")
	item.CreatedAt = rowTimeString(row, "created_at")
	item.UpdatedAt = rowTimeString(row, "updated_at")
	return item, nil
}

func parseResourcePath(path, prefix string) (int64, string, error) {
	if !strings.HasPrefix(path, prefix) {
		return 0, "", &apiError{Status: http.StatusNotFound, Message: "resource not found"}
	}

	remainder := strings.Trim(strings.TrimPrefix(path, prefix), "/")
	if remainder == "" {
		return 0, "", &apiError{Status: http.StatusNotFound, Message: "resource not found"}
	}

	parts := strings.Split(remainder, "/")
	if len(parts) > 2 {
		return 0, "", &apiError{Status: http.StatusNotFound, Message: "resource not found"}
	}

	id, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil || id <= 0 {
		return 0, "", &apiError{Status: http.StatusNotFound, Message: "resource not found"}
	}

	if len(parts) == 1 {
		return id, "", nil
	}

	return id, parts[1], nil
}

func decodeJSONBody(request *http.Request, target any) error {
	if request.Body == nil {
		return &apiError{Status: http.StatusBadRequest, Message: "request body is required"}
	}
	defer request.Body.Close()

	decoder := json.NewDecoder(request.Body)
	if err := decoder.Decode(target); err != nil {
		if errors.Is(err, io.EOF) {
			return &apiError{Status: http.StatusBadRequest, Message: "request body is required"}
		}
		return &apiError{Status: http.StatusBadRequest, Message: fmt.Sprintf("invalid json body: %v", err)}
	}

	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return &apiError{Status: http.StatusBadRequest, Message: "request body must contain a single JSON object"}
	}
	return nil
}

func writeJSON(writer http.ResponseWriter, status int, payload any) {
	writer.Header().Set("Content-Type", "application/json; charset=utf-8")
	writer.WriteHeader(status)
	_ = json.NewEncoder(writer).Encode(payload)
}

func writeError(writer http.ResponseWriter, err error) {
	var apiErr *apiError
	if errors.As(err, &apiErr) {
		writeJSON(writer, apiErr.Status, map[string]any{"error": apiErr.Message})
		return
	}
	writeJSON(writer, http.StatusInternalServerError, map[string]any{"error": err.Error()})
}

func writeMethodNotAllowed(writer http.ResponseWriter) {
	writeError(writer, &apiError{Status: http.StatusMethodNotAllowed, Message: "method not allowed"})
}

func writeConflictError(err error, message string) error {
	if err == nil {
		return nil
	}
	if isUniqueConstraintError(err) {
		return &apiError{Status: http.StatusConflict, Message: message}
	}
	return err
}

func isUniqueConstraintError(err error) bool {
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "unique constraint") ||
		strings.Contains(message, "duplicate entry") ||
		strings.Contains(message, "duplicate key")
}

func generateToken() (string, string, string, error) {
	var tokenID [16]byte
	var tokenSecret [32]byte
	if _, err := rand.Read(tokenID[:]); err != nil {
		return "", "", "", fmt.Errorf("generate token id: %w", err)
	}
	if _, err := rand.Read(tokenSecret[:]); err != nil {
		return "", "", "", fmt.Errorf("generate token secret: %w", err)
	}

	tokenHash := sha256.Sum256(tokenSecret[:])
	tokenIDHex := hex.EncodeToString(tokenID[:])
	tokenSecretHex := hex.EncodeToString(tokenSecret[:])
	return tokenIDHex + tokenSecretHex, tokenIDHex, hex.EncodeToString(tokenHash[:]), nil
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
