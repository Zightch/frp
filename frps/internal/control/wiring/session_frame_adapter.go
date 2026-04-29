package wiring

import (
	"errors"
	"net"

	controlprotocolerrors "github.com/zightch/frp/frps/internal/control/protocol/errors"
	"github.com/zightch/frp/frps/pkg/protocol"
)

var errRuntimeIOStopped = errors.New("runtime io no longer allowed")

type sessionFrameWriter struct {
	server  *Server
	conn    net.Conn
	session *sessionState
}

func (w sessionFrameWriter) WriteFrame(frame protocol.Frame) error {
	return w.server.writeFrameWithSession(w.conn, w.session, frame)
}

func (s *Server) sessionFrameWriter(conn net.Conn, session *sessionState) sessionFrameWriter {
	return sessionFrameWriter{
		server:  s,
		conn:    conn,
		session: session,
	}
}

func (s *Server) writeFrameWithSession(conn net.Conn, session *sessionState, frame protocol.Frame) error {
	session.WriteMu.Lock()
	defer session.WriteMu.Unlock()
	return s.frameWriter(conn, session).WriteFrame(frame)
}

func (s *Server) writeFramesWithSession(conn net.Conn, session *sessionState, frames ...protocol.Frame) error {
	session.WriteMu.Lock()
	defer session.WriteMu.Unlock()
	return s.frameWriter(conn, session).WriteFrames(frames...)
}

func (s *Server) writeRuntimeFrameWithSession(conn net.Conn, session *sessionState, configVersion uint64, frame protocol.Frame) error {
	if !session.LockRuntimeIOWrite(configVersion) {
		return errRuntimeIOStopped
	}
	defer session.UnlockRuntimeIOWrite()
	return s.frameWriter(conn, session).WriteFrame(frame)
}

func (s *Server) writeRuntimeFramesWithSession(conn net.Conn, session *sessionState, configVersion uint64, frames ...protocol.Frame) error {
	if !session.LockRuntimeIOWrite(configVersion) {
		return errRuntimeIOStopped
	}
	defer session.UnlockRuntimeIOWrite()
	return s.frameWriter(conn, session).WriteFrames(frames...)
}

func (s *Server) replyProtocolErrorWithSession(conn net.Conn, session *sessionState, frame protocol.Frame, err error) error {
	return controlprotocolerrors.ReplyProtocolError(s.sessionFrameWriter(conn, session), frame, err)
}

func (s *Server) replyErrorWithSession(conn net.Conn, session *sessionState, requestID, streamID uint32, code uint16, format string, args ...any) error {
	return controlprotocolerrors.ReplyError(s.sessionFrameWriter(conn, session), requestID, streamID, code, format, args...)
}

func (s *Server) writeErrorWithSession(conn net.Conn, session *sessionState, requestID, streamID uint32, code uint16, retryable bool, message string) error {
	return controlprotocolerrors.WriteError(s.sessionFrameWriter(conn, session), requestID, streamID, code, retryable, message)
}
