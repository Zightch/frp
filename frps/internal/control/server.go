package control

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"sync"
)

type Options struct {
	Addr string
}

type Server struct {
	options  Options
	logger   *slog.Logger
	listener net.Listener

	mu        sync.Mutex
	closeOnce sync.Once
	connWG    sync.WaitGroup
}

func NewServer(options Options, logger *slog.Logger) *Server {
	return &Server{
		options: options,
		logger:  logger,
	}
}

func (s *Server) ListenAndServe(ctx context.Context) error {
	listenConfig := net.ListenConfig{}
	listener, err := listenConfig.Listen(ctx, "tcp", s.options.Addr)
	if err != nil {
		return err
	}

	s.mu.Lock()
	s.listener = listener
	s.mu.Unlock()

	s.logger.Info("frpc control listener ready", "addr", s.options.Addr)

	for {
		conn, err := listener.Accept()
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return nil
			}
			return err
		}

		s.connWG.Add(1)
		go s.handleConnection(conn)
	}
}

func (s *Server) Shutdown(ctx context.Context) error {
	s.closeOnce.Do(func() {
		s.mu.Lock()
		if s.listener != nil {
			_ = s.listener.Close()
		}
		s.mu.Unlock()
	})

	done := make(chan struct{})
	go func() {
		defer close(done)
		s.connWG.Wait()
	}()

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-done:
		return nil
	}
}

func (s *Server) handleConnection(conn net.Conn) {
	defer s.connWG.Done()
	defer conn.Close()

	logger := s.logger.With("remote_addr", conn.RemoteAddr().String())
	logger.Info("frpc control connection accepted")
	logger.Info("frpc control connection closed", "reason", "protocol not implemented")
}
