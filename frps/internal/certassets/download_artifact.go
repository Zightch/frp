package certassets

import (
	"archive/zip"
	"bytes"
	"context"
	"database/sql"
	"fmt"
	"strings"
)

const downloadArtifactContentType = "application/zip"

type DownloadRequest struct {
	Mode            DownloadMode `json:"mode"`
	AncestorAssetID *int64       `json:"ancestor_asset_id,omitempty"`
	AssetIDs        []int64      `json:"asset_ids,omitempty"`
}

type DownloadArtifact struct {
	FileName    string
	ContentType string
	Body        []byte
}

type downloadFile struct {
	Name    string
	Content []byte
}

func (s *Service) Download(ctx context.Context, id int64, request DownloadRequest) (DownloadArtifact, error) {
	if s == nil || s.store == nil {
		return DownloadArtifact{}, fmt.Errorf("certificate asset service store is nil")
	}

	assets, err := ListAssetsWithConn(ctx, s.store)
	if err != nil {
		return DownloadArtifact{}, err
	}
	prepared, _, err := PrepareAssetsWithOptions(assets, s.prepareOptionsAt(s.now()))
	if err != nil {
		return DownloadArtifact{}, err
	}
	return buildDownloadArtifact(prepared, id, request)
}

func buildDownloadArtifact(prepared []PreparedAsset, id int64, request DownloadRequest) (DownloadArtifact, error) {
	target, ok := FindPreparedAssetByID(prepared, id)
	if !ok {
		return DownloadArtifact{}, sql.ErrNoRows
	}

	options, err := buildDownloadOptions(prepared, id)
	if err != nil {
		return DownloadArtifact{}, err
	}

	mode, err := resolveDownloadMode(options, request)
	if err != nil {
		return DownloadArtifact{}, err
	}
	if err := validateDownloadRequestShape(mode, request); err != nil {
		return DownloadArtifact{}, err
	}

	preparedByID := make(map[int64]PreparedAsset, len(prepared))
	for _, item := range prepared {
		preparedByID[item.Asset.ID] = item
	}

	var files []downloadFile
	switch mode {
	case DownloadModeOriginal:
		files = buildAssetMaterialFiles(target, "")
	case DownloadModeSingle:
		files = buildSingleDownloadFiles(target)
	case DownloadModeChain:
		files, err = buildChainDownloadFiles(target, options, preparedByID, request.AncestorAssetID)
		if err != nil {
			return DownloadArtifact{}, err
		}
	case DownloadModeTree:
		files, err = buildTreeDownloadFiles(options, preparedByID, request.AssetIDs)
		if err != nil {
			return DownloadArtifact{}, err
		}
	default:
		return DownloadArtifact{}, validationError("download mode is not available for this asset", ValidationIssue{
			Field:   "mode",
			Code:    "download_mode_not_available",
			Message: "download mode is not available for this asset",
		})
	}

	body, err := buildDownloadArchive(files)
	if err != nil {
		return DownloadArtifact{}, err
	}
	return DownloadArtifact{
		FileName:    downloadArchiveName(target.Asset, mode),
		ContentType: downloadArtifactContentType,
		Body:        body,
	}, nil
}

func resolveDownloadMode(options DownloadOptions, request DownloadRequest) (DownloadMode, error) {
	mode := normalizeDownloadMode(request.Mode)
	if mode == "" {
		switch {
		case len(request.AssetIDs) > 0:
			mode = DownloadModeTree
		case request.AncestorAssetID != nil:
			mode = DownloadModeChain
		default:
			mode = defaultDownloadMode(options.Modes)
		}
	}

	for _, option := range options.Modes {
		if option.Mode == mode {
			return mode, nil
		}
	}
	return "", validationError("download mode is not available for this asset", ValidationIssue{
		Field:   "mode",
		Code:    "download_mode_not_available",
		Message: "download mode is not available for this asset",
	})
}

func normalizeDownloadMode(mode DownloadMode) DownloadMode {
	return DownloadMode(strings.ToLower(strings.TrimSpace(string(mode))))
}

func defaultDownloadMode(modes []DownloadModeOption) DownloadMode {
	for _, mode := range modes {
		if mode.Default {
			return mode.Mode
		}
	}
	if len(modes) == 0 {
		return ""
	}
	return modes[0].Mode
}

func validateDownloadRequestShape(mode DownloadMode, request DownloadRequest) error {
	if request.AncestorAssetID != nil && mode != DownloadModeChain {
		return validationError("ancestor_asset_id is only supported for chain mode", ValidationIssue{
			Field:   "ancestor_asset_id",
			Code:    "download_ancestor_not_supported",
			Message: "ancestor_asset_id is only supported for chain mode",
		})
	}
	if len(request.AssetIDs) > 0 && mode != DownloadModeTree {
		return validationError("asset_ids is only supported for tree mode", ValidationIssue{
			Field:   "asset_ids",
			Code:    "download_asset_ids_not_supported",
			Message: "asset_ids is only supported for tree mode",
		})
	}
	return nil
}

func buildSingleDownloadFiles(target PreparedAsset) []downloadFile {
	files := make([]downloadFile, 0, 2)
	files = append(files, downloadFile{
		Name:    downloadCRTFileName(target.Asset, ""),
		Content: []byte(target.Asset.CRT),
	})
	if target.Asset.HasKey() {
		files = append(files, downloadFile{
			Name:    downloadKeyFileName(target.Asset),
			Content: []byte(target.Asset.Key),
		})
	}
	return files
}

func buildAssetMaterialFiles(target PreparedAsset, crtSuffix string) []downloadFile {
	files := make([]downloadFile, 0, 2)
	files = append(files, downloadFile{
		Name:    downloadCRTFileName(target.Asset, crtSuffix),
		Content: []byte(target.Asset.CRT),
	})
	if target.Asset.HasKey() {
		files = append(files, downloadFile{
			Name:    downloadKeyFileName(target.Asset),
			Content: []byte(target.Asset.Key),
		})
	}
	return files
}

func buildChainDownloadFiles(target PreparedAsset, options DownloadOptions, preparedByID map[int64]PreparedAsset, ancestorAssetID *int64) ([]downloadFile, error) {
	var chain bytes.Buffer
	foundAncestor := ancestorAssetID == nil

	for _, item := range options.ChainItems {
		prepared, exists := preparedByID[item.Item.ID]
		if !exists {
			return nil, fmt.Errorf("prepared asset %d not found for download", item.Item.ID)
		}
		chain.WriteString(prepared.Asset.CRT)
		if ancestorAssetID != nil && item.Item.ID == *ancestorAssetID {
			foundAncestor = true
			break
		}
	}

	if !foundAncestor {
		return nil, validationError("ancestor_asset_id must reference the current asset or one of its ancestors", ValidationIssue{
			Field:   "ancestor_asset_id",
			Code:    "download_ancestor_invalid",
			Message: "ancestor_asset_id must reference the current asset or one of its ancestors",
		})
	}

	files := make([]downloadFile, 0, 2)
	files = append(files, downloadFile{
		Name:    downloadCRTFileName(target.Asset, "chain"),
		Content: []byte(normalizePEMText(chain.String())),
	})
	if target.Asset.HasKey() {
		files = append(files, downloadFile{
			Name:    downloadKeyFileName(target.Asset),
			Content: []byte(target.Asset.Key),
		})
	}
	return files, nil
}

func buildTreeDownloadFiles(options DownloadOptions, preparedByID map[int64]PreparedAsset, requestedIDs []int64) ([]downloadFile, error) {
	available := make(map[int64]struct{}, len(options.TreeItems))
	for _, item := range options.TreeItems {
		available[item.Item.ID] = struct{}{}
	}

	requested := make(map[int64]struct{}, len(requestedIDs))
	for _, id := range requestedIDs {
		if _, ok := available[id]; !ok {
			return nil, validationError("asset_ids must reference items within the selected tree", ValidationIssue{
				Field:   "asset_ids",
				Code:    "download_tree_item_invalid",
				Message: "asset_ids must reference items within the selected tree",
			})
		}
		requested[id] = struct{}{}
	}

	files := make([]downloadFile, 0, len(options.TreeItems)*2)
	for _, item := range options.TreeItems {
		if len(requested) > 0 {
			if _, ok := requested[item.Item.ID]; !ok {
				continue
			}
		}

		prepared, exists := preparedByID[item.Item.ID]
		if !exists {
			return nil, fmt.Errorf("prepared asset %d not found for download", item.Item.ID)
		}
		files = append(files, buildAssetMaterialFiles(prepared, "")...)
	}

	if len(files) == 0 {
		return nil, validationError("asset_ids must contain at least one tree item", ValidationIssue{
			Field:   "asset_ids",
			Code:    "download_tree_items_empty",
			Message: "asset_ids must contain at least one tree item",
		})
	}
	return files, nil
}

func buildDownloadArchive(files []downloadFile) ([]byte, error) {
	var body bytes.Buffer
	archive := zip.NewWriter(&body)
	for _, file := range files {
		entry, err := archive.Create(file.Name)
		if err != nil {
			_ = archive.Close()
			return nil, fmt.Errorf("create download archive entry %q: %w", file.Name, err)
		}
		if _, err := entry.Write(file.Content); err != nil {
			_ = archive.Close()
			return nil, fmt.Errorf("write download archive entry %q: %w", file.Name, err)
		}
	}
	if err := archive.Close(); err != nil {
		return nil, fmt.Errorf("close download archive: %w", err)
	}
	return body.Bytes(), nil
}

func downloadArchiveName(asset Asset, mode DownloadMode) string {
	base := downloadFileBaseName(asset)
	switch mode {
	case DownloadModeSingle:
		return base + ".zip"
	default:
		return base + "-" + string(mode) + ".zip"
	}
}

func downloadCRTFileName(asset Asset, suffix string) string {
	base := downloadFileBaseName(asset)
	suffix = strings.TrimSpace(suffix)
	if suffix != "" {
		base += "-" + suffix
	}
	return base + ".crt"
}

func downloadKeyFileName(asset Asset) string {
	return downloadFileBaseName(asset) + ".key"
}

func downloadFileBaseName(asset Asset) string {
	name := strings.ToLower(strings.TrimSpace(asset.Name))
	if name == "" {
		name = "certificate"
	}

	var builder strings.Builder
	lastDash := false
	for _, char := range name {
		switch {
		case char >= 'a' && char <= 'z':
			builder.WriteRune(char)
			lastDash = false
		case char >= '0' && char <= '9':
			builder.WriteRune(char)
			lastDash = false
		case char == '-' || char == '_':
			builder.WriteRune(char)
			lastDash = false
		default:
			if !lastDash {
				builder.WriteByte('-')
				lastDash = true
			}
		}
	}

	sanitized := strings.Trim(builder.String(), "-_.")
	if sanitized == "" {
		sanitized = "certificate"
	}
	return fmt.Sprintf("%s-%d", sanitized, asset.ID)
}
