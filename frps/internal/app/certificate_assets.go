package app

import (
	"context"
	"fmt"

	"github.com/zightch/frp/frps/internal/certassets"
	"github.com/zightch/frp/frps/internal/storage"
)

var prepareCertificateAssetRuntime = func(ctx context.Context, store *storage.SQL) (*certassets.Runtime, error) {
	return certassets.PrepareRuntime(ctx, certassets.NewRepository(store))
}

func (a *App) initCertificateAssets(ctx context.Context) error {
	runtime, err := prepareCertificateAssetRuntime(ctx, a.store)
	if err != nil {
		return fmt.Errorf("prepare certificate assets: %w", err)
	}

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
