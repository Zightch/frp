package control

import (
	"context"
	"fmt"
	"log/slog"
	"net"

	controlauth "github.com/zightch/frp/frps/internal/control/protocol/auth"
	controlruntime "github.com/zightch/frp/frps/internal/control/runtime"
	controlsession "github.com/zightch/frp/frps/internal/control/session"
	"github.com/zightch/frp/frps/pkg/protocol"
)

func (s *Server) authenticate(conn net.Conn, expectedClientID [16]byte, logger *slog.Logger) (*sessionState, *controlsession.Agent, error) {
	result, err := controlauth.Authenticate(controlauth.AuthenticateOptions{
		Conn:             conn,
		ExpectedClientID: expectedClientID,
		Reader:           controlauth.FrameReaderFunc(s.readFrame),
		Writer:           s.frameWriter(conn, nil),
		Repository:       s.repo,
		Challenges:       s.authChallenges,
		ReadTimeout:      s.options.ReadTimeout,
	})
	if err != nil {
		return nil, nil, err
	}
	group := result.Group

	session := newSessionState(
		s.nextSessionID.Add(1),
		group,
		controlruntime.RuntimeSnapshotForGroup(group),
		controlruntime.SessionReadTimeout(s.options.HeartbeatInterval, s.options.ReadTimeout),
	)
	initial := controlsession.NewState(group.ID, session.ID)
	desired := controlruntime.DesiredRuntimeFromGroup(group)
	initial.Desired = &desired

	runtime := &runtimeExecutor{
		groupID:      group.ID,
		conn:         conn,
		logger:       logger,
		session:      session,
		desiredGroup: group,
	}
	agent := s.supervisor.AttachSession(context.Background(), initial, runtime)
	if agent == nil {
		return nil, nil, fmt.Errorf("control supervisor is unavailable")
	}
	attachEvent := controlsession.SessionAttached{
		ConnID:         conn.RemoteAddr().String(),
		HelloRequestID: result.FinishRequestID,
	}
	session.applyControlEvent(attachEvent)
	session.ControlMu.Lock()
	session.Pending = group
	session.Recovery = controlruntime.PendingRecoveryModeForSnapshot(group.Snapshot)
	session.ControlMu.Unlock()
	if !agent.Enqueue(attachEvent) {
		s.supervisor.DetachRuntime(session.ID)
		return nil, nil, fmt.Errorf("control session attach failed")
	}

	return session, agent, nil
}

func (s *Server) issueChallenge(clientSecretHash [32]byte) (protocol.AuthChallenge, error) {
	return s.authChallenges.Issue(clientSecretHash)
}

func (s *Server) consumeChallenge(challengeID uint32, response [32]byte) error {
	return s.authChallenges.Consume(challengeID, response)
}
