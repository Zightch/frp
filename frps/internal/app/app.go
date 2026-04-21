package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/zightch/frp/frps/internal/api"
	"github.com/zightch/frp/frps/internal/auth"
	"github.com/zightch/frp/frps/internal/config"
	"github.com/zightch/frp/frps/internal/control"
	"github.com/zightch/frp/frps/internal/storage"
	"github.com/zightch/frp/frps/internal/system"
)

type App struct {
	config  config.Config
	logger  *slog.Logger
	version string
	api     *api.Server
	auth    *auth.Manager
	control *control.Server
	network *system.NetworkSnapshotService
	store   *storage.SQL
}

func New(cfg config.Config, logger *slog.Logger, version string) *App {
	return &App{
		config:  cfg,
		logger:  logger,
		version: version,
	}
}

func (a *App) Run(parent context.Context) error {
	ctx, cancel := context.WithCancel(parent)
	defer cancel()

	if err := a.initAuth(); err != nil {
		return err
	}

	if err := a.initDatabase(ctx); err != nil {
		a.closeAuth()
		return err
	}
	if err := a.initLocalNetwork(ctx); err != nil {
		a.closeDatabase()
		a.closeAuth()
		return err
	}

	apiServer, err := api.NewServer(
		api.Options{
			Addr:              a.config.ManagementListenAddr,
			ReadHeaderTimeout: a.config.ReadHeaderTimeoutDuration(),
			Store:             a.store,
			Auth:              a.auth,
			WebUIDistDir:      a.config.WebUI.DistDir,
		},
		a.logger.With("subsystem", "api"),
		a.version,
	)
	if err != nil {
		_ = a.closeLocalNetwork(context.Background())
		a.closeDatabase()
		a.closeAuth()
		return fmt.Errorf("init management api: %w", err)
	}
	a.api = apiServer
	a.control = control.NewServer(
		control.Options{
			Addr:        a.config.ControlListenAddr,
			Store:       a.store,
			ReadTimeout: a.config.ReadHeaderTimeoutDuration(),
		},
		a.logger.With("subsystem", "control"),
		a.version,
	)

	errCh := make(chan error, 2)

	go func() {
		if err := a.api.ListenAndServe(); err != nil {
			errCh <- fmt.Errorf("management api: %w", err)
		}
	}()

	go func() {
		if err := a.control.ListenAndServe(ctx); err != nil {
			errCh <- fmt.Errorf("control listener: %w", err)
		}
	}()

	a.logger.Info(
		"frps started",
		"control_addr", a.config.ControlListenAddr,
		"management_addr", a.config.ManagementListenAddr,
		"webui_dist_dir", a.config.WebUI.DistDir,
		"version", a.version,
	)

	select {
	case <-ctx.Done():
		a.logger.Info("frps shutdown requested")
		return a.shutdown()
	case err := <-errCh:
		cancel()
		shutdownErr := a.shutdown()
		if shutdownErr != nil {
			return errors.Join(err, shutdownErr)
		}
		return err
	}
}

func (a *App) shutdown() error {
	shutdownCtx, cancel := context.WithTimeout(context.Background(), a.config.ShutdownTimeoutDuration())
	defer cancel()

	var errs []error

	if a.api != nil {
		if err := a.api.Shutdown(shutdownCtx); err != nil {
			errs = append(errs, fmt.Errorf("shutdown management api: %w", err))
		}
	}

	if a.control != nil {
		if err := a.control.Shutdown(shutdownCtx); err != nil && !errors.Is(err, context.Canceled) {
			errs = append(errs, fmt.Errorf("shutdown control listener: %w", err))
		}
	}

	if err := a.closeLocalNetwork(shutdownCtx); err != nil && !errors.Is(err, context.Canceled) {
		errs = append(errs, fmt.Errorf("shutdown local network snapshot: %w", err))
	}

	a.closeDatabase()
	a.closeAuth()

	if len(errs) == 0 {
		a.logger.Info("frps shutdown completed")
		return nil
	}

	return errors.Join(errs...)
}

func (a *App) closeAuth() {
	if a.auth == nil {
		return
	}
	_ = a.auth.Close()
	a.auth = nil
}
