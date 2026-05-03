package wiring

import (
	"io"
	"log/slog"
	"net"

	controlprotocolerrors "github.com/zightch/frp/frps/internal/control/protocol/errors"
	controlhandshake "github.com/zightch/frp/frps/internal/control/protocol/handshake"
	"github.com/zightch/frp/frps/pkg/protocol"
)

func (s *Server) handleTCPWorkConnection(conn net.Conn, initialFrame protocol.Frame, logger *slog.Logger) {
	logger.Info("frpc tcp work connection accepted")

	conn, session, err := s.negotiateTCPWork(conn, initialFrame)
	if err != nil {
		level, reason := controlprotocolerrors.ConnectionDetails(err)
		logConnection(logger, level, "frpc tcp work handshake failed", err)
		logger.Info("frpc tcp work connection closed", "reason", reason)
		return
	}

	group := session.CurrentGroup()
	logger = logger.With(
		"session_id", session.ID,
		"group_id", group.ID,
		"group_name", group.Name,
	)
	logger.Info("frpc tcp work connection ready")

	if _, err := io.Copy(io.Discard, conn); err != nil {
		level, reason := controlprotocolerrors.ConnectionDetails(err)
		logConnection(logger, level, "frpc tcp work connection ended", err)
		logger.Info("frpc tcp work connection closed", "reason", reason)
		return
	}

	logger.Info("frpc tcp work connection closed", "reason", "completed")
}

func (s *Server) negotiateTCPWork(conn net.Conn, initialFrame protocol.Frame) (net.Conn, *sessionState, error) {
	if initialFrame.Type != protocol.TypeTCPWorkHello {
		return nil, nil, s.replyError(
			conn,
			initialFrame.RequestID,
			initialFrame.StreamID,
			protocol.ErrorCodeProtocolBadBody,
			"expected tcp.work.hello, got %s",
			initialFrame.Type.String(),
		)
	}
	if initialFrame.RequestID == 0 {
		return nil, nil, s.replyError(conn, 0, initialFrame.StreamID, protocol.ErrorCodeProtocolBadBody, "tcp.work.hello requestId must be non-zero")
	}
	if initialFrame.StreamID != 0 {
		return nil, nil, s.replyError(conn, initialFrame.RequestID, initialFrame.StreamID, protocol.ErrorCodeProtocolBadBody, "tcp.work.hello streamId must be zero")
	}

	hello, err := protocol.UnmarshalTCPWorkHello(initialFrame.Body)
	if err != nil {
		return nil, nil, s.replyProtocolError(conn, initialFrame, err)
	}

	runtime := s.runtimeExecutor(hello.SessionID)
	if runtime == nil || runtime.session == nil {
		return nil, nil, s.replyError(
			conn,
			initialFrame.RequestID,
			0,
			protocol.ErrorCodeAuthSessionNotFound,
			"session %d not found",
			hello.SessionID,
		)
	}
	session := runtime.session
	group := session.CurrentGroup()
	selectedMode, err := controlhandshake.SelectTransportSecurityMode(group, hello.SupportedSecurityModes, s.controlTLS)
	if err != nil {
		return nil, nil, s.replyProtocolErrorWithSession(conn, session, initialFrame, err)
	}

	serverHelloBody, err := protocol.MarshalTCPWorkServerHello(protocol.TCPWorkServerHello{
		SelectedSecurityMode: selectedMode,
	})
	if err != nil {
		return nil, nil, err
	}
	if err := s.writeFrameWithSession(conn, session, protocol.Frame{
		Type:      protocol.TypeTCPWorkServerHello,
		RequestID: initialFrame.RequestID,
		Body:      serverHelloBody,
	}); err != nil {
		return nil, nil, err
	}

	if selectedMode == protocol.TransportSecurityModeTLS {
		conn, err = controlhandshake.UpgradeControlConnToTLS(conn, s.clock, s.options.ReadTimeout, s.controlTLS)
		if err != nil {
			return nil, nil, err
		}
	}

	registerFrame, err := s.readFrameWithSessionTimeout(conn, session, s.options.ReadTimeout)
	if err != nil {
		return nil, nil, s.replyProtocolErrorWithSession(conn, session, registerFrame, err)
	}
	if registerFrame.Type != protocol.TypeTCPWorkRegister {
		return nil, nil, s.replyErrorWithSession(
			conn,
			session,
			registerFrame.RequestID,
			registerFrame.StreamID,
			protocol.ErrorCodeProtocolBadBody,
			"expected tcp.work.register, got %s",
			registerFrame.Type.String(),
		)
	}
	if registerFrame.RequestID == 0 {
		return nil, nil, s.replyErrorWithSession(conn, session, 0, registerFrame.StreamID, protocol.ErrorCodeProtocolBadBody, "tcp.work.register requestId must be non-zero")
	}
	if registerFrame.StreamID != 0 {
		return nil, nil, s.replyErrorWithSession(conn, session, registerFrame.RequestID, registerFrame.StreamID, protocol.ErrorCodeProtocolBadBody, "tcp.work.register streamId must be zero")
	}

	register, err := protocol.UnmarshalTCPWorkRegister(registerFrame.Body)
	if err != nil {
		return nil, nil, s.replyProtocolErrorWithSession(conn, session, registerFrame, err)
	}
	if !session.MatchTCPWorkSecret(register.WorkSecret) {
		return nil, nil, s.replyErrorWithSession(
			conn,
			session,
			registerFrame.RequestID,
			0,
			protocol.ErrorCodeAuthWorkSecretMismatch,
			"tcp work secret mismatch",
		)
	}

	readyBody, err := protocol.MarshalTCPWorkReady(protocol.TCPWorkReady{})
	if err != nil {
		return nil, nil, err
	}
	if err := s.writeFrameWithSession(conn, session, protocol.Frame{
		Type:      protocol.TypeTCPWorkReady,
		RequestID: registerFrame.RequestID,
		Body:      readyBody,
	}); err != nil {
		return nil, nil, err
	}
	return conn, session, nil
}
