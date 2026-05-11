package app

import (
	"context"
	"fmt"

	"github.com/zightch/frp/frps/internal/settings/entrycerts"
)

func (a *App) initEntryCertificates(ctx context.Context, service *entrycerts.Service) error {
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
		if item.Status == "broken" {
			continue
		}

		binding, err := service.Resolve(ctx, item.UsageType, *item.AssetID)
		if err != nil {
			return err
		}

		switch item.UsageType {
		case entrycerts.UsageTypeWebUIHTTPS:
			if a.api == nil {
				return fmt.Errorf("management api server is unavailable")
			}
			if err := a.api.EnableWebUIHTTPS(&binding); err != nil {
				return err
			}
		case entrycerts.UsageTypeFrpcTLS:
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
