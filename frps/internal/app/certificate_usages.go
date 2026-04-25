package app

import (
	"context"
	"fmt"

	"github.com/zightch/frp/frps/internal/certusages"
)

func (a *App) initCertificateUsages(ctx context.Context, service *certusages.Service) error {
	if a == nil || service == nil {
		return nil
	}

	items, err := service.List(ctx)
	if err != nil {
		return err
	}

	for _, item := range items {
		if !item.Enabled || item.AssetID == nil {
			continue
		}

		binding, err := service.Resolve(ctx, item.UsageType, *item.AssetID)
		if err != nil {
			return err
		}

		switch item.UsageType {
		case certusages.UsageTypeWebUIHTTPS:
			if a.api == nil {
				return fmt.Errorf("management api server is unavailable")
			}
			if err := a.api.EnableWebUIHTTPS(&binding); err != nil {
				return err
			}
		case certusages.UsageTypeControlListenerTLS:
			if a.control == nil {
				return fmt.Errorf("control server is unavailable")
			}
			if err := a.control.ConfigureControlTLS(&binding); err != nil {
				return err
			}
		}
	}

	return nil
}
