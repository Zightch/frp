package certassets

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
)

type DownloadMode string

const (
	DownloadModeOriginal DownloadMode = "original"
	DownloadModeSingle   DownloadMode = "single"
	DownloadModeChain    DownloadMode = "chain"
	DownloadModeTree     DownloadMode = "tree"
)

type DownloadModeOption struct {
	Mode    DownloadMode `json:"mode"`
	Default bool         `json:"default"`
}

type DownloadChainItem struct {
	Item  DescribedAsset `json:"item"`
	Depth int            `json:"depth"`
}

type DownloadTreeItem struct {
	Item          DescribedAsset `json:"item"`
	ParentAssetID *int64         `json:"parent_asset_id,omitempty"`
	Depth         int            `json:"depth"`
}

type DownloadOptions struct {
	Target     DescribedAsset       `json:"target"`
	Modes      []DownloadModeOption `json:"modes"`
	ChainItems []DownloadChainItem  `json:"chain_items,omitempty"`
	TreeItems  []DownloadTreeItem   `json:"tree_items,omitempty"`
}

func (s *Service) DownloadOptions(ctx context.Context, id int64) (DownloadOptions, error) {
	if s == nil || s.store == nil {
		return DownloadOptions{}, fmt.Errorf("certificate asset service store is nil")
	}

	assets, err := ListAssetsWithConn(ctx, s.store)
	if err != nil {
		return DownloadOptions{}, err
	}
	prepared, _, err := PrepareAssetsWithOptions(assets, s.prepareOptionsAt(s.now()))
	if err != nil {
		return DownloadOptions{}, err
	}
	return buildDownloadOptions(prepared, id)
}

func buildDownloadOptions(prepared []PreparedAsset, id int64) (DownloadOptions, error) {
	target, ok := FindPreparedAssetByID(prepared, id)
	if !ok {
		return DownloadOptions{}, sql.ErrNoRows
	}

	described := DescribePreparedAssets(prepared)
	describedByID := make(map[int64]DescribedAsset, len(described))
	preparedByID := make(map[int64]PreparedAsset, len(prepared))
	childrenByParentID := make(map[int64][]PreparedAsset)
	for _, item := range described {
		describedByID[item.ID] = item
	}
	for _, item := range prepared {
		preparedByID[item.Asset.ID] = item
		if item.Asset.HasIssuer() {
			parentID := *item.Asset.IssuerAssetID
			childrenByParentID[parentID] = append(childrenByParentID[parentID], item)
		}
	}
	for parentID := range childrenByParentID {
		sort.Slice(childrenByParentID[parentID], func(left, right int) bool {
			return childrenByParentID[parentID][left].Asset.ID < childrenByParentID[parentID][right].Asset.ID
		})
	}

	options := DownloadOptions{
		Target: describedByID[id],
	}

	if target.Asset.Source == SourceUpload {
		options.Modes = []DownloadModeOption{
			{Mode: DownloadModeOriginal, Default: true},
		}
		return options, nil
	}

	options.Modes = append(options.Modes,
		DownloadModeOption{Mode: DownloadModeSingle, Default: true},
		DownloadModeOption{Mode: DownloadModeChain, Default: false},
	)

	current := target
	depth := 0
	for {
		options.ChainItems = append(options.ChainItems, DownloadChainItem{
			Item:  describedByID[current.Asset.ID],
			Depth: depth,
		})
		if !current.Asset.HasIssuer() {
			break
		}
		next, exists := preparedByID[*current.Asset.IssuerAssetID]
		if !exists {
			break
		}
		current = next
		depth++
	}

	if target.Asset.AssetType != AssetTypeCA {
		return options, nil
	}

	options.Modes = append(options.Modes, DownloadModeOption{Mode: DownloadModeTree, Default: false})

	var walk func(parentID int64, depth int)
	walk = func(parentID int64, depth int) {
		item := describedByID[parentID]
		var parentAssetID *int64
		if preparedItem, exists := preparedByID[parentID]; exists && preparedItem.Asset.HasIssuer() {
			parentAssetID = preparedItem.Asset.IssuerAssetID
		}
		options.TreeItems = append(options.TreeItems, DownloadTreeItem{
			Item:          item,
			ParentAssetID: parentAssetID,
			Depth:         depth,
		})
		for _, child := range childrenByParentID[parentID] {
			walk(child.Asset.ID, depth+1)
		}
	}
	walk(id, 0)

	return options, nil
}
