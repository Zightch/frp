package control

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"time"

	v2session "github.com/zightch/frp/frps/internal/controlv2/session"
	"github.com/zightch/frp/frps/pkg/protocol"
)

type authChallenge struct {
	ClientSecretHash [32]byte
	Nonce            [16]byte
	ExpiresAt        time.Time
	Used             bool
}

func (s *Server) authenticate(conn net.Conn, expectedClientID [16]byte, logger *slog.Logger) (*sessionState, *v2session.Agent, error) {
	frame, err := s.readFrame(conn)
	if err != nil {
		return nil, nil, s.replyProtocolError(conn, frame, err)
	}
	if frame.Type != protocol.TypeAuthBegin {
		return nil, nil, s.replyError(
			conn,
			frame.RequestID,
			frame.StreamID,
			protocol.ErrorCodeProtocolBadBody,
			"expected auth.begin, got %s",
			frame.Type.String(),
		)
	}
	if frame.RequestID == 0 {
		return nil, nil, s.replyError(conn, 0, frame.StreamID, protocol.ErrorCodeProtocolBadBody, "auth.begin requestId must be non-zero")
	}
	if frame.StreamID != 0 {
		return nil, nil, s.replyError(conn, frame.RequestID, frame.StreamID, protocol.ErrorCodeProtocolBadBody, "auth.begin streamId must be zero")
	}

	begin, err := protocol.UnmarshalAuthBegin(frame.Body)
	if err != nil {
		return nil, nil, s.replyProtocolError(conn, frame, err)
	}
	if begin.ClientID != expectedClientID {
		return nil, nil, s.replyError(conn, frame.RequestID, 0, protocol.ErrorCodeAuthInvalidClient, "auth.begin client_id does not match transport.client_hello")
	}

	group, err := s.loadGroupRuntimeByClientID(begin.ClientID)
	if err != nil {
		switch {
		case errors.Is(err, ErrGroupNotFound):
			return nil, nil, s.replyError(conn, frame.RequestID, 0, protocol.ErrorCodeAuthInvalidClient, "client_id not found")
		default:
			return nil, nil, err
		}
	}
	if !group.Enabled {
		return nil, nil, s.replyError(conn, frame.RequestID, 0, protocol.ErrorCodeAuthGroupDisabled, "proxy group is disabled")
	}

	challenge, err := s.issueChallenge(group.ClientSecretHash)
	if err != nil {
		return nil, nil, err
	}
	challengeBody, err := protocol.MarshalAuthChallenge(challenge)
	if err != nil {
		return nil, nil, err
	}
	if err := s.writeFrame(conn, protocol.Frame{
		Type:      protocol.TypeAuthChallenge,
		RequestID: frame.RequestID,
		Body:      challengeBody,
	}); err != nil {
		return nil, nil, err
	}

	frame, err = s.readFrame(conn)
	if err != nil {
		return nil, nil, s.replyProtocolError(conn, frame, err)
	}
	if frame.Type != protocol.TypeAuthFinish {
		return nil, nil, s.replyError(
			conn,
			frame.RequestID,
			frame.StreamID,
			protocol.ErrorCodeProtocolBadBody,
			"expected auth.finish, got %s",
			frame.Type.String(),
		)
	}
	if frame.RequestID == 0 {
		return nil, nil, s.replyError(conn, 0, frame.StreamID, protocol.ErrorCodeProtocolBadBody, "auth.finish requestId must be non-zero")
	}
	if frame.StreamID != 0 {
		return nil, nil, s.replyError(conn, frame.RequestID, frame.StreamID, protocol.ErrorCodeProtocolBadBody, "auth.finish streamId must be zero")
	}

	finish, err := protocol.UnmarshalAuthFinish(frame.Body)
	if err != nil {
		return nil, nil, s.replyProtocolError(conn, frame, err)
	}
	if err := s.consumeChallenge(finish.ChallengeID, finish.Response); err != nil {
		return nil, nil, s.replyProtocolError(conn, frame, err)
	}

	group, err = s.loadGroupRuntimeByClientID(begin.ClientID)
	if err != nil {
		switch {
		case errors.Is(err, ErrGroupNotFound):
			return nil, nil, s.replyError(conn, frame.RequestID, 0, protocol.ErrorCodeAuthInvalidClient, "client_id not found")
		default:
			return nil, nil, err
		}
	}
	if !group.Enabled {
		return nil, nil, s.replyError(conn, frame.RequestID, 0, protocol.ErrorCodeAuthGroupDisabled, "proxy group is disabled")
	}

	session := newSessionState(
		s.nextSessionID.Add(1),
		group,
		runtimeSnapshotForGroup(group),
		sessionReadTimeout(s.options.HeartbeatInterval, s.options.ReadTimeout),
	)
	initial := v2session.NewState(group.ID, session.ID)
	desired := desiredRuntimeFromGroup(group)
	initial.Desired = &desired

	runtime := &controlV2Runtime{
		conn:         conn,
		logger:       logger,
		session:      session,
		desiredGroup: group,
	}
	s.registerControlV2Runtime(session.ID, runtime)

	agent := s.supervisor.AttachSession(context.Background(), initial)
	if agent == nil {
		s.unregisterControlV2Runtime(session.ID)
		return nil, nil, fmt.Errorf("controlv2 supervisor is unavailable")
	}
	if !agent.Enqueue(v2session.SessionAttached{
		ConnID:         conn.RemoteAddr().String(),
		HelloRequestID: frame.RequestID,
	}) {
		s.unregisterControlV2Runtime(session.ID)
		return nil, nil, fmt.Errorf("controlv2 session attach failed")
	}

	return session, agent, nil
}

func (s *Server) issueChallenge(clientSecretHash [32]byte) (protocol.AuthChallenge, error) {
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return protocol.AuthChallenge{}, err
	}

	challengeID := s.nextChallengeID.Add(1)
	if challengeID == 0 {
		challengeID = s.nextChallengeID.Add(1)
	}

	now := s.clock.Now()
	s.challengeMu.Lock()
	defer s.challengeMu.Unlock()

	s.purgeExpiredChallengesLocked(now)
	s.challenges[challengeID] = &authChallenge{
		ClientSecretHash: clientSecretHash,
		Nonce:            nonce,
		ExpiresAt:        now.Add(s.options.ChallengeTTL),
	}

	return protocol.AuthChallenge{
		ChallengeID: challengeID,
		Nonce:       nonce,
		ExpiresInMs: uint32(s.options.ChallengeTTL / time.Millisecond),
	}, nil
}

func (s *Server) consumeChallenge(challengeID uint32, response [32]byte) error {
	now := s.clock.Now()

	s.challengeMu.Lock()
	defer s.challengeMu.Unlock()

	s.purgeExpiredChallengesLocked(now)

	challenge, ok := s.challenges[challengeID]
	if !ok {
		return protocol.NewError(protocol.ErrorCodeAuthChallengeExpired, "challenge %d is missing or expired", challengeID)
	}
	if challenge.Used {
		return protocol.NewError(protocol.ErrorCodeAuthChallengeReplayed, "challenge %d has already been used", challengeID)
	}
	if now.After(challenge.ExpiresAt) {
		challenge.Used = true
		return protocol.NewError(protocol.ErrorCodeAuthChallengeExpired, "challenge %d expired", challengeID)
	}

	challenge.Used = true
	expected := protocol.ChallengeResponse(challenge.ClientSecretHash, challenge.Nonce)
	if subtle.ConstantTimeCompare(expected[:], response[:]) != 1 {
		return protocol.NewError(protocol.ErrorCodeAuthInvalidClient, "challenge response mismatch")
	}

	return nil
}

func (s *Server) purgeExpiredChallengesLocked(now time.Time) {
	for challengeID, challenge := range s.challenges {
		if now.After(challenge.ExpiresAt) {
			delete(s.challenges, challengeID)
		}
	}
}
