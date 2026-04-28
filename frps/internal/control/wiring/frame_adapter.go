package wiring

import (
	"net"
	"time"

	controlprotocolerrors "github.com/zightch/frp/frps/internal/control/protocol/errors"
	controlframeio "github.com/zightch/frp/frps/internal/control/protocol/frameio"
	"github.com/zightch/frp/frps/pkg/protocol"
)

func (s *Server) readFrame(conn net.Conn) (protocol.Frame, error) {
	return s.readFrameWithTimeout(conn, s.options.ReadTimeout)
}

func (s *Server) readFrameWithTimeout(conn net.Conn, timeout time.Duration) (protocol.Frame, error) {
	return controlframeio.ReadFrame(s.frames, conn, timeout, s.frameContext(conn, nil))
}

func (s *Server) readFrameWithSessionTimeout(conn net.Conn, session *sessionState, timeout time.Duration) (protocol.Frame, error) {
	return controlframeio.ReadFrame(s.frames, conn, timeout, s.frameContext(conn, session))
}

func (s *Server) writeFrame(conn net.Conn, frame protocol.Frame) error {
	return s.frameWriter(conn, nil).WriteFrame(frame)
}

func (s *Server) frameWriter(conn net.Conn, session *sessionState) controlframeio.Writer {
	return controlframeio.Writer{
		Frames:       s.frames,
		Conn:         conn,
		WriteTimeout: s.options.WriteTimeout,
		Context:      s.frameContext(conn, session),
	}
}

func (s *Server) frameContext(conn net.Conn, session *sessionState) controlframeio.Context {
	context := controlframeio.SessionContext{}
	if session != nil {
		context.GroupID = session.CurrentGroupID()
		context.SessionID = session.ID
	}
	return controlframeio.ServerContext(conn, context)
}

func (s *Server) replyProtocolError(conn net.Conn, frame protocol.Frame, err error) error {
	return controlprotocolerrors.ReplyProtocolError(s.frameWriter(conn, nil), frame, err)
}

func (s *Server) replyError(conn net.Conn, requestID, streamID uint32, code uint16, format string, args ...any) error {
	return controlprotocolerrors.ReplyError(s.frameWriter(conn, nil), requestID, streamID, code, format, args...)
}

func (s *Server) writeError(conn net.Conn, requestID, streamID uint32, code uint16, retryable bool, message string) error {
	return controlprotocolerrors.WriteError(s.frameWriter(conn, nil), requestID, streamID, code, retryable, message)
}
