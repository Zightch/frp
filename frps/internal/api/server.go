package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	authn "github.com/zightch/frp/frps/internal/auth"
	"github.com/zightch/frp/frps/internal/storage"
	"github.com/zightch/frp/frps/internal/system"
)

type Options struct {
	Addr              string
	ReadHeaderTimeout time.Duration
	Store             *storage.SQL
	Network           system.SnapshotReader
	Auth              *authn.Manager
	WebUIDistDir      string
}

type Server struct {
	server    *http.Server
	logger    *slog.Logger
	startedAt time.Time
	version   string
	auth      *authn.Manager
	manager   *managementService
	webui     http.Handler
}

func NewServer(options Options, logger *slog.Logger, version string) (*Server, error) {
	webuiHandler, err := newWebUIHandler(options.WebUIDistDir)
	if err != nil {
		return nil, fmt.Errorf("init webui handler: %w", err)
	}

	srv := &Server{
		logger:    logger,
		startedAt: time.Now().UTC(),
		version:   version,
		auth:      options.Auth,
		manager:   newManagementService(options.Store, options.Network),
		webui:     webuiHandler,
	}

	mux := http.NewServeMux()
	mux.Handle("/", srv.webui)
	mux.HandleFunc("/healthz", srv.handleHealth)
	mux.HandleFunc("/readyz", srv.handleHealth)
	mux.HandleFunc("/api/v1/healthz", srv.handleHealth)
	mux.HandleFunc("/api/v1/auth/state", srv.handleAuthState)
	mux.HandleFunc("/api/v1/auth/init", srv.handleAuthInit)
	mux.HandleFunc("/api/v1/auth/challenge", srv.handleAuthChallenge)
	mux.HandleFunc("/api/v1/auth/login", srv.handleAuthLogin)
	mux.HandleFunc("/api/v1/auth/session", srv.handleAuthSession)
	mux.HandleFunc("/api/v1/auth/logout", srv.handleAuthLogout)
	mux.HandleFunc("/api/v1/proxy-groups", srv.handleProxyGroups)
	mux.HandleFunc("/api/v1/proxy-groups/", srv.handleProxyGroupResource)
	mux.HandleFunc("/api/v1/tunnels", srv.handleTunnels)
	mux.HandleFunc("/api/v1/tunnels/", srv.handleTunnelResource)
	mux.HandleFunc("/api/v1/local-ips", srv.handleLocalIPs)

	srv.server = &http.Server{
		Addr:              options.Addr,
		Handler:           srv.loggingMiddleware(mux),
		ReadHeaderTimeout: options.ReadHeaderTimeout,
	}

	return srv, nil
}

func (s *Server) Handler() http.Handler {
	return s.server.Handler
}

func (s *Server) ListenAndServe() error {
	s.logger.Info("management api listening", "addr", s.server.Addr)
	err := s.server.ListenAndServe()
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

func (s *Server) Shutdown(ctx context.Context) error {
	return s.server.Shutdown(ctx)
}

func (s *Server) handleHealth(writer http.ResponseWriter, request *http.Request) {
	response := map[string]any{
		"status":         "ok",
		"service":        "frps",
		"version":        s.version,
		"started_at":     s.startedAt.Format(time.RFC3339),
		"uptime_seconds": int(time.Since(s.startedAt).Seconds()),
	}

	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(writer).Encode(response)
}

func (s *Server) loggingMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		start := time.Now()
		recorder := &statusRecorder{ResponseWriter: writer, status: http.StatusOK}
		next.ServeHTTP(recorder, request)

		s.logger.Info(
			"management api request completed",
			"method", request.Method,
			"path", request.URL.Path,
			"remote_addr", request.RemoteAddr,
			"status", recorder.status,
			"duration", time.Since(start),
		)
	})
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(statusCode int) {
	r.status = statusCode
	r.ResponseWriter.WriteHeader(statusCode)
}
