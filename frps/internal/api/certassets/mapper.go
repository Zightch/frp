package certassets

import (
	"database/sql"
	"errors"
	"net/http"
	"strings"

	"github.com/zightch/frp/frps/internal/api/httpx"
	domaincertassets "github.com/zightch/frp/frps/internal/certassets"
	"github.com/zightch/frp/frps/internal/settings/entrycerts"
)

const schemaTimestampLayout = "2006-01-02 15:04:05.000000"

func mapAssetError(err error) error {
	if err == nil {
		return nil
	}

	var validationErr *domaincertassets.ValidationError
	if errors.As(err, &validationErr) {
		return &httpx.Error{
			Status:  http.StatusUnprocessableEntity,
			Message: validationErr.Error(),
			Code:    "certificate_asset_validation_failed",
			Details: map[string]any{
				"issues": validationErr.Issues,
			},
		}
	}

	var nameConflict *domaincertassets.NameConflictError
	if errors.As(err, &nameConflict) {
		return &httpx.Error{
			Status:  http.StatusConflict,
			Message: nameConflict.Error(),
			Code:    "certificate_asset_name_conflict",
		}
	}

	var duplicateErr *domaincertassets.DuplicateContentError
	if errors.As(err, &duplicateErr) {
		return &httpx.Error{
			Status:  http.StatusConflict,
			Message: duplicateErr.Error(),
			Code:    "certificate_asset_duplicate_content",
			Details: map[string]any{
				"duplicates": duplicateErr.Duplicates,
			},
		}
	}

	var deleteConfirmErr *domaincertassets.DeleteRequiresConfirmationError
	if errors.As(err, &deleteConfirmErr) {
		return &httpx.Error{
			Status:  http.StatusConflict,
			Message: "删除该证书资产会影响其他证书资产",
			Code:    "certificate_asset_delete_requires_confirmation",
			Details: map[string]any{
				"impact": mapDeleteImpactView(deleteConfirmErr.Impact),
			},
		}
	}

	if errors.Is(err, sql.ErrNoRows) {
		return &httpx.Error{
			Status:  http.StatusNotFound,
			Message: "certificate asset not found",
			Code:    "certificate_asset_not_found",
		}
	}

	if httpx.IsUniqueConstraintError(err) {
		return &httpx.Error{
			Status:  http.StatusConflict,
			Message: "certificate asset name already exists",
			Code:    "certificate_asset_name_conflict",
		}
	}

	return err
}

func mapAssetViews(items []domaincertassets.DescribedAsset) []AssetView {
	if len(items) == 0 {
		return []AssetView{}
	}

	result := make([]AssetView, 0, len(items))
	for _, item := range items {
		result = append(result, MapAssetView(item))
	}
	return result
}

func MapAssetView(item domaincertassets.DescribedAsset) AssetView {
	return AssetView{
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
		Status:        item.Status,
		StatusReason:  item.StatusReason,
		CreatedAt:     item.CreatedAt.UTC().Format(schemaTimestampLayout),
		UpdatedAt:     item.UpdatedAt.UTC().Format(schemaTimestampLayout),
	}
}

func mapDeleteImpactView(impact domaincertassets.DeleteImpact) deleteImpactView {
	items := make([]deleteImpactItemView, 0, len(impact.Affected))
	for _, item := range impact.Affected {
		items = append(items, deleteImpactItemView{
			Item:  MapAssetView(item.Item),
			Depth: item.Depth,
		})
	}

	view := deleteImpactView{
		Target:               MapAssetView(impact.Target),
		AffectedItems:        items,
		RequiresConfirmation: len(items) > 0,
	}
	if len(items) > 0 {
		view.WarningMessage = "删除该证书资产会影响其他证书资产"
	}
	return view
}

func mapDownloadOptionsView(options domaincertassets.DownloadOptions) downloadOptionsView {
	modes := make([]downloadModeView, 0, len(options.Modes))
	for _, item := range options.Modes {
		modes = append(modes, downloadModeView{
			Mode:    string(item.Mode),
			Default: item.Default,
		})
	}

	chainItems := make([]downloadChainView, 0, len(options.ChainItems))
	for _, item := range options.ChainItems {
		chainItems = append(chainItems, downloadChainView{
			Item:  MapAssetView(item.Item),
			Depth: item.Depth,
		})
	}

	treeItems := make([]downloadTreeView, 0, len(options.TreeItems))
	for _, item := range options.TreeItems {
		treeItems = append(treeItems, downloadTreeView{
			Item:          MapAssetView(item.Item),
			ParentAssetID: item.ParentAssetID,
			Depth:         item.Depth,
		})
	}

	return downloadOptionsView{
		Target:     MapAssetView(options.Target),
		Modes:      modes,
		ChainItems: chainItems,
		TreeItems:  treeItems,
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
		assetView := MapAssetView(*item.Asset)
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
