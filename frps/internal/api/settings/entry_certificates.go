package settings

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"strings"

	apicertassets "github.com/zightch/frp/frps/internal/api/certassets"
	"github.com/zightch/frp/frps/internal/api/httpx"
	"github.com/zightch/frp/frps/internal/settings/entrycerts"
	"github.com/zightch/frp/frps/internal/storage"
)

const EntryCertificatesPath = "/api/v1/settings/entry-certificates"

const schemaTimestampLayout = "2006-01-02 15:04:05.000000"

type SessionGuard func(http.ResponseWriter, *http.Request) bool

type TLSRequiredGroupCounter interface {
	CountTLSRequiredProxyGroups(context.Context) (int64, error)
}

type Runtime interface {
	EnableWebUIHTTPS(binding *entrycerts.ResolvedBinding) error
	DisableWebUIHTTPS() error
	ConfigureControlTLS(binding *entrycerts.ResolvedBinding) error
	ClearControlTLS() error
}

type Options struct {
	Store             *storage.SQL
	TLSRequiredGroups TLSRequiredGroupCounter
	Runtime           Runtime
}

type Service struct {
	entryCerts        *entrycerts.Service
	tlsRequiredGroups TLSRequiredGroupCounter
	runtime           Runtime
}

type entryCertificateView struct {
	UsageType           string                   `json:"usage_type"`
	AssetID             *int64                   `json:"asset_id,omitempty"`
	AssetName           string                   `json:"asset_name,omitempty"`
	Enabled             bool                     `json:"enabled"`
	Status              string                   `json:"status"`
	StatusReason        string                   `json:"status_reason,omitempty"`
	ResolvedChainLength int                      `json:"resolved_chain_length"`
	UpdatedAt           string                   `json:"updated_at,omitempty"`
	Asset               *apicertassets.AssetView `json:"asset,omitempty"`
}

type putRequest struct {
	AssetID int64 `json:"asset_id"`
	Enabled *bool `json:"enabled"`
}

type parsedPath struct {
	UsageType entrycerts.UsageType
}

type Handler struct {
	service        *Service
	requireSession SessionGuard
}

func NewService(options Options) *Service {
	if options.Store == nil {
		return nil
	}

	return &Service{
		entryCerts:        entrycerts.NewService(options.Store, entrycerts.ServiceOptions{}),
		tlsRequiredGroups: options.TLSRequiredGroups,
		runtime:           options.Runtime,
	}
}

func NewHandler(service *Service, requireSession SessionGuard) *Handler {
	return &Handler{
		service:        service,
		requireSession: requireSession,
	}
}

func (h *Handler) RegisterRoutes(mux *http.ServeMux) {
	if h == nil || mux == nil {
		return
	}

	mux.HandleFunc(EntryCertificatesPath, h.handleCollection)
	mux.HandleFunc(EntryCertificatesPath+"/", h.handleResource)
}

func (h *Handler) handleCollection(writer http.ResponseWriter, request *http.Request) {
	if !h.requireManagementSession(writer, request) {
		return
	}

	service := h.requireService(writer)
	if service == nil {
		return
	}

	if request.Method != http.MethodGet {
		httpx.WriteMethodNotAllowed(writer)
		return
	}

	items, err := service.ListEntryCertificates(request.Context())
	if err != nil {
		httpx.WriteError(writer, err)
		return
	}
	httpx.WriteJSON(writer, http.StatusOK, map[string]any{"items": items})
}

func (h *Handler) handleResource(writer http.ResponseWriter, request *http.Request) {
	if !h.requireManagementSession(writer, request) {
		return
	}

	service := h.requireService(writer)
	if service == nil {
		return
	}

	pathInfo, err := parseUsageTypeFromPath(request.URL.Path)
	if err != nil {
		httpx.WriteError(writer, err)
		return
	}

	switch request.Method {
	case http.MethodPut:
		var payload putRequest
		if err := httpx.DecodeJSONBody(request, &payload); err != nil {
			httpx.WriteError(writer, err)
			return
		}

		item, err := service.UpsertEntryCertificate(request.Context(), pathInfo.UsageType, payload)
		if err != nil {
			httpx.WriteError(writer, err)
			return
		}
		httpx.WriteJSON(writer, http.StatusOK, map[string]any{"item": item})
	case http.MethodDelete:
		item, err := service.DeleteEntryCertificate(request.Context(), pathInfo.UsageType)
		if err != nil {
			httpx.WriteError(writer, err)
			return
		}
		httpx.WriteJSON(writer, http.StatusOK, map[string]any{"item": item})
	default:
		httpx.WriteMethodNotAllowed(writer)
	}
}

func (s *Service) ListEntryCertificates(ctx context.Context) ([]entryCertificateView, error) {
	if s == nil || s.entryCerts == nil {
		return nil, &httpx.Error{Status: http.StatusServiceUnavailable, Message: "entry certificate service is unavailable"}
	}

	items, err := s.entryCerts.List(ctx)
	if err != nil {
		return nil, mapEntryCertificateError(err)
	}

	result := make([]entryCertificateView, 0, len(items))
	for _, item := range items {
		result = append(result, mapEntryCertificateView(item))
	}
	return result, nil
}

func (s *Service) UpsertEntryCertificate(ctx context.Context, usageType entrycerts.UsageType, payload putRequest) (entryCertificateView, error) {
	if s == nil || s.entryCerts == nil || s.runtime == nil {
		return entryCertificateView{}, &httpx.Error{Status: http.StatusServiceUnavailable, Message: "entry certificate service is unavailable"}
	}

	enabled := true
	if payload.Enabled != nil {
		enabled = *payload.Enabled
	}
	if !enabled {
		return s.DeleteEntryCertificate(ctx, usageType)
	}
	if payload.AssetID <= 0 {
		return entryCertificateView{}, &httpx.Error{Status: http.StatusBadRequest, Message: "asset_id must be greater than zero"}
	}

	binding, err := s.entryCerts.Resolve(ctx, usageType, payload.AssetID)
	if err != nil {
		return entryCertificateView{}, mapEntryCertificateError(err)
	}

	previous, previousExists, err := s.entryCerts.LoadUsage(ctx, usageType)
	if err != nil {
		return entryCertificateView{}, mapEntryCertificateError(err)
	}

	if _, err := s.entryCerts.SaveUsage(ctx, usageType, payload.AssetID, true); err != nil {
		return entryCertificateView{}, mapEntryCertificateError(err)
	}
	if err := s.applyBinding(usageType, &binding); err != nil {
		_ = s.restorePreviousEntryCertificate(ctx, previousExists, previous)
		return entryCertificateView{}, mapEntryCertificateError(err)
	}

	item, err := s.entryCerts.Describe(ctx, usageType)
	if err != nil {
		return entryCertificateView{}, mapEntryCertificateError(err)
	}
	return mapEntryCertificateView(item), nil
}

func (s *Service) DeleteEntryCertificate(ctx context.Context, usageType entrycerts.UsageType) (entryCertificateView, error) {
	if s == nil || s.entryCerts == nil || s.runtime == nil {
		return entryCertificateView{}, &httpx.Error{Status: http.StatusServiceUnavailable, Message: "entry certificate service is unavailable"}
	}

	if usageType == entrycerts.UsageTypeFrpcTLS {
		if s.tlsRequiredGroups == nil {
			return entryCertificateView{}, &httpx.Error{Status: http.StatusServiceUnavailable, Message: "proxy group service is unavailable"}
		}

		count, err := s.tlsRequiredGroups.CountTLSRequiredProxyGroups(ctx)
		if err != nil {
			return entryCertificateView{}, err
		}
		if count > 0 {
			return entryCertificateView{}, &httpx.Error{
				Status:  http.StatusConflict,
				Message: "frpc tls entry certificate cannot be removed while frpc tls is enabled for proxy groups",
				Code:    "frpc_tls_in_use",
			}
		}
	}

	previous, previousExists, err := s.entryCerts.LoadUsage(ctx, usageType)
	if err != nil {
		return entryCertificateView{}, mapEntryCertificateError(err)
	}
	if !previousExists {
		item, describeErr := s.entryCerts.Describe(ctx, usageType)
		if describeErr != nil {
			return entryCertificateView{}, mapEntryCertificateError(describeErr)
		}
		return mapEntryCertificateView(item), nil
	}

	if err := s.entryCerts.DeleteUsage(ctx, usageType); err != nil {
		return entryCertificateView{}, mapEntryCertificateError(err)
	}
	if err := s.clearBinding(usageType); err != nil {
		_ = s.restorePreviousEntryCertificate(ctx, previousExists, previous)
		return entryCertificateView{}, mapEntryCertificateError(err)
	}

	item, err := s.entryCerts.Describe(ctx, usageType)
	if err != nil {
		return entryCertificateView{}, mapEntryCertificateError(err)
	}
	return mapEntryCertificateView(item), nil
}

func parseUsageTypeFromPath(path string) (parsedPath, error) {
	value, err := trimResourcePath(path)
	if err != nil {
		return parsedPath{}, err
	}
	if strings.Contains(value, "/") || strings.TrimSpace(value) == "" {
		return parsedPath{}, &httpx.Error{Status: http.StatusNotFound, Message: "resource not found"}
	}

	switch usageType := entrycerts.NormalizeUsageType(value); usageType {
	case entrycerts.UsageTypeWebUIHTTPS, entrycerts.UsageTypeFrpcTLS:
		return parsedPath{UsageType: usageType}, nil
	default:
		return parsedPath{}, &httpx.Error{Status: http.StatusNotFound, Message: "resource not found"}
	}
}

func trimResourcePath(path string) (string, error) {
	prefix := EntryCertificatesPath + "/"
	if strings.HasPrefix(path, prefix) {
		return strings.Trim(strings.TrimPrefix(path, prefix), "/"), nil
	}
	return "", &httpx.Error{Status: http.StatusNotFound, Message: "resource not found"}
}

func (s *Service) restorePreviousEntryCertificate(ctx context.Context, exists bool, usage entrycerts.Usage) error {
	if s == nil || s.entryCerts == nil {
		return nil
	}
	if !exists {
		return s.entryCerts.DeleteUsage(ctx, usage.UsageType)
	}
	return s.entryCerts.RestoreUsage(ctx, usage)
}

func (s *Service) applyBinding(usageType entrycerts.UsageType, binding *entrycerts.ResolvedBinding) error {
	switch usageType {
	case entrycerts.UsageTypeWebUIHTTPS:
		return s.runtime.EnableWebUIHTTPS(binding)
	case entrycerts.UsageTypeFrpcTLS:
		return s.runtime.ConfigureControlTLS(binding)
	default:
		return errors.New("unsupported entry certificate type")
	}
}

func (s *Service) clearBinding(usageType entrycerts.UsageType) error {
	switch usageType {
	case entrycerts.UsageTypeWebUIHTTPS:
		return s.runtime.DisableWebUIHTTPS()
	case entrycerts.UsageTypeFrpcTLS:
		return s.runtime.ClearControlTLS()
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
		assetView := apicertassets.MapAssetView(*item.Asset)
		view.Asset = &assetView
	}
	return view
}

func mapEntryCertificateError(err error) error {
	if err == nil {
		return nil
	}

	if errors.Is(err, sql.ErrNoRows) {
		return &httpx.Error{
			Status:  http.StatusNotFound,
			Message: "certificate asset not found",
			Code:    "certificate_asset_not_found",
		}
	}

	if strings.Contains(err.Error(), "bound asset must be a certificate") ||
		strings.Contains(err.Error(), "bound asset must include a private key") ||
		strings.Contains(err.Error(), "asset_id must be greater than zero") {
		return &httpx.Error{
			Status:  http.StatusUnprocessableEntity,
			Message: err.Error(),
			Code:    "certificate_usage_validation_failed",
		}
	}

	return err
}

func (h *Handler) requireManagementSession(writer http.ResponseWriter, request *http.Request) bool {
	if h == nil || h.requireSession == nil {
		return true
	}
	return h.requireSession(writer, request)
}

func (h *Handler) requireService(writer http.ResponseWriter) *Service {
	if h == nil || h.service == nil {
		httpx.WriteError(writer, &httpx.Error{Status: http.StatusServiceUnavailable, Message: "entry certificate service is unavailable"})
		return nil
	}
	return h.service
}
