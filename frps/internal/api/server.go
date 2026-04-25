package api

import (
	"context"
	"crypto/tls"
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
	"github.com/zightch/frp/frps/internal/settings/entrycerts"
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
	ControlTLSRuntime ControlTLSRuntime
}

type GroupRuntimeRefresher interface {
	RefreshGroup(groupID int64)
}

type TunnelRuntimeStatusReader interface {
	TunnelRuntimeIssues() map[int64]string
}

type ControlTLSRuntime interface {
	ConfigureControlTLS(binding *entrycerts.ResolvedBinding) error
	ClearControlTLS() error
}

type Server struct {
	logger            *slog.Logger
	startedAt         time.Time
	version           string
	auth              *authn.Manager
	manager           *managementService
	webui             http.Handler
	webuiPath         string
	handler           http.Handler
	addr              string
	readHeaderTimeout time.Duration
	controlTLS        ControlTLSRuntime

	mu      sync.RWMutex
	visible bool

	activeServer   *http.Server
	activeListener net.Listener
	reloadCh       chan struct{}
	shutdownCh     chan struct{}
	closeOnce      sync.Once

	webuiTLSMu          sync.RWMutex
	webuiHTTPSEnabled   bool
	webuiTLSCertificate *tls.Certificate
}

const managementReloadTimeout = 5 * time.Second

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
		logger:            logger,
		startedAt:         time.Now().UTC(),
		version:           version,
		auth:              options.Auth,
		manager:           newManagementService(options.Store, options.Network, options.RuntimeRefresher, options.RuntimeStatus),
		webui:             webuiHandler,
		webuiPath:         webuiPathPrefix,
		addr:              options.Addr,
		readHeaderTimeout: options.ReadHeaderTimeout,
		controlTLS:        options.ControlTLSRuntime,
		reloadCh:          make(chan struct{}, 1),
		shutdownCh:        make(chan struct{}),
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
	apiMux.HandleFunc(entryCertificatesPathSettings, srv.handleEntryCertificates)
	apiMux.HandleFunc(entryCertificatesPathSettings+"/", srv.handleEntryCertificateResource)
	apiMux.HandleFunc("/api/v1/proxy-groups", srv.handleProxyGroups)
	apiMux.HandleFunc("/api/v1/proxy-groups/", srv.handleProxyGroupResource)
	apiMux.HandleFunc("/api/v1/tunnels", srv.handleTunnels)
	apiMux.HandleFunc("/api/v1/tunnels/", srv.handleTunnelResource)
	apiMux.HandleFunc("/api/v1/local-ips", srv.handleLocalIPs)
	apiMux.HandleFunc("/api/v1/certificate-assets", srv.handleCertificateAssets)
	apiMux.HandleFunc("/api/v1/certificate-assets/upload", srv.handleCertificateAssetUpload)
	apiMux.HandleFunc("/api/v1/certificate-assets/paste", srv.handleCertificateAssetPaste)
	apiMux.HandleFunc("/api/v1/certificate-assets/generate", srv.handleCertificateAssetGenerate)
	apiMux.HandleFunc("/api/v1/certificate-assets/", srv.handleCertificateAssetResource)

	srv.handler = srv.loggingMiddleware(newManagementHTTPHandler(apiMux, srv.webui, webuiPathPrefix))
	if srv.manager != nil {
		srv.manager.server = srv
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
		path == entryCertificatesPathSettings,
		path == "/api/v1/proxy-groups",
		path == "/api/v1/tunnels",
		path == "/api/v1/local-ips",
		path == "/api/v1/certificate-assets",
		path == "/api/v1/certificate-assets/upload",
		path == "/api/v1/certificate-assets/paste",
		path == "/api/v1/certificate-assets/generate":
		return true
	case strings.HasPrefix(path, "/api/v1/proxy-groups/"),
		strings.HasPrefix(path, entryCertificatesPathSettings+"/"),
		strings.HasPrefix(path, "/api/v1/tunnels/"),
		strings.HasPrefix(path, "/api/v1/certificate-assets/"):
		return true
	case strings.HasPrefix(path, "/api/"):
		return true
	default:
		return false
	}
}

func (s *Server) Handler() http.Handler {
	if s == nil {
		return nil
	}
	return s.handler
}

func (s *Server) ListenAndServe() error {
	if s == nil {
		return nil
	}

	for {
		httpServer, listener, httpsEnabled, err := s.openManagementListener()
		if err != nil {
			return err
		}

		serveErrCh := make(chan error, 1)
		go func() {
			serveErrCh <- httpServer.Serve(listener)
		}()

		select {
		case <-s.reloadCh:
			if err := s.shutdownActiveManagementServer(false); err != nil {
				return err
			}
			err = <-serveErrCh
			if err != nil && !errors.Is(err, http.ErrServerClosed) {
				return err
			}
			s.logger.Info("management api listener reloaded", "addr", s.addr, "https", httpsEnabled)
		case <-s.shutdownCh:
			if err := s.shutdownActiveManagementServer(true); err != nil && !errors.Is(err, context.Canceled) {
				return err
			}
			err = <-serveErrCh
			if err != nil && !errors.Is(err, http.ErrServerClosed) {
				return err
			}
			return nil
		case err = <-serveErrCh:
			if errors.Is(err, http.ErrServerClosed) {
				select {
				case <-s.shutdownCh:
					return nil
				default:
				}
				continue
			}
			return err
		}
	}
}

func (s *Server) Shutdown(ctx context.Context) error {
	if s == nil {
		return nil
	}

	var shutdownErr error
	s.closeOnce.Do(func() {
		close(s.shutdownCh)
	})

	done := make(chan struct{})
	go func() {
		defer close(done)
		shutdownErr = s.shutdownActiveManagementServer(true)
	}()

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-done:
		return shutdownErr
	}
}

func (s *Server) Visible() bool {
	if s == nil {
		return false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.visible
}

func (s *Server) ConfigureControlTLS(binding *entrycerts.ResolvedBinding) error {
	if s == nil || s.controlTLS == nil {
		return fmt.Errorf("control tls runtime is unavailable")
	}
	return s.controlTLS.ConfigureControlTLS(binding)
}

func (s *Server) ClearControlTLS() error {
	if s == nil || s.controlTLS == nil {
		return fmt.Errorf("control tls runtime is unavailable")
	}
	return s.controlTLS.ClearControlTLS()
}

func (s *Server) EnableWebUIHTTPS(binding *entrycerts.ResolvedBinding) error {
	if s == nil {
		return fmt.Errorf("management api server is unavailable")
	}
	if binding == nil {
		return fmt.Errorf("webui https binding is nil")
	}

	certCopy := binding.TLSCertificate
	s.webuiTLSMu.Lock()
	wasEnabled := s.webuiHTTPSEnabled
	s.webuiHTTPSEnabled = true
	s.webuiTLSCertificate = &certCopy
	s.webuiTLSMu.Unlock()

	if !wasEnabled {
		s.requestReload()
	}
	return nil
}

func (s *Server) DisableWebUIHTTPS() error {
	if s == nil {
		return fmt.Errorf("management api server is unavailable")
	}

	s.webuiTLSMu.Lock()
	wasEnabled := s.webuiHTTPSEnabled
	s.webuiHTTPSEnabled = false
	s.webuiTLSCertificate = nil
	s.webuiTLSMu.Unlock()

	if wasEnabled {
		s.requestReload()
	}
	return nil
}

func (s *Server) openManagementListener() (*http.Server, net.Listener, bool, error) {
	testhooks.Point("startup.management_api.before_open", testhooks.F("addr", s.addr))
	listener, err := net.Listen("tcp", s.addr)
	if err != nil {
		return nil, nil, false, err
	}

	httpServer := &http.Server{
		Addr:              s.addr,
		Handler:           s.handler,
		ReadHeaderTimeout: s.readHeaderTimeout,
	}

	httpsEnabled := s.webuiHTTPSState()
	if httpsEnabled {
		listener = tls.NewListener(listener, &tls.Config{
			MinVersion:     tls.VersionTLS12,
			GetCertificate: s.currentWebUITLSCertificate,
		})
	}

	s.mu.Lock()
	s.activeServer = httpServer
	s.activeListener = listener
	s.visible = true
	s.mu.Unlock()

	s.logger.Info("management api listening", "addr", s.addr, "https", httpsEnabled)
	testhooks.Point("startup.management_api.after_open", testhooks.F("addr", s.addr))
	return httpServer, listener, httpsEnabled, nil
}

func (s *Server) shutdownActiveManagementServer(reloading bool) error {
	s.mu.Lock()
	httpServer := s.activeServer
	s.visible = false
	s.mu.Unlock()
	if httpServer == nil {
		return nil
	}

	timeout := managementReloadTimeout
	if !reloading {
		timeout = 10 * time.Second
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	err := httpServer.Shutdown(ctx)

	s.mu.Lock()
	if s.activeServer == httpServer {
		s.activeServer = nil
		s.activeListener = nil
	}
	s.mu.Unlock()
	return err
}

func (s *Server) requestReload() {
	if s == nil {
		return
	}
	select {
	case s.reloadCh <- struct{}{}:
	default:
	}
}

func (s *Server) webuiHTTPSState() bool {
	if s == nil {
		return false
	}
	s.webuiTLSMu.RLock()
	defer s.webuiTLSMu.RUnlock()
	return s.webuiHTTPSEnabled && s.webuiTLSCertificate != nil
}

func (s *Server) currentWebUITLSCertificate(*tls.ClientHelloInfo) (*tls.Certificate, error) {
	if s == nil {
		return nil, fmt.Errorf("management api server is unavailable")
	}
	s.webuiTLSMu.RLock()
	defer s.webuiTLSMu.RUnlock()
	if !s.webuiHTTPSEnabled || s.webuiTLSCertificate == nil {
		return nil, fmt.Errorf("webui https certificate is unavailable")
	}
	return s.webuiTLSCertificate, nil
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
