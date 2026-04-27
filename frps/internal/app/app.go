package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/zightch/frp/frps/internal/api"
	"github.com/zightch/frp/frps/internal/auth"
	"github.com/zightch/frp/frps/internal/certassets"
	"github.com/zightch/frp/frps/internal/config"
	"github.com/zightch/frp/frps/internal/control"
	"github.com/zightch/frp/frps/internal/controlv2"
	"github.com/zightch/frp/frps/internal/settings/entrycerts"
	"github.com/zightch/frp/frps/internal/storage"
	"github.com/zightch/frp/frps/internal/system"
)

type App struct {
	config    config.Config
	logger    *slog.Logger
	version   string
	api       *api.Server
	auth      *auth.Manager
	control   *control.Server
	controlV2 *controlv2.Server
	network   *system.NetworkSnapshotService
	store     *storage.SQL
	certs     *certassets.Runtime
}

var newControlServer = control.NewServer
var newControlV2Server = controlv2.NewServer

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
	if err := a.initCertificateAssets(ctx); err != nil {
		a.closeDatabase()
		a.closeAuth()
		return err
	}
	if err := a.initLocalNetwork(ctx); err != nil {
		a.closeCertificateAssets()
		a.closeDatabase()
		a.closeAuth()
		return err
	}

	a.control = newControlServer(
		control.Options{
			Addr:        a.config.ControlListenAddr,
			Store:       a.store,
			Network:     a.network,
			ReadTimeout: a.config.ReadHeaderTimeoutDuration(),
		},
		a.logger.With("subsystem", "control"),
		a.version,
	)
	if err := a.control.EnsureInitialRuntimeScan(ctx); err != nil {
		_ = a.closeLocalNetwork(context.Background())
		a.closeCertificateAssets()
		a.closeDatabase()
		a.closeAuth()
		return fmt.Errorf("init control runtime scan: %w", err)
	}
	controlV2Server, err := newControlV2Server(controlv2.Options{
		Repo: controlv2.NewSQLRepository(a.store),
	})
	if err != nil {
		_ = a.closeLocalNetwork(context.Background())
		a.closeCertificateAssets()
		a.closeDatabase()
		a.closeAuth()
		return fmt.Errorf("init control v2: %w", err)
	}
	a.controlV2 = controlV2Server

	apiServer, err := api.NewServer(
		api.Options{
			Addr:              a.config.ManagementListenAddr,
			ReadHeaderTimeout: a.config.ReadHeaderTimeoutDuration(),
			Store:             a.store,
			Network:           a.network,
			RuntimeRefresher:  groupRuntimeRefreshFanout{targets: []api.GroupRuntimeRefresher{a.control, a.controlV2}},
			RuntimeStatus:     a.control,
			Auth:              a.auth,
			WebUIDistDir:      a.config.WebUI.DistDir,
			WebUIPathPrefix:   a.config.WebUI.PathPrefix,
			ControlTLSRuntime: a.control,
		},
		a.logger.With("subsystem", "api"),
		a.version,
	)
	if err != nil {
		_ = a.closeLocalNetwork(context.Background())
		a.closeCertificateAssets()
		a.closeDatabase()
		a.closeAuth()
		return fmt.Errorf("init management api: %w", err)
	}
	a.api = apiServer
	if err := a.initEntryCertificates(ctx, entrycerts.NewService(a.store, entrycerts.ServiceOptions{})); err != nil {
		_ = a.closeLocalNetwork(context.Background())
		a.closeCertificateAssets()
		a.closeDatabase()
		a.closeAuth()
		return fmt.Errorf("init entry certificates: %w", err)
	}

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
		"webui_path_prefix", a.config.WebUI.PathPrefix,
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
	if a.controlV2 != nil {
		a.controlV2.Shutdown()
		a.controlV2 = nil
	}

	if err := a.closeLocalNetwork(shutdownCtx); err != nil && !errors.Is(err, context.Canceled) {
		errs = append(errs, fmt.Errorf("shutdown local network snapshot: %w", err))
	}

	a.closeCertificateAssets()
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
