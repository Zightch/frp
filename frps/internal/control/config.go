package control

import (
	"context"
	"log/slog"
	"net"
	"strings"

	"github.com/zightch/frp/frps/pkg/protocol"
)

func (s *Server) handleConfigAck(conn net.Conn, logger *slog.Logger, session *sessionState, frame protocol.Frame) error {
	if frame.RequestID == 0 {
		return s.replyErrorWithSession(conn, session, 0, frame.StreamID, protocol.ErrorCodeProtocolBadBody, "config.ack requestId must be non-zero")
	}
	if frame.StreamID != 0 {
		return s.replyErrorWithSession(conn, session, frame.RequestID, frame.StreamID, protocol.ErrorCodeProtocolBadBody, "config.ack streamId must be zero")
	}
	if session.pendingConfigRequestID == 0 || frame.RequestID != session.pendingConfigRequestID {
		return s.replyErrorWithSession(conn, session, frame.RequestID, frame.StreamID, protocol.ErrorCodeProtocolBadBody, "unexpected config.ack requestId %d", frame.RequestID)
	}

	ack, err := protocol.UnmarshalConfigAck(frame.Body)
	if err != nil {
		return s.replyProtocolErrorWithSession(conn, session, frame, err)
	}
	if ack.ConfigVersion != session.Snapshot.Version {
		return s.replyErrorWithSession(
			conn,
			session,
			frame.RequestID,
			0,
			protocol.ErrorCodeProtocolBadBody,
			"config.ack version mismatch: got %d want %d",
			ack.ConfigVersion,
			session.Snapshot.Version,
		)
	}
	if ack.Status == protocol.StatusError {
		return s.replyErrorWithSession(
			conn,
			session,
			frame.RequestID,
			0,
			protocol.ErrorCodeConfigApplyFailed,
			"client rejected config version %d: %s",
			ack.ConfigVersion,
			strings.TrimSpace(ack.Message),
		)
	}
	if ack.Status != protocol.StatusOK {
		return s.replyErrorWithSession(conn, session, frame.RequestID, 0, protocol.ErrorCodeProtocolBadBody, "unsupported config.ack status %d", ack.Status)
	}

	session.LastAckedConfigVersion = ack.ConfigVersion
	session.pendingConfigRequestID = 0
	logger.Info("config acknowledged", "config_version", ack.ConfigVersion, "applied_at_ms", ack.AppliedAtMs)
	return s.ensureTunnelListeners(conn, logger, session)
}

func (s *Server) pushConfig(conn net.Conn, session *sessionState) error {
	requestID := session.nextRequestID()
	body, err := protocol.MarshalConfigPush(protocol.ConfigPush{
		ConfigVersion: session.Snapshot.Version,
		GeneratedAtMs: session.Snapshot.GeneratedAtMs,
		Tunnels:       session.Snapshot.Tunnels,
	})
	if err != nil {
		return err
	}

	session.pendingConfigRequestID = requestID
	return s.writeFrameWithSession(conn, session, protocol.Frame{
		Type:      protocol.TypeConfigPush,
		RequestID: requestID,
		Body:      body,
	})
}

func (s *Server) loadGroupRuntime(tokenID [16]byte) (GroupRuntime, error) {
	ctx, cancel := context.WithTimeout(context.Background(), s.options.ReadTimeout)
	defer cancel()
	return s.repo.LoadGroupRuntime(ctx, tokenID)
}
