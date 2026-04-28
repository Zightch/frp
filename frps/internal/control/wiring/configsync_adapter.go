package wiring

import (
	"errors"
	"log/slog"
	"net"

	controlconfigsync "github.com/zightch/frp/frps/internal/control/protocol/configsync"
	controlruntime "github.com/zightch/frp/frps/internal/control/runtime"
	controlsession "github.com/zightch/frp/frps/internal/control/session"
	"github.com/zightch/frp/frps/internal/testhooks"
	"github.com/zightch/frp/frps/pkg/protocol"
)

var (
	errConfigUpdateInFlight  = errors.New("config update already in flight")
	errUnexpectedConfigAck   = controlconfigsync.ErrUnexpectedAck
	errConfigVersionMismatch = controlconfigsync.ErrVersionMismatch
)

func (s *Server) handleConfigAck(conn net.Conn, logger *slog.Logger, session *sessionState, agent *controlsession.Agent, frame protocol.Frame) error {
	pendingRequestID, expectedVersion := session.ConfigAckState()
	ack, err := controlconfigsync.DecodeAckFrame(frame, controlconfigsync.AckState{
		PendingRequestID: pendingRequestID,
		ExpectedVersion:  expectedVersion,
	})
	if err != nil {
		return s.replyProtocolErrorWithSession(conn, session, frame, err)
	}

	currentGroup := session.CurrentGroup()
	isInitialStartup := session.LastAckedConfigVersion() == 0
	testhooks.Point("control.config_ack.before_accept",
		testhooks.F("group_id", currentGroup.ID),
		testhooks.F("session_id", session.ID),
		testhooks.F("request_id", frame.RequestID),
		testhooks.F("config_version", ack.ConfigVersion))

	appliedConfig, err := session.acceptConfigAck(frame.RequestID, ack.ConfigVersion)
	if err != nil {
		return s.handleConfigAckAcceptError(conn, session, frame, ack, err)
	}

	if isInitialStartup {
		err := controlconfigsync.ValidateInitialRuntime(controlconfigsync.InitialRuntimeValidationOptions{
			Group:                 appliedConfig.Group,
			Snapshot:              appliedConfig.Snapshot,
			ResolveEffectiveIP:    s.resolveGroupEffectiveIP,
			RecordRuntimeIssue:    s.recordTunnelRuntimeIssueForConfig,
			RuntimeReason:         controlruntime.BuildGroupEffectiveIPRuntimeReason,
			StartupRejectedReason: controlruntime.BuildInitialStartupRejectedReason,
		})
		if err != nil {
			return s.replyProtocolErrorWithSession(conn, session, frame, err)
		}
	}

	testhooks.Point("control.config_ack.after_accept",
		testhooks.F("group_id", appliedConfig.Group.ID),
		testhooks.F("session_id", session.ID),
		testhooks.F("request_id", frame.RequestID),
		testhooks.F("config_version", ack.ConfigVersion))

	logger.Info("config acknowledged", "config_version", ack.ConfigVersion, "applied_at_ms", ack.AppliedAtMs)
	if agent == nil || !agent.Enqueue(controlsession.ConfigAckReceived{RequestID: frame.RequestID, ConfigVersion: ack.ConfigVersion}) {
		return net.ErrClosed
	}
	return nil
}

func (s *Server) handleConfigAckAcceptError(conn net.Conn, session *sessionState, frame protocol.Frame, ack protocol.ConfigAck, err error) error {
	_, expectedVersion := session.ConfigAckState()
	if mapped, ok := controlconfigsync.MapAcceptError(err, frame.RequestID, ack, expectedVersion); ok {
		return s.replyProtocolErrorWithSession(conn, session, frame, mapped)
	}
	return err
}

func (s *Server) pushConfig(conn net.Conn, session *sessionState) error {
	group, snapshot := session.CurrentGroupAndSnapshot()
	return s.pushReloadConfig(conn, session, group, snapshot)
}

func (s *Server) pushReloadConfig(conn net.Conn, session *sessionState, group GroupRuntime, snapshot ConfigSnapshot) error {
	body, err := controlconfigsync.BuildPushBody(snapshot)
	if err != nil {
		return err
	}
	pushOp, err := session.prepareConfigPush(group, snapshot)
	if err != nil {
		return err
	}
	frame := controlconfigsync.NewPushFrame(pushOp.RequestID, body)
	if err := s.writeFrameWithSession(conn, session, frame); err != nil {
		return err
	}
	return nil
}

func (s *Server) applyAcceptedConfig(conn net.Conn, logger Logger, session *sessionState, _ sessionConfigApplyResult) error {
	session.allowTunnelRuntimeStart()
	return s.ensureTunnelListeners(conn, logger, session)
}
