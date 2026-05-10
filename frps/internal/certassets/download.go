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
	return buildDownloadOptionsFromAssets(assets, id)
}

func buildDownloadOptions(prepared []PreparedAsset, id int64) (DownloadOptions, error) {
	assets := make([]Asset, 0, len(prepared))
	for _, item := range prepared {
		assets = append(assets, item.Asset)
	}
	return buildDownloadOptionsFromAssets(assets, id)
}

func buildDownloadOptionsFromAssets(assets []Asset, id int64) (DownloadOptions, error) {
	target, ok := findAssetByID(assets, id)
	if !ok {
		return DownloadOptions{}, sql.ErrNoRows
	}

	described := DescribeAssetsBestEffort(assets)
	describedByID := make(map[int64]DescribedAsset, len(described))
	assetsByID := make(map[int64]Asset, len(assets))
	childrenByParentID := make(map[int64][]Asset)
	for _, item := range described {
		describedByID[item.ID] = item
	}
	for _, item := range assets {
		assetsByID[item.ID] = item
		if item.HasIssuer() {
			parentID := *item.IssuerAssetID
			childrenByParentID[parentID] = append(childrenByParentID[parentID], item)
		}
	}
	for parentID := range childrenByParentID {
		sort.Slice(childrenByParentID[parentID], func(left, right int) bool {
			return childrenByParentID[parentID][left].ID < childrenByParentID[parentID][right].ID
		})
	}

	options := DownloadOptions{
		Target: describedByID[id],
	}

	if target.Source == SourceUpload {
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
		currentDescription, ok := describedByID[current.ID]
		if !ok {
			return DownloadOptions{}, fmt.Errorf("described asset %d not found", current.ID)
		}
		options.ChainItems = append(options.ChainItems, DownloadChainItem{
			Item:  currentDescription,
			Depth: depth,
		})
		if !current.HasIssuer() {
			break
		}
		next, exists := assetsByID[*current.IssuerAssetID]
		if !exists {
			break
		}
		current = next
		depth++
	}

	if target.AssetType != AssetTypeCA {
		return options, nil
	}

	options.Modes = append(options.Modes, DownloadModeOption{Mode: DownloadModeTree, Default: false})

	var walk func(parentID int64, depth int)
	walk = func(parentID int64, depth int) {
		item := describedByID[parentID]
		var parentAssetID *int64
		if assetItem, exists := assetsByID[parentID]; exists && assetItem.HasIssuer() {
			parentAssetID = assetItem.IssuerAssetID
		}
		options.TreeItems = append(options.TreeItems, DownloadTreeItem{
			Item:          item,
			ParentAssetID: parentAssetID,
			Depth:         depth,
		})
		for _, child := range childrenByParentID[parentID] {
			walk(child.ID, depth+1)
		}
	}
	walk(id, 0)

	return options, nil
}
