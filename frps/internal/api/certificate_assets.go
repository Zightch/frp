package api

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"strconv"
	"strings"

	"github.com/zightch/frp/frps/internal/certassets"
)

const certificateAssetUploadMaxMemory = 4 << 20

type certificateAssetView struct {
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
	CreatedAt     string   `json:"created_at"`
	UpdatedAt     string   `json:"updated_at"`
}

type certificateAssetPasteRequest struct {
	Name   string `json:"name"`
	Remark string `json:"remark"`
	CRT    string `json:"crt"`
	Key    string `json:"key"`
}

type certificateAssetGenerateRequest struct {
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

type certificateAssetPatchRequest struct {
	Name   string `json:"name"`
	Remark string `json:"remark"`
}

type certificateAssetDeleteImpactView struct {
	Target               certificateAssetView             `json:"target"`
	AffectedItems        []certificateAssetDeleteItemView `json:"affected_items"`
	RequiresConfirmation bool                             `json:"requires_confirmation"`
	WarningMessage       string                           `json:"warning_message,omitempty"`
}

type certificateAssetDeleteItemView struct {
	Item  certificateAssetView `json:"item"`
	Depth int                  `json:"depth"`
}

type certificateAssetDownloadOptionsView struct {
	Target     certificateAssetView                `json:"target"`
	Modes      []certificateAssetDownloadModeView  `json:"modes"`
	ChainItems []certificateAssetDownloadChainView `json:"chain_items,omitempty"`
	TreeItems  []certificateAssetDownloadTreeView  `json:"tree_items,omitempty"`
}

type certificateAssetDownloadModeView struct {
	Mode    string `json:"mode"`
	Default bool   `json:"default"`
}

type certificateAssetDownloadChainView struct {
	Item  certificateAssetView `json:"item"`
	Depth int                  `json:"depth"`
}

type certificateAssetDownloadTreeView struct {
	Item          certificateAssetView `json:"item"`
	ParentAssetID *int64               `json:"parent_asset_id,omitempty"`
	Depth         int                  `json:"depth"`
}

func (s *Server) handleCertificateAssets(writer http.ResponseWriter, request *http.Request) {
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

	items, err := manager.listCertificateAssets(request.Context())
	if err != nil {
		writeError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"items": items})
}

func (s *Server) handleCertificateAssetUpload(writer http.ResponseWriter, request *http.Request) {
	if !s.requireManagementSession(writer, request) {
		return
	}

	manager := s.requireManager(writer)
	if manager == nil {
		return
	}

	if request.Method != http.MethodPost {
		writeMethodNotAllowed(writer)
		return
	}

	item, err := manager.uploadCertificateAsset(request)
	if err != nil {
		writeError(writer, err)
		return
	}
	writeJSON(writer, http.StatusCreated, map[string]any{"item": item})
}

func (s *Server) handleCertificateAssetPaste(writer http.ResponseWriter, request *http.Request) {
	if !s.requireManagementSession(writer, request) {
		return
	}

	manager := s.requireManager(writer)
	if manager == nil {
		return
	}

	if request.Method != http.MethodPost {
		writeMethodNotAllowed(writer)
		return
	}

	var payload certificateAssetPasteRequest
	if err := decodeJSONBody(request, &payload); err != nil {
		writeError(writer, err)
		return
	}

	item, err := manager.pasteCertificateAsset(request.Context(), payload)
	if err != nil {
		writeError(writer, err)
		return
	}
	writeJSON(writer, http.StatusCreated, map[string]any{"item": item})
}

func (s *Server) handleCertificateAssetGenerate(writer http.ResponseWriter, request *http.Request) {
	if !s.requireManagementSession(writer, request) {
		return
	}

	manager := s.requireManager(writer)
	if manager == nil {
		return
	}

	if request.Method != http.MethodPost {
		writeMethodNotAllowed(writer)
		return
	}

	var payload certificateAssetGenerateRequest
	if err := decodeJSONBody(request, &payload); err != nil {
		writeError(writer, err)
		return
	}

	item, err := manager.generateCertificateAsset(request.Context(), payload)
	if err != nil {
		writeError(writer, err)
		return
	}
	writeJSON(writer, http.StatusCreated, map[string]any{"item": item})
}

func (s *Server) handleCertificateAssetResource(writer http.ResponseWriter, request *http.Request) {
	if !s.requireManagementSession(writer, request) {
		return
	}

	manager := s.requireManager(writer)
	if manager == nil {
		return
	}

	id, suffix, err := parseResourcePath(request.URL.Path, "/api/v1/certificate-assets/")
	if err != nil {
		writeError(writer, err)
		return
	}

	switch {
	case suffix == "delete-impact" && request.Method == http.MethodGet:
		impact, err := manager.getCertificateAssetDeleteImpact(request.Context(), id)
		if err != nil {
			writeError(writer, err)
			return
		}
		writeJSON(writer, http.StatusOK, impact)
	case suffix == "download-options" && request.Method == http.MethodGet:
		options, err := manager.getCertificateAssetDownloadOptions(request.Context(), id)
		if err != nil {
			writeError(writer, err)
			return
		}
		writeJSON(writer, http.StatusOK, options)
	case suffix == "download" && request.Method == http.MethodGet:
		downloadRequest, err := parseCertificateAssetDownloadRequest(request)
		if err != nil {
			writeError(writer, err)
			return
		}

		artifact, err := manager.downloadCertificateAsset(request.Context(), id, downloadRequest)
		if err != nil {
			writeError(writer, err)
			return
		}
		writeCertificateAssetDownload(writer, artifact)
	case suffix == "" && request.Method == http.MethodPatch:
		var payload certificateAssetPatchRequest
		if err := decodeJSONBody(request, &payload); err != nil {
			writeError(writer, err)
			return
		}

		item, err := manager.updateCertificateAsset(request.Context(), id, payload)
		if err != nil {
			writeError(writer, err)
			return
		}
		writeJSON(writer, http.StatusOK, map[string]any{"item": item})
	case suffix == "" && request.Method == http.MethodDelete:
		cascade := parseBoolQueryValue(request.URL.Query().Get("cascade"))
		result, err := manager.deleteCertificateAsset(request.Context(), id, cascade)
		if err != nil {
			writeError(writer, err)
			return
		}
		writeJSON(writer, http.StatusOK, map[string]any{
			"deleted":     true,
			"deleted_ids": result.DeletedIDs,
		})
	default:
		writeMethodNotAllowed(writer)
	}
}

func (m *managementService) listCertificateAssets(ctx context.Context) ([]certificateAssetView, error) {
	if m == nil || m.certs == nil {
		return nil, &apiError{Status: http.StatusServiceUnavailable, Message: "certificate asset service is unavailable"}
	}

	items, err := m.certs.List(ctx)
	if err != nil {
		return nil, mapCertificateAssetError(err)
	}
	return mapCertificateAssetViews(items), nil
}

func (m *managementService) uploadCertificateAsset(request *http.Request) (certificateAssetView, error) {
	if m == nil || m.certs == nil {
		return certificateAssetView{}, &apiError{Status: http.StatusServiceUnavailable, Message: "certificate asset service is unavailable"}
	}

	if err := request.ParseMultipartForm(certificateAssetUploadMaxMemory); err != nil {
		return certificateAssetView{}, &apiError{
			Status:  http.StatusBadRequest,
			Message: fmt.Sprintf("invalid multipart form: %v", err),
			Code:    "certificate_asset_invalid_multipart",
		}
	}

	item, err := m.certs.Import(request.Context(), certassets.CreateInput{
		Name:   strings.TrimSpace(request.FormValue("name")),
		Remark: strings.TrimSpace(request.FormValue("remark")),
		Source: certassets.SourceUpload,
		CRT:    readUploadedFormFile(request, "crt"),
		Key:    readUploadedFormFile(request, "key"),
	})
	if err != nil {
		return certificateAssetView{}, mapCertificateAssetError(err)
	}
	return mapCertificateAssetView(item), nil
}

func (m *managementService) pasteCertificateAsset(ctx context.Context, payload certificateAssetPasteRequest) (certificateAssetView, error) {
	if m == nil || m.certs == nil {
		return certificateAssetView{}, &apiError{Status: http.StatusServiceUnavailable, Message: "certificate asset service is unavailable"}
	}

	item, err := m.certs.Import(ctx, certassets.CreateInput{
		Name:   payload.Name,
		Remark: payload.Remark,
		Source: certassets.SourceUpload,
		CRT:    payload.CRT,
		Key:    payload.Key,
	})
	if err != nil {
		return certificateAssetView{}, mapCertificateAssetError(err)
	}
	return mapCertificateAssetView(item), nil
}

func (m *managementService) generateCertificateAsset(ctx context.Context, payload certificateAssetGenerateRequest) (certificateAssetView, error) {
	if m == nil || m.certs == nil {
		return certificateAssetView{}, &apiError{Status: http.StatusServiceUnavailable, Message: "certificate asset service is unavailable"}
	}

	item, err := m.certs.Generate(ctx, certassets.GenerateInput{
		Name:          payload.Name,
		Remark:        payload.Remark,
		AssetType:     certassets.AssetType(strings.ToLower(strings.TrimSpace(payload.AssetType))),
		IssuerAssetID: payload.IssuerAssetID,
		CommonName:    payload.CommonName,
		ValidityDays:  payload.ValidityDays,
		DNSNames:      payload.DNSNames,
		IPAddresses:   payload.IPAddresses,
		KeyAlgorithm:  certassets.GenerateKeyAlgorithm(payload.KeyAlgorithm),
		KeyBits:       payload.KeyBits,
	})
	if err != nil {
		return certificateAssetView{}, mapCertificateAssetError(err)
	}
	return mapCertificateAssetView(item), nil
}

func (m *managementService) updateCertificateAsset(ctx context.Context, id int64, payload certificateAssetPatchRequest) (certificateAssetView, error) {
	if m == nil || m.certs == nil {
		return certificateAssetView{}, &apiError{Status: http.StatusServiceUnavailable, Message: "certificate asset service is unavailable"}
	}

	item, err := m.certs.UpdateMetadata(ctx, id, certassets.UpdateMetadataInput{
		Name:   payload.Name,
		Remark: payload.Remark,
	})
	if err != nil {
		return certificateAssetView{}, mapCertificateAssetError(err)
	}
	return mapCertificateAssetView(item), nil
}

func (m *managementService) getCertificateAssetDeleteImpact(ctx context.Context, id int64) (certificateAssetDeleteImpactView, error) {
	if m == nil || m.certs == nil {
		return certificateAssetDeleteImpactView{}, &apiError{Status: http.StatusServiceUnavailable, Message: "certificate asset service is unavailable"}
	}

	impact, err := m.certs.DeleteImpact(ctx, id)
	if err != nil {
		return certificateAssetDeleteImpactView{}, mapCertificateAssetError(err)
	}
	return mapCertificateAssetDeleteImpactView(impact), nil
}

func (m *managementService) deleteCertificateAsset(ctx context.Context, id int64, cascade bool) (certassets.DeleteResult, error) {
	if m == nil || m.certs == nil {
		return certassets.DeleteResult{}, &apiError{Status: http.StatusServiceUnavailable, Message: "certificate asset service is unavailable"}
	}

	result, err := m.certs.Delete(ctx, id, cascade)
	if err != nil {
		return certassets.DeleteResult{}, mapCertificateAssetError(err)
	}
	return result, nil
}

func (m *managementService) getCertificateAssetDownloadOptions(ctx context.Context, id int64) (certificateAssetDownloadOptionsView, error) {
	if m == nil || m.certs == nil {
		return certificateAssetDownloadOptionsView{}, &apiError{Status: http.StatusServiceUnavailable, Message: "certificate asset service is unavailable"}
	}

	options, err := m.certs.DownloadOptions(ctx, id)
	if err != nil {
		return certificateAssetDownloadOptionsView{}, mapCertificateAssetError(err)
	}
	return mapCertificateAssetDownloadOptionsView(options), nil
}

func (m *managementService) downloadCertificateAsset(ctx context.Context, id int64, request certassets.DownloadRequest) (certassets.DownloadArtifact, error) {
	if m == nil || m.certs == nil {
		return certassets.DownloadArtifact{}, &apiError{Status: http.StatusServiceUnavailable, Message: "certificate asset service is unavailable"}
	}

	artifact, err := m.certs.Download(ctx, id, request)
	if err != nil {
		return certassets.DownloadArtifact{}, mapCertificateAssetError(err)
	}
	return artifact, nil
}

func mapCertificateAssetError(err error) error {
	if err == nil {
		return nil
	}

	var validationErr *certassets.ValidationError
	if errors.As(err, &validationErr) {
		return &apiError{
			Status:  http.StatusUnprocessableEntity,
			Message: validationErr.Error(),
			Code:    "certificate_asset_validation_failed",
			Details: map[string]any{
				"issues": validationErr.Issues,
			},
		}
	}

	var nameConflict *certassets.NameConflictError
	if errors.As(err, &nameConflict) {
		return &apiError{
			Status:  http.StatusConflict,
			Message: nameConflict.Error(),
			Code:    "certificate_asset_name_conflict",
		}
	}

	var duplicateErr *certassets.DuplicateContentError
	if errors.As(err, &duplicateErr) {
		return &apiError{
			Status:  http.StatusConflict,
			Message: duplicateErr.Error(),
			Code:    "certificate_asset_duplicate_content",
			Details: map[string]any{
				"duplicates": duplicateErr.Duplicates,
			},
		}
	}

	var deleteConfirmErr *certassets.DeleteRequiresConfirmationError
	if errors.As(err, &deleteConfirmErr) {
		return &apiError{
			Status:  http.StatusConflict,
			Message: "删除该证书资产会影响其他证书资产",
			Code:    "certificate_asset_delete_requires_confirmation",
			Details: map[string]any{
				"impact": mapCertificateAssetDeleteImpactView(deleteConfirmErr.Impact),
			},
		}
	}

	if errors.Is(err, sql.ErrNoRows) {
		return &apiError{
			Status:  http.StatusNotFound,
			Message: "certificate asset not found",
			Code:    "certificate_asset_not_found",
		}
	}

	if isUniqueConstraintError(err) {
		return &apiError{
			Status:  http.StatusConflict,
			Message: "certificate asset name already exists",
			Code:    "certificate_asset_name_conflict",
		}
	}

	return err
}

func mapCertificateAssetViews(items []certassets.DescribedAsset) []certificateAssetView {
	if len(items) == 0 {
		return []certificateAssetView{}
	}
	result := make([]certificateAssetView, 0, len(items))
	for _, item := range items {
		result = append(result, mapCertificateAssetView(item))
	}
	return result
}

func mapCertificateAssetView(item certassets.DescribedAsset) certificateAssetView {
	return certificateAssetView{
		ID:            item.ID,
		Name:          item.Name,
		Remark:        item.Remark,
		Source:        string(item.Source),
		AssetType:     string(item.AssetType),
		FormatType:    string(item.FormatType),
		IssuerAssetID: item.IssuerAssetID,
		IssuerName:    item.IssuerName,
		CommonName:    item.CommonName,
		Subject:       item.Subject,
		Issuer:        item.Issuer,
		SerialNumber:  item.SerialNumber,
		NotBefore:     item.NotBefore,
		NotAfter:      item.NotAfter,
		DNSNames:      append([]string(nil), item.DNSNames...),
		IPAddresses:   append([]string(nil), item.IPAddresses...),
		KeyPresent:    item.KeyPresent,
		CanIssue:      item.CanIssue,
		IsSelfSigned:  item.IsSelfSigned,
		ChainLength:   item.ChainLength,
		CreatedAt:     item.CreatedAt.UTC().Format(schemaTimestampLayout),
		UpdatedAt:     item.UpdatedAt.UTC().Format(schemaTimestampLayout),
	}
}

func mapCertificateAssetDeleteImpactView(impact certassets.DeleteImpact) certificateAssetDeleteImpactView {
	items := make([]certificateAssetDeleteItemView, 0, len(impact.Affected))
	for _, item := range impact.Affected {
		items = append(items, certificateAssetDeleteItemView{
			Item:  mapCertificateAssetView(item.Item),
			Depth: item.Depth,
		})
	}

	view := certificateAssetDeleteImpactView{
		Target:               mapCertificateAssetView(impact.Target),
		AffectedItems:        items,
		RequiresConfirmation: len(items) > 0,
	}
	if len(items) > 0 {
		view.WarningMessage = "删除该证书资产会影响其他证书资产"
	}
	return view
}

func mapCertificateAssetDownloadOptionsView(options certassets.DownloadOptions) certificateAssetDownloadOptionsView {
	modes := make([]certificateAssetDownloadModeView, 0, len(options.Modes))
	for _, item := range options.Modes {
		modes = append(modes, certificateAssetDownloadModeView{
			Mode:    string(item.Mode),
			Default: item.Default,
		})
	}

	chainItems := make([]certificateAssetDownloadChainView, 0, len(options.ChainItems))
	for _, item := range options.ChainItems {
		chainItems = append(chainItems, certificateAssetDownloadChainView{
			Item:  mapCertificateAssetView(item.Item),
			Depth: item.Depth,
		})
	}

	treeItems := make([]certificateAssetDownloadTreeView, 0, len(options.TreeItems))
	for _, item := range options.TreeItems {
		treeItems = append(treeItems, certificateAssetDownloadTreeView{
			Item:          mapCertificateAssetView(item.Item),
			ParentAssetID: item.ParentAssetID,
			Depth:         item.Depth,
		})
	}

	return certificateAssetDownloadOptionsView{
		Target:     mapCertificateAssetView(options.Target),
		Modes:      modes,
		ChainItems: chainItems,
		TreeItems:  treeItems,
	}
}

func readUploadedFormFile(request *http.Request, field string) string {
	file, _, err := request.FormFile(field)
	if err != nil {
		return ""
	}
	defer file.Close()

	raw, err := io.ReadAll(file)
	if err != nil {
		return ""
	}
	return string(raw)
}

func parseBoolQueryValue(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

func parseCertificateAssetDownloadRequest(request *http.Request) (certassets.DownloadRequest, error) {
	query := request.URL.Query()
	item := certassets.DownloadRequest{
		Mode: certassets.DownloadMode(strings.ToLower(strings.TrimSpace(query.Get("mode")))),
	}

	if raw := strings.TrimSpace(query.Get("ancestor_id")); raw != "" {
		id, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || id <= 0 {
			return certassets.DownloadRequest{}, &apiError{Status: http.StatusBadRequest, Message: "ancestor_id must be a positive integer"}
		}
		item.AncestorAssetID = &id
	}

	assetIDs, err := parseCertificateAssetDownloadAssetIDs(query["asset_ids"])
	if err != nil {
		return certassets.DownloadRequest{}, err
	}
	item.AssetIDs = assetIDs

	return item, nil
}

func parseCertificateAssetDownloadAssetIDs(values []string) ([]int64, error) {
	if len(values) == 0 {
		return nil, nil
	}

	seen := make(map[int64]struct{})
	items := make([]int64, 0)
	for _, raw := range values {
		for _, part := range strings.Split(raw, ",") {
			part = strings.TrimSpace(part)
			if part == "" {
				continue
			}

			id, err := strconv.ParseInt(part, 10, 64)
			if err != nil || id <= 0 {
				return nil, &apiError{Status: http.StatusBadRequest, Message: "asset_ids must contain positive integers"}
			}
			if _, ok := seen[id]; ok {
				continue
			}
			seen[id] = struct{}{}
			items = append(items, id)
		}
	}
	if len(items) == 0 {
		return nil, nil
	}
	return items, nil
}

func writeCertificateAssetDownload(writer http.ResponseWriter, artifact certassets.DownloadArtifact) {
	writer.Header().Set("Content-Type", artifact.ContentType)
	if contentDisposition := mime.FormatMediaType("attachment", map[string]string{"filename": artifact.FileName}); contentDisposition != "" {
		writer.Header().Set("Content-Disposition", contentDisposition)
	} else {
		writer.Header().Set("Content-Disposition", `attachment; filename="download.zip"`)
	}
	writer.WriteHeader(http.StatusOK)
	_, _ = writer.Write(artifact.Body)
}
