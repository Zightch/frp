package control

import (
	"crypto/rand"
	"crypto/subtle"
	"errors"
	"net"
	"time"

	"github.com/zightch/frp/frps/pkg/protocol"
)

type authChallenge struct {
	TokenHash [32]byte
	Nonce     [16]byte
	ExpiresAt time.Time
	Used      bool
}

func (s *Server) authenticate(conn net.Conn) (*sessionState, error) {
	frame, err := s.readFrame(conn)
	if err != nil {
		return nil, s.replyProtocolError(conn, frame, err)
	}
	if frame.Type != protocol.TypeAuthBegin {
		return nil, s.replyError(
			conn,
			frame.RequestID,
			frame.StreamID,
			protocol.ErrorCodeProtocolBadBody,
			"expected auth.begin, got %s",
			frame.Type.String(),
		)
	}
	if frame.RequestID == 0 {
		return nil, s.replyError(conn, 0, frame.StreamID, protocol.ErrorCodeProtocolBadBody, "auth.begin requestId must be non-zero")
	}
	if frame.StreamID != 0 {
		return nil, s.replyError(conn, frame.RequestID, frame.StreamID, protocol.ErrorCodeProtocolBadBody, "auth.begin streamId must be zero")
	}

	begin, err := protocol.UnmarshalAuthBegin(frame.Body)
	if err != nil {
		return nil, s.replyProtocolError(conn, frame, err)
	}

	group, err := s.loadGroupRuntime(begin.TokenID)
	if err != nil {
		switch {
		case errors.Is(err, ErrGroupNotFound):
			return nil, s.replyError(conn, frame.RequestID, 0, protocol.ErrorCodeAuthInvalidToken, "token id not found")
		default:
			return nil, err
		}
	}
	if !group.Enabled {
		return nil, s.replyError(conn, frame.RequestID, 0, protocol.ErrorCodeAuthGroupDisabled, "proxy group is disabled")
	}

	challenge, err := s.issueChallenge(group.TokenHash)
	if err != nil {
		return nil, err
	}
	challengeBody, err := protocol.MarshalAuthChallenge(challenge)
	if err != nil {
		return nil, err
	}
	if err := s.writeFrame(conn, protocol.Frame{
		Type:      protocol.TypeAuthChallenge,
		RequestID: frame.RequestID,
		Body:      challengeBody,
	}); err != nil {
		return nil, err
	}

	frame, err = s.readFrame(conn)
	if err != nil {
		return nil, s.replyProtocolError(conn, frame, err)
	}
	if frame.Type != protocol.TypeAuthFinish {
		return nil, s.replyError(
			conn,
			frame.RequestID,
			frame.StreamID,
			protocol.ErrorCodeProtocolBadBody,
			"expected auth.finish, got %s",
			frame.Type.String(),
		)
	}
	if frame.RequestID == 0 {
		return nil, s.replyError(conn, 0, frame.StreamID, protocol.ErrorCodeProtocolBadBody, "auth.finish requestId must be non-zero")
	}
	if frame.StreamID != 0 {
		return nil, s.replyError(conn, frame.RequestID, frame.StreamID, protocol.ErrorCodeProtocolBadBody, "auth.finish streamId must be zero")
	}

	finish, err := protocol.UnmarshalAuthFinish(frame.Body)
	if err != nil {
		return nil, s.replyProtocolError(conn, frame, err)
	}
	if err := s.consumeChallenge(finish.ChallengeID, finish.Response); err != nil {
		return nil, s.replyProtocolError(conn, frame, err)
	}

	session := &sessionState{
		ID:             s.nextSessionID.Add(1),
		Group:          group,
		Snapshot:       group.Snapshot,
		readTimeout:    sessionReadTimeout(s.options.HeartbeatInterval, s.options.ReadTimeout),
		streams:        make(map[uint32]*publicStream),
		udpSessions:    make(map[uint32]*publicUDPSession),
		udpSessionKeys: make(map[string]uint32),
		listeners:      make(map[uint32][]net.Listener),
		udpListeners:   make(map[uint32][]*net.UDPConn),
		done:           make(chan struct{}),
	}
	session.nextServerRequestID.Store(initialServerRequestID - 1)

	if !s.reserveGroupSlot(session.Group.ID, session.ID) {
		return nil, s.replyError(conn, frame.RequestID, 0, protocol.ErrorCodeAuthClientLimitReached, "proxy group already has an active client")
	}

	helloBody, err := protocol.MarshalServerHello(protocol.ServerHello{
		HeartbeatIntervalMs: uint32(s.options.HeartbeatInterval / time.Millisecond),
		SessionID:           session.ID,
		CapabilityBits:      0,
		ServerVersion:       s.version,
	})
	if err != nil {
		s.releaseGroupSlot(session.Group.ID, session.ID)
		return nil, err
	}
	if err := s.writeFrame(conn, protocol.Frame{
		Type:      protocol.TypeServerHello,
		RequestID: frame.RequestID,
		Body:      helloBody,
	}); err != nil {
		s.releaseGroupSlot(session.Group.ID, session.ID)
		return nil, err
	}

	if err := s.pushConfig(conn, session); err != nil {
		s.releaseGroupSlot(session.Group.ID, session.ID)
		return nil, err
	}

	return session, nil
}

func (s *Server) issueChallenge(tokenHash [32]byte) (protocol.AuthChallenge, error) {
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return protocol.AuthChallenge{}, err
	}

	challengeID := s.nextChallengeID.Add(1)
	if challengeID == 0 {
		challengeID = s.nextChallengeID.Add(1)
	}

	now := time.Now().UTC()
	s.challengeMu.Lock()
	defer s.challengeMu.Unlock()

	s.purgeExpiredChallengesLocked(now)
	s.challenges[challengeID] = &authChallenge{
		TokenHash: tokenHash,
		Nonce:     nonce,
		ExpiresAt: now.Add(s.options.ChallengeTTL),
	}

	return protocol.AuthChallenge{
		ChallengeID: challengeID,
		Nonce:       nonce,
		ExpiresInMs: uint32(s.options.ChallengeTTL / time.Millisecond),
	}, nil
}

func (s *Server) consumeChallenge(challengeID uint32, response [32]byte) error {
	now := time.Now().UTC()

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
	expected := protocol.ChallengeResponse(challenge.TokenHash, challenge.Nonce)
	if subtle.ConstantTimeCompare(expected[:], response[:]) != 1 {
		return protocol.NewError(protocol.ErrorCodeAuthInvalidToken, "challenge response mismatch")
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

func (s *Server) reserveGroupSlot(groupID int64, sessionID uint64) bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, ok := s.groupSlots[groupID]; ok {
		return false
	}
	s.groupSlots[groupID] = sessionID
	return true
}

func (s *Server) releaseGroupSlot(groupID int64, sessionID uint64) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.groupSlots[groupID] == sessionID {
		delete(s.groupSlots, groupID)
	}
}
