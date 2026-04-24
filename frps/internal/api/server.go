package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	authn "github.com/zightch/frp/frps/internal/auth"
	"github.com/zightch/frp/frps/internal/config"
	"github.com/zightch/frp/frps/internal/storage"
	"github.com/zightch/frp/frps/internal/system"
	"github.com/zightch/frp/frps/internal/testhooks"
)

type Options struct {
	Addr              string
	ReadHeaderTimeout time.Duration
	Store             *storage.SQL
	Network           system.SnapshotReader
	RuntimeRefresher  GroupRuntimeRefresher
	RuntimeStatus     TunnelRuntimeStatusReader
	Auth              *authn.Manager
	WebUIDistDir      string
	WebUIPathPrefix   string
}

type GroupRuntimeRefresher interface {
	RefreshGroup(groupID int64)
}

type TunnelRuntimeStatusReader interface {
	TunnelRuntimeIssues() map[int64]string
}

type Server struct {
	server    *http.Server
	logger    *slog.Logger
	startedAt time.Time
	version   string
	auth      *authn.Manager
	manager   *managementService
	webui     http.Handler
	webuiPath string

	mu      sync.RWMutex
	visible bool
}

func NewServer(options Options, logger *slog.Logger, version string) (*Server, error) {
	webuiPathPrefix, err := config.NormalizeWebUIPathPrefix(options.WebUIPathPrefix)
	if err != nil {
		return nil, fmt.Errorf("normalize webui path prefix: %w", err)
	}

	webuiHandler, fallbackReason, err := newWebUIHandler(options.WebUIDistDir, webuiPathPrefix)
	if err != nil {
		return nil, fmt.Errorf("init webui handler: %w", err)
	}
	if fallbackReason != "" && logger != nil {
		logger.Warn(
			"webui dist unavailable; serving placeholder page",
			"dist_dir", options.WebUIDistDir,
			"reason", fallbackReason,
		)
	}

	srv := &Server{
		logger:    logger,
		startedAt: time.Now().UTC(),
		version:   version,
		auth:      options.Auth,
		manager:   newManagementService(options.Store, options.Network, options.RuntimeRefresher, options.RuntimeStatus),
		webui:     webuiHandler,
		webuiPath: webuiPathPrefix,
	}

	apiMux := http.NewServeMux()
	apiMux.HandleFunc("/healthz", srv.handleHealth)
	apiMux.HandleFunc("/readyz", srv.handleHealth)
	apiMux.HandleFunc("/api/v1/healthz", srv.handleHealth)
	apiMux.HandleFunc("/api/v1/auth/state", srv.handleAuthState)
	apiMux.HandleFunc("/api/v1/auth/init", srv.handleAuthInit)
	apiMux.HandleFunc("/api/v1/auth/challenge", srv.handleAuthChallenge)
	apiMux.HandleFunc("/api/v1/auth/login", srv.handleAuthLogin)
	apiMux.HandleFunc("/api/v1/auth/session", srv.handleAuthSession)
	apiMux.HandleFunc("/api/v1/auth/logout", srv.handleAuthLogout)
	apiMux.HandleFunc("/api/v1/proxy-groups", srv.handleProxyGroups)
	apiMux.HandleFunc("/api/v1/proxy-groups/", srv.handleProxyGroupResource)
	apiMux.HandleFunc("/api/v1/tunnels", srv.handleTunnels)
	apiMux.HandleFunc("/api/v1/tunnels/", srv.handleTunnelResource)
	apiMux.HandleFunc("/api/v1/local-ips", srv.handleLocalIPs)

	srv.server = &http.Server{
		Addr:              options.Addr,
		Handler:           srv.loggingMiddleware(newManagementHTTPHandler(apiMux, srv.webui, webuiPathPrefix)),
		ReadHeaderTimeout: options.ReadHeaderTimeout,
	}

	return srv, nil
}

type managementHTTPHandler struct {
	api             http.Handler
	webui           http.Handler
	webuiPathPrefix string
}

func newManagementHTTPHandler(apiHandler http.Handler, webuiHandler http.Handler, webuiPathPrefix string) http.Handler {
	return &managementHTTPHandler{
		api:             apiHandler,
		webui:           webuiHandler,
		webuiPathPrefix: webuiPathPrefix,
	}
}

func (h *managementHTTPHandler) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	requestPath := request.URL.Path
	if requestPath == "" {
		requestPath = "/"
	}

	if h.webuiPathPrefix == "" {
		if isManagementAPIPath(requestPath) {
			h.api.ServeHTTP(writer, request)
			return
		}
		h.webui.ServeHTTP(writer, request)
		return
	}

	if isManagementAPIPath(requestPath) {
		h.api.ServeHTTP(writer, request)
		return
	}

	if requestPath == "/" || requestPath == h.webuiPathPrefix {
		http.Redirect(writer, request, h.webuiPathPrefix+"/", http.StatusTemporaryRedirect)
		return
	}

	if requestPath == h.webuiPathPrefix+"/" || strings.HasPrefix(requestPath, h.webuiPathPrefix+"/") {
		strippedRequest := request.Clone(request.Context())
		strippedPath := strings.TrimPrefix(requestPath, h.webuiPathPrefix)
		if strippedPath == "" {
			strippedPath = "/"
		}
		strippedRequest.URL.Path = strippedPath
		if request.URL.RawPath != "" {
			strippedRequest.URL.RawPath = strings.TrimPrefix(request.URL.RawPath, h.webuiPathPrefix)
		}

		if isManagementAPIPath(strippedPath) {
			h.api.ServeHTTP(writer, strippedRequest)
			return
		}
		h.webui.ServeHTTP(writer, strippedRequest)
		return
	}

	http.NotFound(writer, request)
}

func isManagementAPIPath(path string) bool {
	switch {
	case path == "/healthz", path == "/readyz", path == "/api/v1/healthz":
		return true
	case path == "/api/v1/auth/state",
		path == "/api/v1/auth/init",
		path == "/api/v1/auth/challenge",
		path == "/api/v1/auth/login",
		path == "/api/v1/auth/session",
		path == "/api/v1/auth/logout",
		path == "/api/v1/proxy-groups",
		path == "/api/v1/tunnels",
		path == "/api/v1/local-ips":
		return true
	case strings.HasPrefix(path, "/api/v1/proxy-groups/"), strings.HasPrefix(path, "/api/v1/tunnels/"):
		return true
	default:
		return false
	}
}

func (s *Server) Handler() http.Handler {
	return s.server.Handler
}

func (s *Server) ListenAndServe() error {
	testhooks.Point("startup.management_api.before_open", testhooks.F("addr", s.server.Addr))
	listener, err := net.Listen("tcp", s.server.Addr)
	if err != nil {
		return err
	}
	s.logger.Info("management api listening", "addr", s.server.Addr)
	s.mu.Lock()
	s.visible = true
	s.mu.Unlock()
	testhooks.Point("startup.management_api.after_open", testhooks.F("addr", s.server.Addr))
	err = s.server.Serve(listener)
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

func (s *Server) Shutdown(ctx context.Context) error {
	s.mu.Lock()
	s.visible = false
	s.mu.Unlock()
	return s.server.Shutdown(ctx)
}

func (s *Server) Visible() bool {
	if s == nil {
		return false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.visible
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
