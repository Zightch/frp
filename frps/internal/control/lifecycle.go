package control

import (
	"context"
	"errors"
	"net"

	"github.com/zightch/frp/frps/internal/testhooks"
)

func (s *Server) isShuttingDown() bool {
	if s == nil || s.shutdownCh == nil {
		return false
	}

	select {
	case <-s.shutdownCh:
		return true
	default:
		return false
	}
}

func (s *Server) ListenAndServe(ctx context.Context) error {
	if s.isShuttingDown() {
		return nil
	}
	if err := s.EnsureInitialRuntimeScan(ctx); err != nil {
		return err
	}
	if s.isShuttingDown() {
		return nil
	}

	testhooks.Point("startup.control_listener.before_open", testhooks.F("addr", s.options.Addr))
	listenConfig := net.ListenConfig{}
	listener, err := listenConfig.Listen(ctx, "tcp", s.options.Addr)
	if err != nil {
		return err
	}
	if s.isShuttingDown() {
		_ = listener.Close()
		return nil
	}

	s.mu.Lock()
	s.listener = listener
	s.controlListenerOpen = true
	s.mu.Unlock()

	testhooks.Point("startup.control_listener.after_open", testhooks.F("addr", s.options.Addr))
	if s.isShuttingDown() {
		_ = listener.Close()
		return nil
	}
	s.startRuntimeIssuePolling(ctx)
	s.logger.Info("frpc control listener ready", "addr", s.options.Addr)

	for {
		conn, err := listener.Accept()
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return nil
			}
			return err
		}

		s.registerConn(conn)
		s.connWG.Add(1)
		go s.handleConnection(conn)
	}
}

func (s *Server) Shutdown(ctx context.Context) error {
	s.closeOnce.Do(func() {
		if s.shutdownCh != nil {
			close(s.shutdownCh)
		}
		s.mu.Lock()
		listener := s.listener
		s.controlListenerOpen = false
		activeConn := make([]net.Conn, 0, len(s.activeConn))
		for conn := range s.activeConn {
			activeConn = append(activeConn, conn)
		}
		runtimeScanCancel := s.runtimeScanCancel
		s.mu.Unlock()

		if runtimeScanCancel != nil {
			runtimeScanCancel()
		}
		if listener != nil {
			_ = listener.Close()
		}
		if s.supervisor != nil {
			s.supervisor.Shutdown()
		}
		for _, conn := range activeConn {
			_ = conn.Close()
		}
	})

	done := make(chan struct{})
	go func() {
		defer close(done)
		s.connWG.Wait()
		s.scanWG.Wait()
	}()

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-done:
		return nil
	}
}

func (s *Server) EnsureInitialRuntimeScan(ctx context.Context) error {
	if s == nil {
		return nil
	}

	s.initialRuntimeScanMu.Lock()
	defer s.initialRuntimeScanMu.Unlock()

	if s.initialRuntimeScanDone {
		return nil
	}
	testhooks.Point("startup.initial_scan.before_full_scan")
	if err := s.scanNonListeningTunnelRuntimeIssues(ctx); err != nil {
		return err
	}
	testhooks.Point("startup.initial_scan.after_full_scan")
	s.initialRuntimeScanDone = true
	return nil
}

func (s *Server) registerConn(conn net.Conn) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.activeConn[conn] = struct{}{}
}

func (s *Server) unregisterConn(conn net.Conn) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.activeConn, conn)
}
