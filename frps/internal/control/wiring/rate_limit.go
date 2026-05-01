package wiring

import (
	"context"
	"fmt"
	"net"
	"sync"

	"github.com/zightch/frp/frps/pkg/protocol"
	"github.com/zightch/frp/frps/pkg/ratepolicy"
)

type sessionRateLimitState struct {
	mu       sync.Mutex
	ctx      context.Context
	cancel   context.CancelFunc
	registry *ratepolicy.LimiterRegistry
	streams  map[uint32]streamRateLimitEntry
}

type streamRateLimitEntry struct {
	ctx      context.Context
	cancel   context.CancelFunc
	limiters ratepolicy.TunnelLimiters
}

func newSessionRateLimitState() *sessionRateLimitState {
	state := &sessionRateLimitState{
		streams: make(map[uint32]streamRateLimitEntry),
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
	if err := s.registerStreamRateLimit(streamID, stream); err != nil {
		s.ConcreteSessionState.ClosePublicStream(streamID)
		return false
	}
	return true
}

func (s *sessionState) ClosePublicStream(streamID uint32) bool {
	if s == nil || s.ConcreteSessionState == nil {
		return false
	}
	s.cancelStreamRateLimit(streamID)
	return s.ConcreteSessionState.ClosePublicStream(streamID)
}

func (s *sessionState) FreezeTunnelRuntime() ([]net.Listener, []UDPListener, map[uint32]*publicStream, []*publicUDPSession) {
	if s == nil || s.ConcreteSessionState == nil {
		return nil, nil, nil, nil
	}
	listeners, udpListeners, streams, udpSessions := s.ConcreteSessionState.FreezeTunnelRuntime()
	s.resetRateLimitRuntime()
	return listeners, udpListeners, streams, udpSessions
}

func (s *sessionState) StreamRateLimit(streamID uint32) (context.Context, ratepolicy.TunnelLimiters, bool) {
	if s == nil || s.rateLimit == nil {
		return nil, ratepolicy.TunnelLimiters{}, false
	}

	s.rateLimit.mu.Lock()
	defer s.rateLimit.mu.Unlock()

	entry, ok := s.rateLimit.streams[streamID]
	if !ok {
		return nil, ratepolicy.TunnelLimiters{}, false
	}
	return entry.ctx, entry.limiters, true
}

func (s *sessionState) registerStreamRateLimit(streamID uint32, stream *publicStream) error {
	if s == nil || s.rateLimit == nil {
		return fmt.Errorf("session rate limit state is nil")
	}
	if stream == nil {
		return fmt.Errorf("public stream is nil")
	}

	s.rateLimit.mu.Lock()
	defer s.rateLimit.mu.Unlock()

	limiters, err := buildTunnelLimitersLocked(s.rateLimit, stream.Tunnel)
	if err != nil {
		return err
	}

	streamCtx, streamCancel := context.WithCancel(s.rateLimit.ctx)
	s.rateLimit.streams[streamID] = streamRateLimitEntry{
		ctx:      streamCtx,
		cancel:   streamCancel,
		limiters: limiters,
	}
	return nil
}

func (s *sessionState) cancelStreamRateLimit(streamID uint32) {
	if s == nil || s.rateLimit == nil {
		return
	}

	s.rateLimit.mu.Lock()
	entry, ok := s.rateLimit.streams[streamID]
	if ok {
		delete(s.rateLimit.streams, streamID)
	}
	s.rateLimit.mu.Unlock()

	if ok && entry.cancel != nil {
		entry.cancel()
	}
}

func (s *sessionState) resetRateLimitRuntime() {
	if s == nil || s.rateLimit == nil {
		return
	}

	s.rateLimit.mu.Lock()
	for streamID, entry := range s.rateLimit.streams {
		delete(s.rateLimit.streams, streamID)
		if entry.cancel != nil {
			entry.cancel()
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
	s.registry = ratepolicy.NewLimiterRegistry()
}

func buildTunnelLimitersLocked(state *sessionRateLimitState, tunnel protocol.TunnelEntry) (ratepolicy.TunnelLimiters, error) {
	if state == nil {
		return ratepolicy.TunnelLimiters{}, fmt.Errorf("session rate limit state is nil")
	}
	if state.registry == nil {
		state.registry = ratepolicy.NewLimiterRegistry()
	}
	if tunnel.RatePolicy.PolicyID == 0 {
		return ratepolicy.TunnelLimiters{}, nil
	}

	spec, err := rateLimitSpecForTunnel(tunnel)
	if err != nil {
		return ratepolicy.TunnelLimiters{}, err
	}
	return state.registry.Limiters(spec)
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
