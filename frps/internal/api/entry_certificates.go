package api

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"strings"

	"github.com/zightch/frp/frps/internal/settings/entrycerts"
)

const (
	entryCertificatesPathSettings = "/api/v1/settings/entry-certificates"
)

type entryCertificateView struct {
	UsageType           string                `json:"usage_type"`
	AssetID             *int64                `json:"asset_id,omitempty"`
	AssetName           string                `json:"asset_name,omitempty"`
	Enabled             bool                  `json:"enabled"`
	Status              string                `json:"status"`
	StatusReason        string                `json:"status_reason,omitempty"`
	ResolvedChainLength int                   `json:"resolved_chain_length"`
	UpdatedAt           string                `json:"updated_at,omitempty"`
	Asset               *certificateAssetView `json:"asset,omitempty"`
}

type entryCertificatePutRequest struct {
	AssetID int64 `json:"asset_id"`
	Enabled *bool `json:"enabled"`
}

type parsedEntryCertificatePath struct {
	UsageType entrycerts.UsageType
}

func (s *Server) handleEntryCertificates(writer http.ResponseWriter, request *http.Request) {
	if !s.requireManagementSession(writer, request) {
		return
	}

	manager := s.requireManager(writer)
	if manager == nil {
		return
	}

	if request.Method != http.MethodGet {
		writeMethodNotAllowed(writer)
		return
	}

	items, err := manager.listEntryCertificates(request.Context())
	if err != nil {
		writeError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"items": items})
}

func (s *Server) handleEntryCertificateResource(writer http.ResponseWriter, request *http.Request) {
	if !s.requireManagementSession(writer, request) {
		return
	}

	manager := s.requireManager(writer)
	if manager == nil {
		return
	}

	pathInfo, err := parseEntryCertificateTypeFromPath(request.URL.Path)
	if err != nil {
		writeError(writer, err)
		return
	}

	switch request.Method {
	case http.MethodPut:
		var payload entryCertificatePutRequest
		if err := decodeJSONBody(request, &payload); err != nil {
			writeError(writer, err)
			return
		}

		item, err := manager.upsertEntryCertificate(request.Context(), pathInfo.UsageType, payload)
		if err != nil {
			writeError(writer, err)
			return
		}
		writeJSON(writer, http.StatusOK, map[string]any{"item": item})
	case http.MethodDelete:
		item, err := manager.deleteEntryCertificate(request.Context(), pathInfo.UsageType)
		if err != nil {
			writeError(writer, err)
			return
		}
		writeJSON(writer, http.StatusOK, map[string]any{"item": item})
	default:
		writeMethodNotAllowed(writer)
	}
}

func parseEntryCertificateTypeFromPath(path string) (parsedEntryCertificatePath, error) {
	value, err := trimEntryCertificateResourcePath(path)
	if err != nil {
		return parsedEntryCertificatePath{}, err
	}
	if strings.Contains(value, "/") || strings.TrimSpace(value) == "" {
		return parsedEntryCertificatePath{}, &apiError{Status: http.StatusNotFound, Message: "resource not found"}
	}

	switch usageType := entrycerts.NormalizeUsageType(value); usageType {
	case entrycerts.UsageTypeWebUIHTTPS, entrycerts.UsageTypeFrpcTLS:
		return parsedEntryCertificatePath{UsageType: usageType}, nil
	default:
		return parsedEntryCertificatePath{}, &apiError{Status: http.StatusNotFound, Message: "resource not found"}
	}
}

func trimEntryCertificateResourcePath(path string) (string, error) {
	prefix := entryCertificatesPathSettings + "/"
	if strings.HasPrefix(path, prefix) {
		return strings.Trim(strings.TrimPrefix(path, prefix), "/"), nil
	}
	return "", &apiError{Status: http.StatusNotFound, Message: "resource not found"}
}

func (m *managementService) listEntryCertificates(ctx context.Context) ([]entryCertificateView, error) {
	if m == nil || m.entryCerts == nil {
		return nil, &apiError{Status: http.StatusServiceUnavailable, Message: "entry certificate service is unavailable"}
	}
	items, err := m.entryCerts.List(ctx)
	if err != nil {
		return nil, mapEntryCertificateError(err)
	}
	result := make([]entryCertificateView, 0, len(items))
	for _, item := range items {
		result = append(result, mapEntryCertificateView(item))
	}
	return result, nil
}

func (m *managementService) upsertEntryCertificate(ctx context.Context, usageType entrycerts.UsageType, payload entryCertificatePutRequest) (entryCertificateView, error) {
	if m == nil || m.entryCerts == nil || m.server == nil {
		return entryCertificateView{}, &apiError{Status: http.StatusServiceUnavailable, Message: "entry certificate service is unavailable"}
	}

	enabled := true
	if payload.Enabled != nil {
		enabled = *payload.Enabled
	}
	if !enabled {
		return m.deleteEntryCertificate(ctx, usageType)
	}
	if payload.AssetID <= 0 {
		return entryCertificateView{}, &apiError{Status: http.StatusBadRequest, Message: "asset_id must be greater than zero"}
	}

	binding, err := m.entryCerts.Resolve(ctx, usageType, payload.AssetID)
	if err != nil {
		return entryCertificateView{}, mapEntryCertificateError(err)
	}

	previous, previousExists, err := m.entryCerts.LoadUsage(ctx, usageType)
	if err != nil {
		return entryCertificateView{}, mapEntryCertificateError(err)
	}

	if _, err := m.entryCerts.SaveUsage(ctx, usageType, payload.AssetID, true); err != nil {
		return entryCertificateView{}, mapEntryCertificateError(err)
	}
	if err := m.applyEntryCertificateBinding(usageType, &binding); err != nil {
		_ = m.restorePreviousEntryCertificate(ctx, previousExists, previous)
		return entryCertificateView{}, mapEntryCertificateError(err)
	}

	item, err := m.entryCerts.Describe(ctx, usageType)
	if err != nil {
		return entryCertificateView{}, mapEntryCertificateError(err)
	}
	return mapEntryCertificateView(item), nil
}

func (m *managementService) deleteEntryCertificate(ctx context.Context, usageType entrycerts.UsageType) (entryCertificateView, error) {
	if m == nil || m.entryCerts == nil || m.server == nil {
		return entryCertificateView{}, &apiError{Status: http.StatusServiceUnavailable, Message: "entry certificate service is unavailable"}
	}

	if usageType == entrycerts.UsageTypeFrpcTLS {
		count, err := m.countTLSRequiredProxyGroups(ctx)
		if err != nil {
			return entryCertificateView{}, err
		}
		if count > 0 {
			return entryCertificateView{}, &apiError{
				Status:  http.StatusConflict,
				Message: "frpc tls entry certificate cannot be removed while frpc tls is enabled for proxy groups",
				Code:    "frpc_tls_in_use",
			}
		}
	}

	previous, previousExists, err := m.entryCerts.LoadUsage(ctx, usageType)
	if err != nil {
		return entryCertificateView{}, mapEntryCertificateError(err)
	}
	if !previousExists {
		item, describeErr := m.entryCerts.Describe(ctx, usageType)
		if describeErr != nil {
			return entryCertificateView{}, mapEntryCertificateError(describeErr)
		}
		return mapEntryCertificateView(item), nil
	}

	if err := m.entryCerts.DeleteUsage(ctx, usageType); err != nil {
		return entryCertificateView{}, mapEntryCertificateError(err)
	}
	if err := m.clearEntryCertificateBinding(usageType); err != nil {
		_ = m.restorePreviousEntryCertificate(ctx, previousExists, previous)
		return entryCertificateView{}, mapEntryCertificateError(err)
	}

	item, err := m.entryCerts.Describe(ctx, usageType)
	if err != nil {
		return entryCertificateView{}, mapEntryCertificateError(err)
	}
	return mapEntryCertificateView(item), nil
}

func (m *managementService) restorePreviousEntryCertificate(ctx context.Context, exists bool, usage entrycerts.Usage) error {
	if m == nil || m.entryCerts == nil {
		return nil
	}
	if !exists {
		return m.entryCerts.DeleteUsage(ctx, usage.UsageType)
	}
	return m.entryCerts.RestoreUsage(ctx, usage)
}

func (m *managementService) applyEntryCertificateBinding(usageType entrycerts.UsageType, binding *entrycerts.ResolvedBinding) error {
	switch usageType {
	case entrycerts.UsageTypeWebUIHTTPS:
		return m.server.EnableWebUIHTTPS(binding)
	case entrycerts.UsageTypeFrpcTLS:
		return m.server.ConfigureControlTLS(binding)
	default:
		return errors.New("unsupported entry certificate type")
	}
}

func (m *managementService) clearEntryCertificateBinding(usageType entrycerts.UsageType) error {
	switch usageType {
	case entrycerts.UsageTypeWebUIHTTPS:
		return m.server.DisableWebUIHTTPS()
	case entrycerts.UsageTypeFrpcTLS:
		return m.server.ClearControlTLS()
	default:
		return errors.New("unsupported entry certificate type")
	}
}

func mapEntryCertificateView(item entrycerts.DescribedUsage) entryCertificateView {
	view := entryCertificateView{
		UsageType:           string(item.UsageType),
		AssetID:             item.AssetID,
		AssetName:           item.AssetName,
		Enabled:             item.Enabled,
		Status:              item.Status,
		StatusReason:        item.StatusReason,
		ResolvedChainLength: item.ResolvedChainLength,
	}
	if !item.UpdatedAt.IsZero() {
		view.UpdatedAt = item.UpdatedAt.UTC().Format(schemaTimestampLayout)
	}
	if item.Asset != nil {
		assetView := mapCertificateAssetView(*item.Asset)
		view.Asset = &assetView
	}
	return view
}

func mapEntryCertificateError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, sql.ErrNoRows) {
		return &apiError{
			Status:  http.StatusNotFound,
			Message: "certificate asset not found",
			Code:    "certificate_asset_not_found",
		}
	}
	if strings.Contains(err.Error(), "bound asset must be a certificate") ||
		strings.Contains(err.Error(), "bound asset must include a private key") ||
		strings.Contains(err.Error(), "asset_id must be greater than zero") {
		return &apiError{
			Status:  http.StatusUnprocessableEntity,
			Message: err.Error(),
			Code:    "certificate_usage_validation_failed",
		}
	}
	return err
}

func (m *managementService) activeEntryCertificateConflicts(ctx context.Context, assetIDs []int64) ([]entryCertificateView, error) {
	if m == nil || m.entryCerts == nil || len(assetIDs) == 0 {
		return nil, nil
	}

	items, err := m.entryCerts.List(ctx)
	if err != nil {
		return nil, mapEntryCertificateError(err)
	}
	seen := make(map[int64]struct{}, len(assetIDs))
	for _, assetID := range assetIDs {
		seen[assetID] = struct{}{}
	}

	conflicts := make([]entryCertificateView, 0)
	for _, item := range items {
		if item.AssetID == nil || !item.Enabled {
			continue
		}
		if _, ok := seen[*item.AssetID]; !ok {
			continue
		}
		conflicts = append(conflicts, mapEntryCertificateView(item))
	}
	return conflicts, nil
}
