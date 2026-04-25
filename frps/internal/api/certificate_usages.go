package api

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"strings"

	"github.com/zightch/frp/frps/internal/certusages"
)

type certificateUsageView struct {
	UsageType           string  `json:"usage_type"`
	AssetID             *int64  `json:"asset_id,omitempty"`
	AssetName           string  `json:"asset_name,omitempty"`
	Enabled             bool    `json:"enabled"`
	Status              string  `json:"status"`
	StatusReason        string  `json:"status_reason,omitempty"`
	ResolvedChainLength int     `json:"resolved_chain_length"`
	UpdatedAt           string  `json:"updated_at,omitempty"`
	Asset               *certificateAssetView `json:"asset,omitempty"`
}

type certificateUsagePutRequest struct {
	AssetID int64 `json:"asset_id"`
	Enabled *bool `json:"enabled"`
}

func (s *Server) handleCertificateUsages(writer http.ResponseWriter, request *http.Request) {
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

	items, err := manager.listCertificateUsages(request.Context())
	if err != nil {
		writeError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"items": items})
}

func (s *Server) handleCertificateUsageResource(writer http.ResponseWriter, request *http.Request) {
	if !s.requireManagementSession(writer, request) {
		return
	}

	manager := s.requireManager(writer)
	if manager == nil {
		return
	}

	usageType, err := parseCertificateUsageTypeFromPath(request.URL.Path)
	if err != nil {
		writeError(writer, err)
		return
	}

	switch request.Method {
	case http.MethodPut:
		var payload certificateUsagePutRequest
		if err := decodeJSONBody(request, &payload); err != nil {
			writeError(writer, err)
			return
		}

		item, err := manager.upsertCertificateUsage(request.Context(), usageType, payload)
		if err != nil {
			writeError(writer, err)
			return
		}
		writeJSON(writer, http.StatusOK, map[string]any{"item": item})
	case http.MethodDelete:
		item, err := manager.deleteCertificateUsage(request.Context(), usageType)
		if err != nil {
			writeError(writer, err)
			return
		}
		writeJSON(writer, http.StatusOK, map[string]any{"item": item})
	default:
		writeMethodNotAllowed(writer)
	}
}

func parseCertificateUsageTypeFromPath(path string) (certusages.UsageType, error) {
	const prefix = "/api/v1/certificate-usages/"
	if !strings.HasPrefix(path, prefix) {
		return "", &apiError{Status: http.StatusNotFound, Message: "resource not found"}
	}

	value := strings.Trim(strings.TrimPrefix(path, prefix), "/")
	if strings.Contains(value, "/") || strings.TrimSpace(value) == "" {
		return "", &apiError{Status: http.StatusNotFound, Message: "resource not found"}
	}
	usageType := certusages.UsageType(strings.ToLower(strings.TrimSpace(value)))
	switch usageType {
	case certusages.UsageTypeWebUIHTTPS, certusages.UsageTypeControlListenerTLS:
		return usageType, nil
	default:
		return "", &apiError{Status: http.StatusNotFound, Message: "resource not found"}
	}
}

func (m *managementService) listCertificateUsages(ctx context.Context) ([]certificateUsageView, error) {
	if m == nil || m.usages == nil {
		return nil, &apiError{Status: http.StatusServiceUnavailable, Message: "certificate usage service is unavailable"}
	}
	items, err := m.usages.List(ctx)
	if err != nil {
		return nil, mapCertificateUsageError(err)
	}
	result := make([]certificateUsageView, 0, len(items))
	for _, item := range items {
		result = append(result, mapCertificateUsageView(item))
	}
	return result, nil
}

func (m *managementService) upsertCertificateUsage(ctx context.Context, usageType certusages.UsageType, payload certificateUsagePutRequest) (certificateUsageView, error) {
	if m == nil || m.usages == nil || m.server == nil {
		return certificateUsageView{}, &apiError{Status: http.StatusServiceUnavailable, Message: "certificate usage service is unavailable"}
	}

	enabled := true
	if payload.Enabled != nil {
		enabled = *payload.Enabled
	}
	if !enabled {
		return m.deleteCertificateUsage(ctx, usageType)
	}
	if payload.AssetID <= 0 {
		return certificateUsageView{}, &apiError{Status: http.StatusBadRequest, Message: "asset_id must be greater than zero"}
	}

	binding, err := m.usages.Resolve(ctx, usageType, payload.AssetID)
	if err != nil {
		return certificateUsageView{}, mapCertificateUsageError(err)
	}

	previous, previousExists, err := m.usages.LoadUsage(ctx, usageType)
	if err != nil {
		return certificateUsageView{}, mapCertificateUsageError(err)
	}

	if _, err := m.usages.SaveUsage(ctx, usageType, payload.AssetID, true); err != nil {
		return certificateUsageView{}, mapCertificateUsageError(err)
	}
	if err := m.applyCertificateUsageBinding(usageType, &binding); err != nil {
		_ = m.restorePreviousCertificateUsage(ctx, previousExists, previous)
		return certificateUsageView{}, mapCertificateUsageError(err)
	}

	item, err := m.usages.Describe(ctx, usageType)
	if err != nil {
		return certificateUsageView{}, mapCertificateUsageError(err)
	}
	return mapCertificateUsageView(item), nil
}

func (m *managementService) deleteCertificateUsage(ctx context.Context, usageType certusages.UsageType) (certificateUsageView, error) {
	if m == nil || m.usages == nil || m.server == nil {
		return certificateUsageView{}, &apiError{Status: http.StatusServiceUnavailable, Message: "certificate usage service is unavailable"}
	}

	if usageType == certusages.UsageTypeControlListenerTLS {
		count, err := m.countTLSRequiredProxyGroups(ctx)
		if err != nil {
			return certificateUsageView{}, err
		}
		if count > 0 {
			return certificateUsageView{}, &apiError{
				Status:  http.StatusConflict,
				Message: "frpc login tls certificate cannot be removed while proxy groups require tls",
				Code:    "control_listener_tls_in_use",
			}
		}
	}

	previous, previousExists, err := m.usages.LoadUsage(ctx, usageType)
	if err != nil {
		return certificateUsageView{}, mapCertificateUsageError(err)
	}
	if !previousExists {
		item, describeErr := m.usages.Describe(ctx, usageType)
		if describeErr != nil {
			return certificateUsageView{}, mapCertificateUsageError(describeErr)
		}
		return mapCertificateUsageView(item), nil
	}

	if err := m.usages.DeleteUsage(ctx, usageType); err != nil {
		return certificateUsageView{}, mapCertificateUsageError(err)
	}
	if err := m.clearCertificateUsageBinding(usageType); err != nil {
		_ = m.restorePreviousCertificateUsage(ctx, previousExists, previous)
		return certificateUsageView{}, mapCertificateUsageError(err)
	}

	item, err := m.usages.Describe(ctx, usageType)
	if err != nil {
		return certificateUsageView{}, mapCertificateUsageError(err)
	}
	return mapCertificateUsageView(item), nil
}

func (m *managementService) restorePreviousCertificateUsage(ctx context.Context, exists bool, usage certusages.Usage) error {
	if m == nil || m.usages == nil {
		return nil
	}
	if !exists {
		return m.usages.DeleteUsage(ctx, usage.UsageType)
	}
	return m.usages.RestoreUsage(ctx, usage)
}

func (m *managementService) applyCertificateUsageBinding(usageType certusages.UsageType, binding *certusages.ResolvedBinding) error {
	switch usageType {
	case certusages.UsageTypeWebUIHTTPS:
		return m.server.EnableWebUIHTTPS(binding)
	case certusages.UsageTypeControlListenerTLS:
		return m.server.ConfigureControlTLS(binding)
	default:
		return errors.New("unsupported certificate usage type")
	}
}

func (m *managementService) clearCertificateUsageBinding(usageType certusages.UsageType) error {
	switch usageType {
	case certusages.UsageTypeWebUIHTTPS:
		return m.server.DisableWebUIHTTPS()
	case certusages.UsageTypeControlListenerTLS:
		return m.server.ClearControlTLS()
	default:
		return errors.New("unsupported certificate usage type")
	}
}

func mapCertificateUsageView(item certusages.DescribedUsage) certificateUsageView {
	view := certificateUsageView{
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

func mapCertificateUsageError(err error) error {
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

func (m *managementService) activeCertificateUsageConflicts(ctx context.Context, assetIDs []int64) ([]certificateUsageView, error) {
	if m == nil || m.usages == nil || len(assetIDs) == 0 {
		return nil, nil
	}

	items, err := m.usages.List(ctx)
	if err != nil {
		return nil, mapCertificateUsageError(err)
	}
	seen := make(map[int64]struct{}, len(assetIDs))
	for _, assetID := range assetIDs {
		seen[assetID] = struct{}{}
	}

	conflicts := make([]certificateUsageView, 0)
	for _, item := range items {
		if item.AssetID == nil || !item.Enabled {
			continue
		}
		if _, ok := seen[*item.AssetID]; !ok {
			continue
		}
		conflicts = append(conflicts, mapCertificateUsageView(item))
	}
	return conflicts, nil
}
