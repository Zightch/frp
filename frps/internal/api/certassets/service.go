package certassets

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/zightch/frp/frps/internal/api/httpx"
	domaincertassets "github.com/zightch/frp/frps/internal/certassets"
	"github.com/zightch/frp/frps/internal/settings/entrycerts"
	"github.com/zightch/frp/frps/internal/storage"
)

const certificateAssetUploadMaxMemory = 4 << 20

type Options struct {
	Store *storage.SQL
}

type Service struct {
	store      *storage.SQL
	assets     *domaincertassets.Service
	entryCerts *entrycerts.Service
}

func NewService(options Options) *Service {
	if options.Store == nil {
		return nil
	}

	return &Service{
		store:      options.Store,
		assets:     domaincertassets.NewService(options.Store, domaincertassets.ServiceOptions{}),
		entryCerts: entrycerts.NewService(options.Store, entrycerts.ServiceOptions{}),
	}
}

func (s *Service) ListAssets(ctx context.Context) ([]AssetView, error) {
	if s == nil || s.assets == nil {
		return nil, &httpx.Error{Status: http.StatusServiceUnavailable, Message: "certificate asset service is unavailable"}
	}

	items, err := s.assets.List(ctx)
	if err != nil {
		return nil, mapAssetError(err)
	}
	return mapAssetViews(items), nil
}

func (s *Service) UploadAsset(request *http.Request) (AssetView, error) {
	if s == nil || s.assets == nil {
		return AssetView{}, &httpx.Error{Status: http.StatusServiceUnavailable, Message: "certificate asset service is unavailable"}
	}

	if err := request.ParseMultipartForm(certificateAssetUploadMaxMemory); err != nil {
		return AssetView{}, &httpx.Error{
			Status:  http.StatusBadRequest,
			Message: fmt.Sprintf("invalid multipart form: %v", err),
			Code:    "certificate_asset_invalid_multipart",
		}
	}

	item, err := s.assets.Import(request.Context(), domaincertassets.CreateInput{
		Name:   strings.TrimSpace(request.FormValue("name")),
		Remark: strings.TrimSpace(request.FormValue("remark")),
		Source: domaincertassets.SourceUpload,
		CRT:    readUploadedFormFile(request, "crt"),
		Key:    readUploadedFormFile(request, "key"),
	})
	if err != nil {
		return AssetView{}, mapAssetError(err)
	}
	return MapAssetView(item), nil
}

func (s *Service) PasteAsset(ctx context.Context, payload pasteRequest) (AssetView, error) {
	if s == nil || s.assets == nil {
		return AssetView{}, &httpx.Error{Status: http.StatusServiceUnavailable, Message: "certificate asset service is unavailable"}
	}

	item, err := s.assets.Import(ctx, domaincertassets.CreateInput{
		Name:   payload.Name,
		Remark: payload.Remark,
		Source: domaincertassets.SourceUpload,
		CRT:    payload.CRT,
		Key:    payload.Key,
	})
	if err != nil {
		return AssetView{}, mapAssetError(err)
	}
	return MapAssetView(item), nil
}

func (s *Service) GenerateAsset(ctx context.Context, payload generateRequest) (AssetView, error) {
	if s == nil || s.assets == nil {
		return AssetView{}, &httpx.Error{Status: http.StatusServiceUnavailable, Message: "certificate asset service is unavailable"}
	}

	item, err := s.assets.Generate(ctx, domaincertassets.GenerateInput{
		Name:          payload.Name,
		Remark:        payload.Remark,
		AssetType:     domaincertassets.AssetType(strings.ToLower(strings.TrimSpace(payload.AssetType))),
		IssuerAssetID: payload.IssuerAssetID,
		CommonName:    payload.CommonName,
		ValidityDays:  payload.ValidityDays,
		DNSNames:      payload.DNSNames,
		IPAddresses:   payload.IPAddresses,
		KeyAlgorithm:  domaincertassets.GenerateKeyAlgorithm(payload.KeyAlgorithm),
		KeyBits:       payload.KeyBits,
	})
	if err != nil {
		return AssetView{}, mapAssetError(err)
	}
	return MapAssetView(item), nil
}

func (s *Service) UpdateAsset(ctx context.Context, id int64, payload patchRequest) (AssetView, error) {
	if s == nil || s.assets == nil {
		return AssetView{}, &httpx.Error{Status: http.StatusServiceUnavailable, Message: "certificate asset service is unavailable"}
	}

	item, err := s.assets.UpdateMetadata(ctx, id, domaincertassets.UpdateMetadataInput{
		Name:   payload.Name,
		Remark: payload.Remark,
	})
	if err != nil {
		return AssetView{}, mapAssetError(err)
	}
	return MapAssetView(item), nil
}

func (s *Service) DeleteImpact(ctx context.Context, id int64) (deleteImpactView, error) {
	if s == nil || s.assets == nil {
		return deleteImpactView{}, &httpx.Error{Status: http.StatusServiceUnavailable, Message: "certificate asset service is unavailable"}
	}

	impact, err := s.assets.DeleteImpact(ctx, id)
	if err != nil {
		return deleteImpactView{}, mapAssetError(err)
	}

	view := mapDeleteImpactView(impact)
	assetIDs := collectDeleteImpactAssetIDs(impact)
	usageItems, err := s.activeEntryCertificateConflicts(ctx, assetIDs)
	if err != nil {
		return deleteImpactView{}, err
	}
	if len(usageItems) > 0 {
		view.UsageItems = usageItems
		view.RequiresConfirmation = true
		if view.WarningMessage == "" {
			view.WarningMessage = "删除该证书资产前需要先到系统设置的入口证书中解绑"
		} else {
			view.WarningMessage += "；并且需要先到系统设置的入口证书中解绑"
		}
	}
	return view, nil
}

func (s *Service) DeleteAsset(ctx context.Context, id int64, cascade bool) (domaincertassets.DeleteResult, error) {
	if s == nil || s.assets == nil {
		return domaincertassets.DeleteResult{}, &httpx.Error{Status: http.StatusServiceUnavailable, Message: "certificate asset service is unavailable"}
	}

	impact, err := s.assets.DeleteImpact(ctx, id)
	if err != nil {
		return domaincertassets.DeleteResult{}, mapAssetError(err)
	}

	usageItems, err := s.activeEntryCertificateConflicts(ctx, collectDeleteImpactAssetIDs(impact))
	if err != nil {
		return domaincertassets.DeleteResult{}, err
	}
	if len(usageItems) > 0 {
		return domaincertassets.DeleteResult{}, &httpx.Error{
			Status:  http.StatusConflict,
			Message: "certificate asset is still bound to an active entry certificate",
			Code:    "certificate_asset_in_use",
			Details: map[string]any{
				"usage_items": usageItems,
			},
		}
	}

	result, err := s.assets.Delete(ctx, id, cascade)
	if err != nil {
		return domaincertassets.DeleteResult{}, mapAssetError(err)
	}
	return result, nil
}

func (s *Service) DownloadOptions(ctx context.Context, id int64) (downloadOptionsView, error) {
	if s == nil || s.assets == nil {
		return downloadOptionsView{}, &httpx.Error{Status: http.StatusServiceUnavailable, Message: "certificate asset service is unavailable"}
	}

	options, err := s.assets.DownloadOptions(ctx, id)
	if err != nil {
		return downloadOptionsView{}, mapAssetError(err)
	}
	return mapDownloadOptionsView(options), nil
}

func (s *Service) DownloadAsset(ctx context.Context, id int64, request domaincertassets.DownloadRequest) (domaincertassets.DownloadArtifact, error) {
	if s == nil || s.assets == nil {
		return domaincertassets.DownloadArtifact{}, &httpx.Error{Status: http.StatusServiceUnavailable, Message: "certificate asset service is unavailable"}
	}

	artifact, err := s.assets.Download(ctx, id, request)
	if err != nil {
		return domaincertassets.DownloadArtifact{}, mapAssetError(err)
	}
	return artifact, nil
}

func (s *Service) activeEntryCertificateConflicts(ctx context.Context, assetIDs []int64) ([]entryCertificateView, error) {
	if s == nil || s.entryCerts == nil || len(assetIDs) == 0 {
		return nil, nil
	}

	usages, err := entrycerts.ListUsagesWithConn(ctx, s.store)
	if err != nil {
		return nil, mapEntryCertificateError(err)
	}

	seen := make(map[int64]struct{}, len(assetIDs))
	for _, assetID := range assetIDs {
		seen[assetID] = struct{}{}
	}

	conflicts := make([]entryCertificateView, 0)
	for _, usage := range usages {
		if usage.TargetType != entrycerts.TargetTypeGlobal || usage.AssetID <= 0 || !usage.Enabled {
			continue
		}
		if _, ok := seen[usage.AssetID]; !ok {
			continue
		}
		assetID := usage.AssetID
		conflicts = append(conflicts, entryCertificateView{
			UsageType: string(usage.UsageType),
			AssetID:   &assetID,
			Enabled:   usage.Enabled,
			Status:    "enabled",
			UpdatedAt: usage.UpdatedAt.UTC().Format(schemaTimestampLayout),
		})
	}
	return conflicts, nil
}

func collectDeleteImpactAssetIDs(impact domaincertassets.DeleteImpact) []int64 {
	assetIDs := make([]int64, 0, 1+len(impact.Affected))
	assetIDs = append(assetIDs, impact.Target.ID)
	for _, item := range impact.Affected {
		assetIDs = append(assetIDs, item.Item.ID)
	}
	return assetIDs
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
