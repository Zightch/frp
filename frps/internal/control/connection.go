package control

import (
	"context"
	"log/slog"
	"net"

	controlprotocolerrors "github.com/zightch/frp/frps/internal/control/protocol/errors"
	controlsession "github.com/zightch/frp/frps/internal/control/session"
	"github.com/zightch/frp/frps/pkg/protocol"
)

func (s *Server) handleConnection(conn net.Conn) {
	defer s.connWG.Done()
	defer s.unregisterConn(conn)
	defer conn.Close()

	logger := s.logger.With("remote_addr", conn.RemoteAddr().String())
	logger.Info("frpc control connection accepted")

	if s.repo == nil {
		logger.Error("frpc control connection rejected", "reason", "repository not configured")
		logger.Info("frpc control connection closed", "reason", "repository not configured")
		return
	}

	conn, clientID, err := s.negotiateTransport(conn)
	if err != nil {
		level, reason := controlprotocolerrors.ConnectionDetails(err)
		logConnection(logger, level, "frpc control transport negotiation failed", err)
		logger.Info("frpc control connection closed", "reason", reason)
		return
	}

	session, agent, err := s.authenticate(conn, clientID, logger)
	if err != nil {
		level, reason := controlprotocolerrors.ConnectionDetails(err)
		logConnection(logger, level, "frpc control login failed", err)
		logger.Info("frpc control connection closed", "reason", reason)
		return
	}

	group, snapshot := session.CurrentGroupAndSnapshot()
	logger = logger.With(
		"session_id", session.ID,
		"group_id", group.ID,
		"group_name", group.Name,
	)
	defer s.unregisterRuntimeExecutor(session.ID)
	defer func() {
		if agent != nil {
			session.applyControlEvent(controlsession.ControlConnClosed{Reason: "connection closed"})
			_ = agent.Enqueue(controlsession.ControlConnClosed{Reason: "connection closed"})
		}
	}()
	defer s.shutdownSession(session)
	logger.Info(
		"frpc control login succeeded",
		"config_version", snapshot.Version,
		"tunnel_count", len(snapshot.Tunnels),
	)

	err = s.runSession(conn, logger, session, agent)
	if err != nil {
		level, reason := controlprotocolerrors.ConnectionDetails(err)
		logConnection(logger, level, "frpc control session ended", err)
		logger.Info("frpc control connection closed", "reason", reason)
		return
	}

	logger.Info("frpc control connection closed", "reason", "completed")
}

func (s *Server) runSession(conn net.Conn, logger *slog.Logger, session *sessionState, agent *controlsession.Agent) error {
	for {
		frame, err := s.readFrameWithSessionTimeout(conn, session, session.ReadTimeout)
		if err != nil {
			return s.replyProtocolErrorWithSession(conn, session, frame, err)
		}

		switch frame.Type {
		case protocol.TypeConfigAck:
			if err := s.handleConfigAck(conn, logger, session, agent, frame); err != nil {
				return err
			}
		case protocol.TypeHeartbeatPing:
			if err := s.handleHeartbeatPing(conn, session, agent, frame); err != nil {
				return err
			}
		case protocol.TypeStreamOpened:
			if err := s.handleStreamOpened(conn, session, frame); err != nil {
				return err
			}
		case protocol.TypeStreamData:
			if err := s.handleStreamData(conn, session, frame); err != nil {
				return err
			}
		case protocol.TypeStreamClose:
			if err := s.handleStreamClose(session, frame); err != nil {
				return err
			}
		case protocol.TypeUDPData:
			if err := s.handleUDPData(conn, session, frame); err != nil {
				return err
			}
		case protocol.TypeUDPClose:
			if err := s.handleUDPClose(session, frame); err != nil {
				return err
			}
		default:
			return s.replyErrorWithSession(
				conn,
				session,
				frame.RequestID,
				frame.StreamID,
				protocol.ErrorCodeProtocolBadBody,
				"unexpected message type %s",
				frame.Type.String(),
			)
		}
	}
}

func (s *Server) handleHeartbeatPing(conn net.Conn, session *sessionState, agent *controlsession.Agent, frame protocol.Frame) error {
	if frame.RequestID == 0 {
		return s.replyErrorWithSession(conn, session, 0, frame.StreamID, protocol.ErrorCodeProtocolBadBody, "heartbeat.ping requestId must be non-zero")
	}
	if frame.StreamID != 0 {
		return s.replyErrorWithSession(conn, session, frame.RequestID, frame.StreamID, protocol.ErrorCodeProtocolBadBody, "heartbeat.ping streamId must be zero")
	}

	ping, err := protocol.UnmarshalHeartbeatPing(frame.Body)
	if err != nil {
		return s.replyProtocolErrorWithSession(conn, session, frame, err)
	}
	if agent == nil || !agent.Enqueue(controlsession.HeartbeatPingReceived{
		RequestID:    frame.RequestID,
		ClientUnixMs: ping.ClientUnixMs,
	}) {
		return net.ErrClosed
	}
	return nil
}

func logConnection(logger *slog.Logger, level slog.Level, message string, err error) {
	if controlprotocolerrors.IsExpectedConnectionClose(err) {
		logger.Log(context.Background(), slog.LevelInfo, message, "error", err)
		return
	}
	logger.Log(context.Background(), level, message, "error", err)
}
