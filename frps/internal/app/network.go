package app

import (
	"context"
	"fmt"

	"github.com/zightch/frp/frps/internal/system"
)

func (a *App) initLocalNetwork(ctx context.Context) error {
	service := system.NewNetworkSnapshotService(system.Options{}, a.logger.With("subsystem", "system"))
	if err := service.Start(ctx); err != nil {
		return fmt.Errorf("init local network snapshot: %w", err)
	}

	a.network = service
	return nil
}

func (a *App) closeLocalNetwork(ctx context.Context) error {
	if a.network == nil {
		return nil
	}

	if err := a.network.Shutdown(ctx); err != nil {
		return err
	}

	a.network = nil
	a.logger.Info("local network snapshot stopped")
	return nil
}
