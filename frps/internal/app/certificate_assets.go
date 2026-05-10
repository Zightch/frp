package app

import (
	"context"
	"github.com/zightch/frp/frps/internal/testhooks"
)

func (a *App) initCertificateAssets(ctx context.Context) error {
	_ = ctx
	testhooks.Point("startup.certificate_assets.before_prepare")
	testhooks.Point(
		"startup.certificate_assets.after_prepare",
		testhooks.F("system_ca_count", 0),
		testhooks.F("asset_count", 0),
	)

	if a.logger != nil {
		a.logger.Info(
			"certificate assets ready",
			"system_ca_count", 0,
			"asset_count", 0,
		)
	}
	return nil
}

func (a *App) closeCertificateAssets() {
	a.certs = nil
}
