package wiring

import (
	"fmt"
	"sync"

	"github.com/zightch/frp/frps/pkg/ratepolicy"
)

type sharedRateLimitStore struct {
	mu      sync.Mutex
	entries map[sharedRateLimitKey]*sharedRateLimitEntry
}

type sharedRateLimitKey struct {
	policyID uint32
	downlink ratepolicy.BucketConfig
	uplink   ratepolicy.BucketConfig
}

type sharedRateLimitEntry struct {
	refs     int
	limiters ratepolicy.TunnelLimiters
}

func newSharedRateLimitStore() *sharedRateLimitStore {
	return &sharedRateLimitStore{
		entries: make(map[sharedRateLimitKey]*sharedRateLimitEntry),
	}
}

func (s *sharedRateLimitStore) Acquire(spec ratepolicy.LimiterSpec) (ratepolicy.TunnelLimiters, func(), error) {
	if err := validateSharedLimiterSpec(spec); err != nil {
		return ratepolicy.TunnelLimiters{}, nil, err
	}
	key := sharedRateLimitKeyForSpec(spec)

	s.mu.Lock()
	if entry, ok := s.entries[key]; ok {
		entry.refs++
		limiters := entry.limiters
		s.mu.Unlock()
		return limiters, s.releaseFunc(key), nil
	}

	downlink, err := ratepolicy.NewTokenBucket(spec.Downlink)
	if err != nil {
		s.mu.Unlock()
		return ratepolicy.TunnelLimiters{}, nil, err
	}
	uplink, err := ratepolicy.NewTokenBucket(spec.Uplink)
	if err != nil {
		s.mu.Unlock()
		return ratepolicy.TunnelLimiters{}, nil, err
	}

	limiters := ratepolicy.TunnelLimiters{
		Downlink: downlink,
		Uplink:   uplink,
	}
	s.entries[key] = &sharedRateLimitEntry{
		refs:     1,
		limiters: limiters,
	}
	s.mu.Unlock()
	return limiters, s.releaseFunc(key), nil
}

func (s *sharedRateLimitStore) releaseFunc(key sharedRateLimitKey) func() {
	var once sync.Once
	return func() {
		once.Do(func() {
			s.release(key)
		})
	}
}

func (s *sharedRateLimitStore) release(key sharedRateLimitKey) {
	if s == nil {
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	entry, ok := s.entries[key]
	if !ok {
		return
	}
	entry.refs--
	if entry.refs <= 0 {
		delete(s.entries, key)
	}
}

func validateSharedLimiterSpec(spec ratepolicy.LimiterSpec) error {
	if spec.Mode != ratepolicy.ModeShared {
		return fmt.Errorf("shared rate limit store requires shared mode")
	}
	if spec.PolicyID == 0 {
		return fmt.Errorf("shared limiter requires policy id")
	}
	if err := spec.Downlink.Validate(); err != nil {
		return fmt.Errorf("downlink bucket: %w", err)
	}
	if err := spec.Uplink.Validate(); err != nil {
		return fmt.Errorf("uplink bucket: %w", err)
	}
	return nil
}

func sharedRateLimitKeyForSpec(spec ratepolicy.LimiterSpec) sharedRateLimitKey {
	return sharedRateLimitKey{
		policyID: spec.PolicyID,
		downlink: spec.Downlink,
		uplink:   spec.Uplink,
	}
}
