package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/zightch/frp/frps/internal/api"
	"github.com/zightch/frp/frps/internal/config"
	"github.com/zightch/frp/frps/internal/control"
	"github.com/zightch/frp/frps/internal/storage"
)

type App struct {
	config  config.Config
	logger  *slog.Logger
	version string
	api     *api.Server
	control *control.Server
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

	if err := a.initDatabase(ctx); err != nil {
		return err
	}

	a.api = api.NewServer(
		api.Options{
			Addr:              a.config.ManagementListenAddr,
			ReadHeaderTimeout: a.config.ReadHeaderTimeoutDuration(),
			Store:             a.store,
		},
		a.logger.With("subsystem", "api"),
		a.version,
	)
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

	a.closeDatabase()

	if len(errs) == 0 {
		a.logger.Info("frps shutdown completed")
		return nil
	}

	return errors.Join(errs...)
}
