package entrycerts

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/zightch/frp/frps/internal/certassets"
	"github.com/zightch/frp/frps/internal/storage"
)

type ServiceOptions struct {
	Prepare certassets.PrepareOptions
	Now     func() time.Time
}

type Service struct {
	store   *storage.SQL
	prepare certassets.PrepareOptions
	now     func() time.Time
}

func NewService(store *storage.SQL, options ServiceOptions) *Service {
	nowFn := options.Now
	if nowFn == nil {
		nowFn = func() time.Time { return time.Now().UTC() }
	}
	return &Service{
		store:   store,
		prepare: options.Prepare,
		now:     nowFn,
	}
}

func (s *Service) List(ctx context.Context) ([]DescribedUsage, error) {
	if s == nil || s.store == nil {
		return nil, fmt.Errorf("entry certificate service store is nil")
	}

	usageRows, err := ListUsagesWithConn(ctx, s.store)
	if err != nil {
		return nil, err
	}
	usageByType := make(map[UsageType]Usage, len(usageRows))
	for _, item := range usageRows {
		if item.TargetType != TargetTypeGlobal {
			continue
		}
		usageByType[item.UsageType] = item
	}

	_, describedByID, err := s.loadDescribedState(ctx)
	if err != nil {
		return nil, err
	}

	items := make([]DescribedUsage, 0, len(GlobalUsageTypes()))
	for _, usageType := range GlobalUsageTypes() {
		usage, ok := usageByType[usageType]
		if !ok {
			items = append(items, DescribedUsage{
				UsageType: usageType,
				Status:    "unbound",
			})
			continue
		}
		item := describeUsage(usage, describedByID)
		switch {
		case !usage.Enabled:
			item.Status = "disabled"
		case item.Asset == nil:
			item.Status = "broken"
			item.StatusReason = "bound asset not found"
		case item.Asset.Status == certassets.AssetStatusBroken:
			item.Status = "broken"
			item.StatusReason = item.Asset.StatusReason
		case item.Asset.Status == certassets.AssetStatusWarning:
			item.Status = "warning"
			item.StatusReason = item.Asset.StatusReason
			item.ResolvedChainLength = item.Asset.ChainLength
		default:
			item.Status = "enabled"
			item.ResolvedChainLength = item.Asset.ChainLength
		}
		items = append(items, item)
	}
	return items, nil
}

func (s *Service) Describe(ctx context.Context, usageType UsageType) (DescribedUsage, error) {
	if s == nil || s.store == nil {
		return DescribedUsage{}, fmt.Errorf("entry certificate service store is nil")
	}

	usage, err := LoadUsageByType(ctx, s.store, usageType)
	if err != nil {
		if err == sql.ErrNoRows {
			return DescribedUsage{
				UsageType: usageType,
				Status:    "unbound",
			}, nil
		}
		return DescribedUsage{}, err
	}

	_, describedByID, err := s.loadDescribedState(ctx)
	if err != nil {
		return DescribedUsage{}, err
	}
	item := describeUsage(usage, describedByID)
	if !usage.Enabled {
		item.Status = "disabled"
		return item, nil
	}
	if item.Asset == nil {
		item.Status = "broken"
		item.StatusReason = "bound asset not found"
		return item, nil
	}
	switch item.Asset.Status {
	case certassets.AssetStatusBroken:
		item.Status = "broken"
		item.StatusReason = item.Asset.StatusReason
	case certassets.AssetStatusWarning:
		item.Status = "warning"
		item.StatusReason = item.Asset.StatusReason
		item.ResolvedChainLength = item.Asset.ChainLength
	default:
		item.Status = "enabled"
		item.ResolvedChainLength = item.Asset.ChainLength
	}
	return item, nil
}

func (s *Service) Resolve(ctx context.Context, usageType UsageType, assetID int64) (ResolvedBinding, error) {
	if s == nil || s.store == nil {
		return ResolvedBinding{}, fmt.Errorf("entry certificate service store is nil")
	}
	prepared, describedByID, err := s.loadPreparedStateForAssetIDs(ctx, []int64{assetID})
	if err != nil {
		return ResolvedBinding{}, err
	}
	binding, err := s.resolveBindingFromState(usageType, assetID, prepared, describedByID)
	if err != nil {
		return ResolvedBinding{}, err
	}
	binding.UsageType = usageType
	return binding, nil
}

func (s *Service) ResolveCertificateAsset(ctx context.Context, assetID int64) (ResolvedBinding, error) {
	if s == nil || s.store == nil {
		return ResolvedBinding{}, fmt.Errorf("entry certificate service store is nil")
	}
	prepared, describedByID, err := s.loadPreparedStateForAssetIDs(ctx, []int64{assetID})
	if err != nil {
		return ResolvedBinding{}, err
	}
	return s.resolveCertificateAssetFromState(assetID, prepared, describedByID)
}

func (s *Service) ResolveCAPoolAssets(ctx context.Context, assetIDs []int64) (ResolvedCAPool, error) {
	if s == nil || s.store == nil {
		return ResolvedCAPool{}, fmt.Errorf("entry certificate service store is nil")
	}
	prepared, describedByID, err := s.loadPreparedStateForAssetIDs(ctx, assetIDs)
	if err != nil {
		return ResolvedCAPool{}, err
	}
	return s.resolveCAPoolAssetsFromState(assetIDs, prepared, describedByID)
}

func (s *Service) LoadUsage(ctx context.Context, usageType UsageType) (Usage, bool, error) {
	if s == nil || s.store == nil {
		return Usage{}, false, fmt.Errorf("entry certificate service store is nil")
	}
	item, err := LoadUsageByType(ctx, s.store, usageType)
	if err != nil {
		if err == sql.ErrNoRows {
			return Usage{}, false, nil
		}
		return Usage{}, false, err
	}
	return item, true, nil
}

func (s *Service) SaveUsage(ctx context.Context, usageType UsageType, assetID int64, enabled bool) (Usage, error) {
	if s == nil || s.store == nil {
		return Usage{}, fmt.Errorf("entry certificate service store is nil")
	}
	return UpsertUsage(ctx, s.store, usageType, assetID, enabled, s.now())
}

func (s *Service) RestoreUsage(ctx context.Context, usage Usage) error {
	if s == nil || s.store == nil {
		return fmt.Errorf("entry certificate service store is nil")
	}
	if usage.ID == 0 {
		return DeleteUsageByType(ctx, s.store, usage.UsageType)
	}
	_, err := UpsertUsage(ctx, s.store, usage.UsageType, usage.AssetID, usage.Enabled, usage.UpdatedAt)
	return err
}

func (s *Service) DeleteUsage(ctx context.Context, usageType UsageType) error {
	if s == nil || s.store == nil {
		return fmt.Errorf("entry certificate service store is nil")
	}
	return DeleteUsageByType(ctx, s.store, usageType)
}

func (s *Service) ListTargetUsages(ctx context.Context, targetType TargetType, targetID int64) ([]Usage, error) {
	if s == nil || s.store == nil {
		return nil, fmt.Errorf("entry certificate service store is nil")
	}
	return ListUsagesByTarget(ctx, s.store, targetType, targetID)
}

func (s *Service) ReplaceTargetUsages(ctx context.Context, targetType TargetType, targetID int64, usageType UsageType, assetIDs []int64) error {
	if s == nil || s.store == nil {
		return fmt.Errorf("entry certificate service store is nil")
	}
	return ReplaceTargetUsages(ctx, s.store, targetType, targetID, usageType, assetIDs, true, s.now())
}

func (s *Service) ResolveTargetCertificate(ctx context.Context, targetType TargetType, targetID int64, usageType UsageType) (ResolvedBinding, bool, error) {
	if s == nil || s.store == nil {
		return ResolvedBinding{}, false, fmt.Errorf("entry certificate service store is nil")
	}
	usages, err := ListUsagesByTargetAndType(ctx, s.store, targetType, targetID, usageType)
	if err != nil {
		return ResolvedBinding{}, false, err
	}
	enabled := enabledUsages(usages)
	switch len(enabled) {
	case 0:
		return ResolvedBinding{}, false, nil
	case 1:
	default:
		return ResolvedBinding{}, false, fmt.Errorf("usage %q allows only one bound asset", usageType)
	}

	binding, err := s.Resolve(ctx, usageType, enabled[0].AssetID)
	if err != nil {
		return ResolvedBinding{}, false, err
	}
	return binding, true, nil
}

func (s *Service) ResolveTargetCAPool(ctx context.Context, targetType TargetType, targetID int64, usageType UsageType) (ResolvedCAPool, error) {
	if s == nil || s.store == nil {
		return ResolvedCAPool{}, fmt.Errorf("entry certificate service store is nil")
	}
	usages, err := ListUsagesByTargetAndType(ctx, s.store, targetType, targetID, usageType)
	if err != nil {
		return ResolvedCAPool{}, err
	}
	enabled := enabledUsages(usages)
	assetIDs := make([]int64, 0, len(enabled))
	for _, usage := range enabled {
		assetIDs = append(assetIDs, usage.AssetID)
	}
	pool, err := s.ResolveCAPoolAssets(ctx, assetIDs)
	if err != nil {
		return ResolvedCAPool{}, err
	}
	pool.UsageType = usageType
	return pool, nil
}

func (s *Service) IsEnabled(ctx context.Context, usageType UsageType) (bool, error) {
	item, ok, err := s.LoadUsage(ctx, usageType)
	if err != nil || !ok {
		return false, err
	}
	return item.Enabled, nil
}

func describeUsage(usage Usage, describedByID map[int64]certassets.DescribedAsset) DescribedUsage {
	item := DescribedUsage{
		UsageType: usage.UsageType,
		Enabled:   usage.Enabled,
		UpdatedAt: usage.UpdatedAt,
	}
	assetID := usage.AssetID
	item.AssetID = &assetID
	if asset, ok := describedByID[usage.AssetID]; ok {
		assetCopy := asset
		item.AssetName = asset.Name
		item.Asset = &assetCopy
		item.ResolvedChainLength = asset.ChainLength
	}
	return item
}

func (s *Service) loadDescribedState(ctx context.Context) ([]certassets.Asset, map[int64]certassets.DescribedAsset, error) {
	assets, err := certassets.ListAssetsWithConn(ctx, s.store)
	if err != nil {
		return nil, nil, err
	}

	described := certassets.DescribeAssetsBestEffort(assets)
	describedByID := make(map[int64]certassets.DescribedAsset, len(described))
	for _, item := range described {
		describedByID[item.ID] = item
	}
	return assets, describedByID, nil
}

func (s *Service) loadPreparedStateForAssetIDs(ctx context.Context, assetIDs []int64) ([]certassets.PreparedAsset, map[int64]certassets.DescribedAsset, error) {
	assets, err := certassets.ListAssetsWithConn(ctx, s.store)
	if err != nil {
		return nil, nil, err
	}

	selected, err := selectAssetsForPreparation(assets, assetIDs)
	if err != nil {
		return nil, nil, err
	}

	options := s.prepare
	options.Now = s.now().UTC()
	prepared, _ := certassets.PrepareAssetsBestEffort(selected, options)

	described := certassets.DescribePreparedAssets(prepared)
	describedByID := make(map[int64]certassets.DescribedAsset, len(described))
	for _, item := range described {
		describedByID[item.ID] = item
	}
	return prepared, describedByID, nil
}

func (s *Service) resolveBindingFromState(usageType UsageType, assetID int64, prepared []certassets.PreparedAsset, describedByID map[int64]certassets.DescribedAsset) (ResolvedBinding, error) {
	if !isKnownUsageType(usageType) {
		return ResolvedBinding{}, fmt.Errorf("unsupported entry certificate type %q", usageType)
	}
	binding, err := s.resolveCertificateAssetFromState(assetID, prepared, describedByID)
	if err != nil {
		return ResolvedBinding{}, err
	}
	binding.UsageType = usageType
	return binding, nil
}

func (s *Service) resolveCertificateAssetFromState(assetID int64, prepared []certassets.PreparedAsset, describedByID map[int64]certassets.DescribedAsset) (ResolvedBinding, error) {
	if assetID <= 0 {
		return ResolvedBinding{}, fmt.Errorf("asset_id must be greater than zero")
	}

	target, ok := certassets.FindPreparedAssetByID(prepared, assetID)
	if !ok {
		return ResolvedBinding{}, sql.ErrNoRows
	}
	if target.Asset.AssetType != certassets.AssetTypeCertificate {
		return ResolvedBinding{}, fmt.Errorf("bound asset must be a certificate")
	}
	if !target.Asset.HasKey() {
		return ResolvedBinding{}, fmt.Errorf("bound asset must include a private key")
	}

	certificatePEM, chainLength, err := buildCertificatePEM(target, prepared)
	if err != nil {
		return ResolvedBinding{}, err
	}
	pair, err := tls.X509KeyPair([]byte(certificatePEM), []byte(target.Asset.Key))
	if err != nil {
		return ResolvedBinding{}, fmt.Errorf("build tls certificate: %w", err)
	}

	asset, ok := describedByID[assetID]
	if !ok {
		return ResolvedBinding{}, fmt.Errorf("described asset %d not found", assetID)
	}
	return ResolvedBinding{
		Asset:               asset,
		CertificatePEM:      certificatePEM,
		KeyPEM:              target.Asset.Key,
		TLSCertificate:      pair,
		ResolvedChainLength: chainLength,
	}, nil
}

func (s *Service) resolveCAPoolAssetsFromState(assetIDs []int64, prepared []certassets.PreparedAsset, describedByID map[int64]certassets.DescribedAsset) (ResolvedCAPool, error) {
	pool := x509.NewCertPool()
	assets := make([]certassets.DescribedAsset, 0, len(assetIDs))
	seen := make(map[int64]struct{}, len(assetIDs))

	preparedByID := make(map[int64]certassets.PreparedAsset, len(prepared))
	for _, item := range prepared {
		preparedByID[item.Asset.ID] = item
	}

	var builder strings.Builder
	for _, assetID := range assetIDs {
		if assetID <= 0 {
			return ResolvedCAPool{}, fmt.Errorf("asset_id must be greater than zero")
		}
		if _, ok := seen[assetID]; ok {
			continue
		}
		seen[assetID] = struct{}{}

		target, ok := preparedByID[assetID]
		if !ok {
			continue
		}
		if target.Asset.AssetType != certassets.AssetTypeCA {
			return ResolvedCAPool{}, fmt.Errorf("bound asset must be a ca")
		}
		described, ok := describedByID[assetID]
		if !ok {
			return ResolvedCAPool{}, fmt.Errorf("described asset %d not found", assetID)
		}
		assets = append(assets, described)

		pemText := normalizePEMText(target.Asset.CRT)
		if pemText == "" {
			continue
		}
		builder.WriteString(pemText)
		if !pool.AppendCertsFromPEM([]byte(pemText)) {
			return ResolvedCAPool{}, fmt.Errorf("append ca certificates from asset %d", assetID)
		}
	}

	return ResolvedCAPool{
		Assets: assets,
		PEM:    builder.String(),
		Pool:   pool,
	}, nil
}

func selectAssetsForPreparation(assets []certassets.Asset, rootAssetIDs []int64) ([]certassets.Asset, error) {
	if len(rootAssetIDs) == 0 {
		return nil, nil
	}

	assetsByID := make(map[int64]certassets.Asset, len(assets))
	for _, asset := range assets {
		assetsByID[asset.ID] = asset
	}

	selected := make([]certassets.Asset, 0, len(rootAssetIDs))
	selectedSet := make(map[int64]struct{}, len(rootAssetIDs))

	for _, rootID := range rootAssetIDs {
		if rootID <= 0 {
			return nil, fmt.Errorf("asset_id must be greater than zero")
		}

		currentID := rootID
		chainSeen := make(map[int64]struct{})
		for currentID > 0 {
			if _, ok := chainSeen[currentID]; ok {
				break
			}
			chainSeen[currentID] = struct{}{}

			asset, ok := assetsByID[currentID]
			if !ok {
				if currentID == rootID {
					return nil, sql.ErrNoRows
				}
				break
			}
			if _, ok := selectedSet[currentID]; !ok {
				selected = append(selected, asset)
				selectedSet[currentID] = struct{}{}
			}
			if !asset.HasIssuer() {
				break
			}
			currentID = *asset.IssuerAssetID
		}
	}

	return selected, nil
}

func buildCertificatePEM(target certassets.PreparedAsset, prepared []certassets.PreparedAsset) (string, int, error) {
	if target.Asset.Source == certassets.SourceUpload {
		return normalizePEMText(target.Asset.CRT), len(target.Certificates), nil
	}

	preparedByID := make(map[int64]certassets.PreparedAsset, len(prepared))
	for _, item := range prepared {
		preparedByID[item.Asset.ID] = item
	}

	var builder strings.Builder
	builder.WriteString(target.Asset.CRT)
	chainLength := len(target.Certificates)
	visited := map[int64]struct{}{
		target.Asset.ID: {},
	}
	current := target
	for current.Asset.HasIssuer() {
		issuerID := *current.Asset.IssuerAssetID
		if _, ok := visited[issuerID]; ok {
			break
		}
		visited[issuerID] = struct{}{}

		issuer, ok := preparedByID[issuerID]
		if !ok {
			break
		}
		if isSelfSigned(issuer.Leaf) {
			break
		}

		builder.WriteString(issuer.Asset.CRT)
		chainLength += len(issuer.Certificates)
		current = issuer
	}

	return normalizePEMText(builder.String()), chainLength, nil
}

func normalizePEMText(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	return value + "\n"
}

func isSelfSigned(cert *x509.Certificate) bool {
	if cert == nil {
		return false
	}
	if !bytes.Equal(cert.RawIssuer, cert.RawSubject) {
		return false
	}
	return cert.CheckSignatureFrom(cert) == nil
}

func isKnownUsageType(value UsageType) bool {
	switch value {
	case UsageTypeWebUIHTTPS,
		UsageTypeFrpcTLS,
		UsageTypeTunnelListenServerCert,
		UsageTypeTunnelListenClientCA,
		UsageTypeTunnelBackendClientCert,
		UsageTypeTunnelBackendCA:
		return true
	default:
		return false
	}
}

func enabledUsages(usages []Usage) []Usage {
	items := make([]Usage, 0, len(usages))
	for _, usage := range usages {
		if !usage.Enabled {
			continue
		}
		items = append(items, usage)
	}
	return items
}
