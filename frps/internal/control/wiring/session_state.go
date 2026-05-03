package wiring

import (
	"sync"
	"time"

	controlruntime "github.com/zightch/frp/frps/internal/control/runtime"
)

// sessionState is the root assembly wrapper around concrete session runtime state.
type sessionState struct {
	*controlruntime.ConcreteSessionState
	rateLimit *sessionRateLimitState

	tcpWorkMu       sync.RWMutex
	tcpWorkPoolSize uint16
	tcpWorkSecret   [32]byte
}

func newSessionState(id uint64, group GroupRuntime, snapshot ConfigSnapshot, readTimeout time.Duration) *sessionState {
	return &sessionState{
		ConcreteSessionState: controlruntime.NewConcreteSessionState(id, group, snapshot, readTimeout),
		rateLimit:            newSessionRateLimitState(),
	}
}

func (s *sessionState) SetSharedRateLimitStore(store *sharedRateLimitStore) {
	if s == nil || s.rateLimit == nil || store == nil {
		return
	}

	s.rateLimit.mu.Lock()
	s.rateLimit.shared = store
	s.rateLimit.mu.Unlock()
}

func (s *sessionState) SetTCPWorkConfig(poolSize uint16, secret [32]byte) {
	if s == nil {
		return
	}
	s.tcpWorkMu.Lock()
	s.tcpWorkPoolSize = poolSize
	s.tcpWorkSecret = secret
	s.tcpWorkMu.Unlock()
}

func (s *sessionState) TCPWorkConfig() (uint16, [32]byte) {
	if s == nil {
		return 0, [32]byte{}
	}
	s.tcpWorkMu.RLock()
	defer s.tcpWorkMu.RUnlock()
	return s.tcpWorkPoolSize, s.tcpWorkSecret
}

func (s *sessionState) MatchTCPWorkSecret(secret [32]byte) bool {
	if s == nil {
		return false
	}
	s.tcpWorkMu.RLock()
	defer s.tcpWorkMu.RUnlock()
	return s.tcpWorkSecret == secret
}
