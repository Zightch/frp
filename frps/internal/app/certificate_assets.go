package app

import (
	"context"
	"fmt"

	"github.com/zightch/frp/frps/internal/certassets"
	"github.com/zightch/frp/frps/internal/storage"
	"github.com/zightch/frp/frps/internal/testhooks"
)

var prepareCertificateAssetRuntime = func(ctx context.Context, store *storage.SQL) (*certassets.Runtime, error) {
	return certassets.PrepareRuntime(ctx, certassets.NewRepository(store))
}

func (a *App) initCertificateAssets(ctx context.Context) error {
	testhooks.Point("startup.certificate_assets.before_prepare")
	runtime, err := prepareCertificateAssetRuntime(ctx, a.store)
	if err != nil {
		return fmt.Errorf("prepare certificate assets: %w", err)
	}
	testhooks.Point(
		"startup.certificate_assets.after_prepare",
		testhooks.F("system_ca_count", runtime.SystemCACount()),
		testhooks.F("asset_count", runtime.AssetCount()),
	)

	a.certs = runtime
	if a.logger != nil {
		a.logger.Info(
			"certificate assets ready",
			"system_ca_count", runtime.SystemCACount(),
			"asset_count", runtime.AssetCount(),
		)
	}
	return nil
}

func (a *App) closeCertificateAssets() {
	a.certs = nil
}
