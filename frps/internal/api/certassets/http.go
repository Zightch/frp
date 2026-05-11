package certassets

import (
	"net/http"

	"github.com/zightch/frp/frps/internal/api/httpx"
)

const Path = "/api/v1/certificate-assets"

type SessionGuard func(http.ResponseWriter, *http.Request) bool

type AssetView struct {
	ID            int64    `json:"id"`
	Name          string   `json:"name"`
	Remark        string   `json:"remark"`
	Source        string   `json:"source"`
	AssetType     string   `json:"asset_type"`
	FormatType    string   `json:"format_type"`
	IssuerAssetID *int64   `json:"issuer_asset_id,omitempty"`
	IssuerName    string   `json:"issuer_name,omitempty"`
	CommonName    string   `json:"common_name,omitempty"`
	Subject       string   `json:"subject,omitempty"`
	Issuer        string   `json:"issuer,omitempty"`
	SerialNumber  string   `json:"serial_number,omitempty"`
	NotBefore     string   `json:"not_before,omitempty"`
	NotAfter      string   `json:"not_after,omitempty"`
	DNSNames      []string `json:"dns_names,omitempty"`
	IPAddresses   []string `json:"ip_addresses,omitempty"`
	KeyPresent    bool     `json:"key_present"`
	CanIssue      bool     `json:"can_issue"`
	IsSelfSigned  bool     `json:"is_self_signed"`
	ChainLength   int      `json:"chain_length"`
	Status        string   `json:"status"`
	StatusReason  string   `json:"status_reason,omitempty"`
	CreatedAt     string   `json:"created_at"`
	UpdatedAt     string   `json:"updated_at"`
}

type pasteRequest struct {
	Name   string `json:"name"`
	Remark string `json:"remark"`
	CRT    string `json:"crt"`
	Key    string `json:"key"`
}

type generateRequest struct {
	Name          string   `json:"name"`
	Remark        string   `json:"remark"`
	AssetType     string   `json:"asset_type"`
	IssuerAssetID *int64   `json:"issuer_asset_id"`
	CommonName    string   `json:"common_name"`
	ValidityDays  int      `json:"validity_days"`
	DNSNames      []string `json:"dns_names"`
	IPAddresses   []string `json:"ip_addresses"`
	KeyAlgorithm  string   `json:"key_algorithm"`
	KeyBits       int      `json:"key_bits"`
}

type patchRequest struct {
	Name   string `json:"name"`
	Remark string `json:"remark"`
}

type deleteImpactView struct {
	Target               AssetView              `json:"target"`
	AffectedItems        []deleteImpactItemView `json:"affected_items"`
	UsageItems           []entryCertificateView `json:"usage_items,omitempty"`
	RequiresConfirmation bool                   `json:"requires_confirmation"`
	WarningMessage       string                 `json:"warning_message,omitempty"`
}

type deleteImpactItemView struct {
	Item  AssetView `json:"item"`
	Depth int       `json:"depth"`
}

type downloadOptionsView struct {
	Target     AssetView           `json:"target"`
	Modes      []downloadModeView  `json:"modes"`
	ChainItems []downloadChainView `json:"chain_items,omitempty"`
	TreeItems  []downloadTreeView  `json:"tree_items,omitempty"`
}

type downloadModeView struct {
	Mode    string `json:"mode"`
	Default bool   `json:"default"`
}

type downloadChainView struct {
	Item  AssetView `json:"item"`
	Depth int       `json:"depth"`
}

type downloadTreeView struct {
	Item          AssetView `json:"item"`
	ParentAssetID *int64    `json:"parent_asset_id,omitempty"`
	Depth         int       `json:"depth"`
}

type entryCertificateView struct {
	UsageType           string     `json:"usage_type"`
	AssetID             *int64     `json:"asset_id,omitempty"`
	AssetName           string     `json:"asset_name,omitempty"`
	Enabled             bool       `json:"enabled"`
	Status              string     `json:"status"`
	StatusReason        string     `json:"status_reason,omitempty"`
	ResolvedChainLength int        `json:"resolved_chain_length"`
	UpdatedAt           string     `json:"updated_at,omitempty"`
	Asset               *AssetView `json:"asset,omitempty"`
}

type Handler struct {
	service        *Service
	requireSession SessionGuard
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

	mux.HandleFunc(Path, h.handleCollection)
	mux.HandleFunc(Path+"/upload", h.handleUpload)
	mux.HandleFunc(Path+"/paste", h.handlePaste)
	mux.HandleFunc(Path+"/generate", h.handleGenerate)
	mux.HandleFunc(Path+"/", h.handleResource)
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

	items, err := service.ListAssets(request.Context())
	if err != nil {
		httpx.WriteError(writer, err)
		return
	}
	httpx.WriteJSON(writer, http.StatusOK, map[string]any{"items": items})
}

func (h *Handler) handleUpload(writer http.ResponseWriter, request *http.Request) {
	if !h.requireManagementSession(writer, request) {
		return
	}

	service := h.requireService(writer)
	if service == nil {
		return
	}

	if request.Method != http.MethodPost {
		httpx.WriteMethodNotAllowed(writer)
		return
	}

	item, err := service.UploadAsset(request)
	if err != nil {
		httpx.WriteError(writer, err)
		return
	}
	httpx.WriteJSON(writer, http.StatusCreated, map[string]any{"item": item})
}

func (h *Handler) handlePaste(writer http.ResponseWriter, request *http.Request) {
	if !h.requireManagementSession(writer, request) {
		return
	}

	service := h.requireService(writer)
	if service == nil {
		return
	}

	if request.Method != http.MethodPost {
		httpx.WriteMethodNotAllowed(writer)
		return
	}

	var payload pasteRequest
	if err := httpx.DecodeJSONBody(request, &payload); err != nil {
		httpx.WriteError(writer, err)
		return
	}

	item, err := service.PasteAsset(request.Context(), payload)
	if err != nil {
		httpx.WriteError(writer, err)
		return
	}
	httpx.WriteJSON(writer, http.StatusCreated, map[string]any{"item": item})
}

func (h *Handler) handleGenerate(writer http.ResponseWriter, request *http.Request) {
	if !h.requireManagementSession(writer, request) {
		return
	}

	service := h.requireService(writer)
	if service == nil {
		return
	}

	if request.Method != http.MethodPost {
		httpx.WriteMethodNotAllowed(writer)
		return
	}

	var payload generateRequest
	if err := httpx.DecodeJSONBody(request, &payload); err != nil {
		httpx.WriteError(writer, err)
		return
	}

	item, err := service.GenerateAsset(request.Context(), payload)
	if err != nil {
		httpx.WriteError(writer, err)
		return
	}
	httpx.WriteJSON(writer, http.StatusCreated, map[string]any{"item": item})
}

func (h *Handler) handleResource(writer http.ResponseWriter, request *http.Request) {
	if !h.requireManagementSession(writer, request) {
		return
	}

	service := h.requireService(writer)
	if service == nil {
		return
	}

	id, suffix, err := httpx.ParseResourcePath(request.URL.Path, Path+"/")
	if err != nil {
		httpx.WriteError(writer, err)
		return
	}

	switch {
	case suffix == "delete-impact" && request.Method == http.MethodGet:
		impact, err := service.DeleteImpact(request.Context(), id)
		if err != nil {
			httpx.WriteError(writer, err)
			return
		}
		httpx.WriteJSON(writer, http.StatusOK, impact)
	case suffix == "download-options" && request.Method == http.MethodGet:
		options, err := service.DownloadOptions(request.Context(), id)
		if err != nil {
			httpx.WriteError(writer, err)
			return
		}
		httpx.WriteJSON(writer, http.StatusOK, options)
	case suffix == "download" && request.Method == http.MethodGet:
		downloadRequest, err := parseDownloadRequest(request)
		if err != nil {
			httpx.WriteError(writer, err)
			return
		}

		artifact, err := service.DownloadAsset(request.Context(), id, downloadRequest)
		if err != nil {
			httpx.WriteError(writer, err)
			return
		}
		writeDownload(writer, artifact)
	case suffix == "" && request.Method == http.MethodPatch:
		var payload patchRequest
		if err := httpx.DecodeJSONBody(request, &payload); err != nil {
			httpx.WriteError(writer, err)
			return
		}

		item, err := service.UpdateAsset(request.Context(), id, payload)
		if err != nil {
			httpx.WriteError(writer, err)
			return
		}
		httpx.WriteJSON(writer, http.StatusOK, map[string]any{"item": item})
	case suffix == "" && request.Method == http.MethodDelete:
		result, err := service.DeleteAsset(request.Context(), id, parseBoolQueryValue(request.URL.Query().Get("cascade")))
		if err != nil {
			httpx.WriteError(writer, err)
			return
		}
		httpx.WriteJSON(writer, http.StatusOK, map[string]any{
			"deleted":     true,
			"deleted_ids": result.DeletedIDs,
		})
	default:
		httpx.WriteMethodNotAllowed(writer)
	}
}

func (h *Handler) requireManagementSession(writer http.ResponseWriter, request *http.Request) bool {
	if h == nil || h.requireSession == nil {
		return true
	}
	return h.requireSession(writer, request)
}

func (h *Handler) requireService(writer http.ResponseWriter) *Service {
	if h == nil || h.service == nil {
		httpx.WriteError(writer, &httpx.Error{Status: http.StatusServiceUnavailable, Message: "certificate asset service is unavailable"})
		return nil
	}
	return h.service
}
