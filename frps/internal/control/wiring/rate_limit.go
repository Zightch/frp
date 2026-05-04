package wiring

import (
	"context"
	"fmt"
	"net"
	"sync"
	"time"

	"github.com/zightch/frp/frps/pkg/protocol"
	"github.com/zightch/frp/frps/pkg/ratepolicy"
)

type sessionRateLimitState struct {
	mu          sync.Mutex
	ctx         context.Context
	cancel      context.CancelFunc
	independent *ratepolicy.LimiterRegistry
	shared      *sharedRateLimitStore
	entries     map[uint32]rateLimitEntry
}

type rateLimitEntry struct {
	ctx      context.Context
	cancel   context.CancelFunc
	limiters ratepolicy.TunnelLimiters
	release  func()
}

func newSessionRateLimitState() *sessionRateLimitState {
	state := &sessionRateLimitState{
		entries: make(map[uint32]rateLimitEntry),
		shared:  newSharedRateLimitStore(),
	}
	state.resetRuntimeLocked()
	return state
}

func (s *sessionState) AddPublicStream(streamID uint32, stream *publicStream, configVersion uint64) bool {
	if s == nil || s.ConcreteSessionState == nil {
		return false
	}
	if !s.ConcreteSessionState.AddPublicStream(streamID, stream, configVersion) {
		return false
	}
	if err := s.registerRateLimit(streamID, stream.Tunnel); err != nil {
		s.ConcreteSessionState.ClosePublicStream(streamID)
		return false
	}
	return true
}

func (s *sessionState) BindPublicUDPSession(udpSession *publicUDPSession, configVersion uint64) (*publicUDPSession, bool) {
	if s == nil || s.ConcreteSessionState == nil {
		return nil, false
	}

	bound, created := s.ConcreteSessionState.BindPublicUDPSession(udpSession, configVersion)
	if bound == nil || !created {
		return bound, created
	}
	if err := s.registerRateLimit(bound.SessionID, bound.Tunnel); err != nil {
		s.ConcreteSessionState.ClosePublicUDPSession(bound.SessionID)
		return nil, false
	}
	return bound, true
}

func (s *sessionState) ClosePublicStream(streamID uint32) bool {
	if s == nil || s.ConcreteSessionState == nil {
		return false
	}
	s.cancelRateLimit(streamID)
	return s.ConcreteSessionState.ClosePublicStream(streamID)
}

func (s *sessionState) ClosePublicUDPSession(sessionID uint32) bool {
	if s == nil || s.ConcreteSessionState == nil {
		return false
	}
	s.cancelRateLimit(sessionID)
	return s.ConcreteSessionState.ClosePublicUDPSession(sessionID)
}

func (s *sessionState) FreezeTunnelRuntime() ([]net.Listener, []UDPListener, map[uint32]*publicStream, []*publicUDPSession) {
	if s == nil || s.ConcreteSessionState == nil {
		return nil, nil, nil, nil
	}
	listeners, udpListeners, streams, udpSessions := s.ConcreteSessionState.FreezeTunnelRuntime()
	s.CloseBusyTCPWorkConns()
	s.resetRateLimitRuntime()
	return listeners, udpListeners, streams, udpSessions
}

func (s *sessionState) TakeIdlePublicUDPSessions(now time.Time) []*publicUDPSession {
	if s == nil || s.ConcreteSessionState == nil {
		return nil
	}

	sessions := s.ConcreteSessionState.TakeIdlePublicUDPSessions(now)
	for _, session := range sessions {
		s.cancelRateLimit(session.SessionID)
	}
	return sessions
}

func (s *sessionState) StreamRateLimit(streamID uint32) (context.Context, ratepolicy.TunnelLimiters, bool) {
	return s.lookupRateLimit(streamID)
}

func (s *sessionState) UDPSessionRateLimit(sessionID uint32) (context.Context, ratepolicy.TunnelLimiters, bool) {
	return s.lookupRateLimit(sessionID)
}

func (s *sessionState) lookupRateLimit(id uint32) (context.Context, ratepolicy.TunnelLimiters, bool) {
	if s == nil || s.rateLimit == nil {
		return nil, ratepolicy.TunnelLimiters{}, false
	}

	s.rateLimit.mu.Lock()
	defer s.rateLimit.mu.Unlock()

	entry, ok := s.rateLimit.entries[id]
	if !ok {
		return nil, ratepolicy.TunnelLimiters{}, false
	}
	return entry.ctx, entry.limiters, true
}

func (s *sessionState) registerRateLimit(id uint32, tunnel protocol.TunnelEntry) error {
	if s == nil || s.rateLimit == nil {
		return fmt.Errorf("session rate limit state is nil")
	}

	s.rateLimit.mu.Lock()
	defer s.rateLimit.mu.Unlock()

	limiters, release, err := buildTunnelLimitersLocked(s.rateLimit, tunnel)
	if err != nil {
		return err
	}

	entryCtx, entryCancel := context.WithCancel(s.rateLimit.ctx)
	s.rateLimit.entries[id] = rateLimitEntry{
		ctx:      entryCtx,
		cancel:   entryCancel,
		limiters: limiters,
		release:  release,
	}
	return nil
}

func (s *sessionState) cancelRateLimit(id uint32) {
	if s == nil || s.rateLimit == nil {
		return
	}

	s.rateLimit.mu.Lock()
	entry, ok := s.rateLimit.entries[id]
	if ok {
		delete(s.rateLimit.entries, id)
	}
	s.rateLimit.mu.Unlock()

	if ok && entry.cancel != nil {
		entry.cancel()
	}
	if ok && entry.release != nil {
		entry.release()
	}
}

func (s *sessionState) resetRateLimitRuntime() {
	if s == nil || s.rateLimit == nil {
		return
	}

	s.rateLimit.mu.Lock()
	for id, entry := range s.rateLimit.entries {
		delete(s.rateLimit.entries, id)
		if entry.cancel != nil {
			entry.cancel()
		}
		if entry.release != nil {
			entry.release()
		}
	}
	s.rateLimit.resetRuntimeLocked()
	s.rateLimit.mu.Unlock()
}

func (s *sessionRateLimitState) resetRuntimeLocked() {
	if s.cancel != nil {
		s.cancel()
	}
	s.ctx, s.cancel = context.WithCancel(context.Background())
	s.independent = ratepolicy.NewLimiterRegistry()
}

func buildTunnelLimitersLocked(state *sessionRateLimitState, tunnel protocol.TunnelEntry) (ratepolicy.TunnelLimiters, func(), error) {
	if state == nil {
		return ratepolicy.TunnelLimiters{}, nil, fmt.Errorf("session rate limit state is nil")
	}
	if tunnel.RatePolicy.PolicyID == 0 {
		return ratepolicy.TunnelLimiters{}, nil, nil
	}

	spec, err := rateLimitSpecForTunnel(tunnel)
	if err != nil {
		return ratepolicy.TunnelLimiters{}, nil, err
	}
	switch spec.Mode {
	case ratepolicy.ModeIndependent:
		if state.independent == nil {
			state.independent = ratepolicy.NewLimiterRegistry()
		}
		limiters, err := state.independent.Limiters(spec)
		return limiters, nil, err
	case ratepolicy.ModeShared:
		if state.shared == nil {
			state.shared = newSharedRateLimitStore()
		}
		return state.shared.Acquire(spec)
	default:
		return ratepolicy.TunnelLimiters{}, nil, fmt.Errorf("unsupported rate policy mode %q", spec.Mode)
	}
}

func rateLimitSpecForTunnel(tunnel protocol.TunnelEntry) (ratepolicy.LimiterSpec, error) {
	mode, err := rateLimitModeFromWire(tunnel.RatePolicy.Mode)
	if err != nil {
		return ratepolicy.LimiterSpec{}, err
	}
	return ratepolicy.NewLimiterSpec(
		mode,
		tunnel.RatePolicy.PolicyID,
		tunnel.TunnelID,
		tunnel.RatePolicy.DownlinkBPS,
		tunnel.RatePolicy.UplinkBPS,
	)
}

func rateLimitModeFromWire(mode uint8) (ratepolicy.Mode, error) {
	switch mode {
	case protocol.RatePolicyModeIndependent:
		return ratepolicy.ModeIndependent, nil
	case protocol.RatePolicyModeShared:
		return ratepolicy.ModeShared, nil
	default:
		return "", fmt.Errorf("unsupported rate policy mode %d", mode)
	}
}
