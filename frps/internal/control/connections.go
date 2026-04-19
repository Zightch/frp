package control

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net"

	"github.com/zightch/frp/frps/pkg/protocol"
	"github.com/zightch/frp/frps/pkg/transport"
)

type Logger interface {
	Info(msg string, args ...any)
	Warn(msg string, args ...any)
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

func logConnection(logger *slog.Logger, level slog.Level, message string, err error) {
	if isExpectedConnectionClose(err) {
		logger.Log(context.Background(), slog.LevelInfo, message, "error", err)
		return
	}
	logger.Log(context.Background(), level, message, "error", err)
}

func connectionErrorDetails(err error) (slog.Level, string) {
	if err == nil {
		return slog.LevelInfo, "completed"
	}
	if isExpectedConnectionClose(err) {
		return slog.LevelInfo, connectionReason(err)
	}
	return slog.LevelWarn, connectionReason(err)
}

func isExpectedConnectionClose(err error) bool {
	if err == nil {
		return true
	}
	if errors.Is(err, io.EOF) || errors.Is(err, net.ErrClosed) {
		return true
	}

	var netErr net.Error
	return errors.As(err, &netErr) && netErr.Timeout()
}

func connectionReason(err error) string {
	switch {
	case err == nil:
		return "completed"
	case errors.Is(err, io.EOF):
		return "eof"
	case errors.Is(err, net.ErrClosed):
		return "closed"
	case errors.Is(err, transport.ErrFrameTooSmall):
		return "invalid frame length below minimum"
	case errors.Is(err, transport.ErrFrameTooLarge):
		return "invalid frame length above maximum"
	}

	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return "timeout"
	}

	var protocolErr *protocol.ProtocolError
	if errors.As(err, &protocolErr) {
		return protocolErr.Message
	}

	return err.Error()
}
