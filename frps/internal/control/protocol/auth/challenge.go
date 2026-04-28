package auth

import (
	"crypto/rand"
	"crypto/subtle"
	"fmt"
	"io"
	"sync"
	"sync/atomic"
	"time"

	"github.com/zightch/frp/frps/pkg/protocol"
)

const defaultChallengeTTL = 30 * time.Second

type Clock interface {
	Now() time.Time
}

type ChallengeServiceOptions struct {
	Clock  Clock
	TTL    time.Duration
	Random io.Reader
}

type ChallengeService struct {
	mu              sync.Mutex
	challenges      map[uint32]*challenge
	nextChallengeID atomic.Uint32
	clock           Clock
	ttl             time.Duration
	random          io.Reader
}

type challenge struct {
	clientSecretHash [32]byte
	nonce            [16]byte
	expiresAt        time.Time
	used             bool
}

func NewChallengeService(options ChallengeServiceOptions) *ChallengeService {
	random := options.Random
	if random == nil {
		random = rand.Reader
	}
	ttl := options.TTL
	if ttl <= 0 {
		ttl = defaultChallengeTTL
	}
	return &ChallengeService{
		challenges: make(map[uint32]*challenge),
		clock:      options.Clock,
		ttl:        ttl,
		random:     random,
	}
}

func (s *ChallengeService) Issue(clientSecretHash [32]byte) (protocol.AuthChallenge, error) {
	if s == nil {
		return protocol.AuthChallenge{}, fmt.Errorf("auth challenge service is nil")
	}

	var nonce [16]byte
	if _, err := io.ReadFull(s.random, nonce[:]); err != nil {
		return protocol.AuthChallenge{}, err
	}

	challengeID := s.nextChallengeID.Add(1)
	if challengeID == 0 {
		challengeID = s.nextChallengeID.Add(1)
	}

	now := s.now()
	s.mu.Lock()
	defer s.mu.Unlock()

	s.purgeExpiredLocked(now)
	s.challenges[challengeID] = &challenge{
		clientSecretHash: clientSecretHash,
		nonce:            nonce,
		expiresAt:        now.Add(s.ttl),
	}

	return protocol.AuthChallenge{
		ChallengeID: challengeID,
		Nonce:       nonce,
		ExpiresInMs: uint32(s.ttl / time.Millisecond),
	}, nil
}

func (s *ChallengeService) Consume(challengeID uint32, response [32]byte) error {
	if s == nil {
		return fmt.Errorf("auth challenge service is nil")
	}

	now := s.now()
	s.mu.Lock()
	defer s.mu.Unlock()

	s.purgeExpiredLocked(now)

	challenge, ok := s.challenges[challengeID]
	if !ok {
		return protocol.NewError(protocol.ErrorCodeAuthChallengeExpired, "challenge %d is missing or expired", challengeID)
	}
	if challenge.used {
		return protocol.NewError(protocol.ErrorCodeAuthChallengeReplayed, "challenge %d has already been used", challengeID)
	}
	if now.After(challenge.expiresAt) {
		challenge.used = true
		return protocol.NewError(protocol.ErrorCodeAuthChallengeExpired, "challenge %d expired", challengeID)
	}

	challenge.used = true
	expected := protocol.ChallengeResponse(challenge.clientSecretHash, challenge.nonce)
	if subtle.ConstantTimeCompare(expected[:], response[:]) != 1 {
		return protocol.NewError(protocol.ErrorCodeAuthInvalidClient, "challenge response mismatch")
	}

	return nil
}

func (s *ChallengeService) PurgeExpired(now time.Time) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.purgeExpiredLocked(now)
}

func (s *ChallengeService) now() time.Time {
	if s.clock == nil {
		return time.Now().UTC()
	}
	return s.clock.Now()
}

func (s *ChallengeService) purgeExpiredLocked(now time.Time) {
	for challengeID, challenge := range s.challenges {
		if now.After(challenge.expiresAt) {
			delete(s.challenges, challengeID)
		}
	}
}
