package control

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"strings"

	"github.com/zightch/frp/frps/internal/testhooks"
	"github.com/zightch/frp/frps/pkg/protocol"
	"github.com/zightch/frp/frps/pkg/testsupport"
)

var (
	errConfigUpdateInFlight  = errors.New("config update already in flight")
	errUnexpectedConfigAck   = errors.New("unexpected config ack")
	errConfigVersionMismatch = errors.New("config version mismatch")
)

func (s *Server) handleConfigAck(conn net.Conn, logger *slog.Logger, session *sessionState, frame protocol.Frame) error {
	if frame.RequestID == 0 {
		return s.replyErrorWithSession(conn, session, 0, frame.StreamID, protocol.ErrorCodeProtocolBadBody, "config.ack requestId must be non-zero")
	}
	if frame.StreamID != 0 {
		return s.replyErrorWithSession(conn, session, frame.RequestID, frame.StreamID, protocol.ErrorCodeProtocolBadBody, "config.ack streamId must be zero")
	}

	ack, err := protocol.UnmarshalConfigAck(frame.Body)
	if err != nil {
		return s.replyProtocolErrorWithSession(conn, session, frame, err)
	}
	pendingRequestID, expectedVersion := session.configAckState()
	isInitialStartup := session.lastAckedConfigVersion() == 0
	if pendingRequestID == 0 || frame.RequestID != pendingRequestID {
		return s.replyErrorWithSession(conn, session, frame.RequestID, frame.StreamID, protocol.ErrorCodeProtocolBadBody, "unexpected config.ack requestId %d", frame.RequestID)
	}
	if ack.ConfigVersion != expectedVersion {
		return s.replyErrorWithSession(
			conn,
			session,
			frame.RequestID,
			0,
			protocol.ErrorCodeProtocolBadBody,
			"config.ack version mismatch: got %d want %d",
			ack.ConfigVersion,
			expectedVersion,
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

	testhooks.Point(
		"control.config_ack.before_accept",
		testhooks.F("group_id", session.Group.ID),
		testhooks.F("session_id", session.ID),
		testhooks.F("request_id", frame.RequestID),
		testhooks.F("config_version", ack.ConfigVersion),
	)
	if err := session.acceptConfigAck(frame.RequestID, ack.ConfigVersion); err != nil {
		switch {
		case errors.Is(err, errUnexpectedConfigAck):
			return s.replyErrorWithSession(conn, session, frame.RequestID, frame.StreamID, protocol.ErrorCodeProtocolBadBody, "unexpected config.ack requestId %d", frame.RequestID)
		case errors.Is(err, errConfigVersionMismatch):
			return s.replyErrorWithSession(
				conn,
				session,
				frame.RequestID,
				0,
				protocol.ErrorCodeProtocolBadBody,
				"config.ack version mismatch: got %d want %d",
				ack.ConfigVersion,
				expectedVersion,
			)
		default:
			return err
		}
	}
	testhooks.Point(
		"control.config_ack.after_accept",
		testhooks.F("group_id", session.Group.ID),
		testhooks.F("session_id", session.ID),
		testhooks.F("request_id", frame.RequestID),
		testhooks.F("config_version", ack.ConfigVersion),
	)
	logger.Info("config acknowledged", "config_version", ack.ConfigVersion, "applied_at_ms", ack.AppliedAtMs)
	if len(session.Snapshot.Tunnels) == 0 {
		session.setRecoveryMode(testsupport.RecoveryModeEmptyConfig)
	} else {
		session.setRecoveryMode(testsupport.RecoveryModeRunning)
	}
	session.allowTunnelRuntimeStart()
	if err := s.ensureTunnelListeners(conn, logger, session); err != nil {
		if isInitialStartup {
			if reason, ok := buildInitialStartupRejectedReason(session.currentGroup(), err); ok {
				return s.replyErrorWithSession(conn, session, frame.RequestID, 0, protocol.ErrorCodeConfigApplyFailed, "%s", reason)
			}
		}
		return err
	}
	return nil
}

func (s *Server) pushConfig(conn net.Conn, session *sessionState) error {
	group, snapshot := session.currentGroupAndSnapshot()
	return s.pushReloadConfig(conn, session, group, snapshot)
}

func (s *Server) loadGroupRuntimeByClientID(clientID [16]byte) (GroupRuntime, error) {
	ctx, cancel := context.WithTimeout(context.Background(), s.options.ReadTimeout)
	defer cancel()
	return s.repo.LoadGroupRuntimeByClientID(ctx, clientID)
}

func (s *Server) loadGroupRuntimeByID(groupID int64) (GroupRuntime, error) {
	ctx, cancel := context.WithTimeout(context.Background(), s.options.ReadTimeout)
	defer cancel()
	return s.repo.LoadGroupRuntimeByID(ctx, groupID)
}

func runtimeSnapshotForGroup(group GroupRuntime) ConfigSnapshot {
	snapshot := group.Snapshot
	if group.Enabled {
		return snapshot
	}
	snapshot.Tunnels = nil
	return snapshot
}

func (s *Server) pushReloadConfig(conn net.Conn, session *sessionState, group GroupRuntime, snapshot ConfigSnapshot) error {
	requestID := session.nextRequestID()
	body, err := protocol.MarshalConfigPush(protocol.ConfigPush{
		ConfigVersion: snapshot.Version,
		GeneratedAtMs: snapshot.GeneratedAtMs,
		Tunnels:       snapshot.Tunnels,
	})
	if err != nil {
		return err
	}
	if err := session.reconfigure(group, snapshot, requestID); err != nil {
		return err
	}
	if len(snapshot.Tunnels) == 0 {
		session.setRecoveryMode(testsupport.RecoveryModePendingEmptyConfig)
	} else {
		session.setRecoveryMode(testsupport.RecoveryModePendingFullConfig)
	}
	testhooks.Point(
		"control.config_push.before_write",
		testhooks.F("group_id", group.ID),
		testhooks.F("session_id", session.ID),
		testhooks.F("request_id", requestID),
		testhooks.F("config_version", snapshot.Version),
		testhooks.F("tunnel_count", len(snapshot.Tunnels)),
	)
	if err := s.writeFrameWithSession(conn, session, protocol.Frame{
		Type:      protocol.TypeConfigPush,
		RequestID: requestID,
		Body:      body,
	}); err != nil {
		session.clearPendingConfigRequest(requestID)
		return err
	}
	testhooks.Point(
		"control.config_push.after_write",
		testhooks.F("group_id", group.ID),
		testhooks.F("session_id", session.ID),
		testhooks.F("request_id", requestID),
		testhooks.F("config_version", snapshot.Version),
		testhooks.F("tunnel_count", len(snapshot.Tunnels)),
	)
	return nil
}
