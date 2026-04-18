package api

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"time"
)

type Options struct {
	Addr              string
	ReadHeaderTimeout time.Duration
}

type Server struct {
	server    *http.Server
	logger    *slog.Logger
	startedAt time.Time
	version   string
}

func NewServer(options Options, logger *slog.Logger, version string) *Server {
	srv := &Server{
		logger:    logger,
		startedAt: time.Now().UTC(),
		version:   version,
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/", srv.handleIndex)
	mux.HandleFunc("/healthz", srv.handleHealth)
	mux.HandleFunc("/readyz", srv.handleHealth)
	mux.HandleFunc("/api/v1/healthz", srv.handleHealth)

	srv.server = &http.Server{
		Addr:              options.Addr,
		Handler:           srv.loggingMiddleware(mux),
		ReadHeaderTimeout: options.ReadHeaderTimeout,
	}

	return srv
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

func (s *Server) handleIndex(writer http.ResponseWriter, request *http.Request) {
	if request.URL.Path != "/" {
		http.NotFound(writer, request)
		return
	}

	writer.Header().Set("Content-Type", "text/plain; charset=utf-8")
	writer.WriteHeader(http.StatusOK)
	_, _ = writer.Write([]byte("frps management API is running\n"))
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
