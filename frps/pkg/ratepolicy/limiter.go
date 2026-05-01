package ratepolicy

import (
	"context"
	"fmt"
	"math"
	"sync"
	"time"
)

type Direction string

const (
	DirectionDownlink Direction = "downlink"
	DirectionUplink   Direction = "uplink"
)

type BucketConfig struct {
	RateBPS    uint64
	BurstBytes int
}

type TokenBucket struct {
	mu         sync.Mutex
	config     BucketConfig
	available  float64
	lastRefill time.Time
	waiters    []*bucketWaiter
	now        func() time.Time
	newTimer   func(time.Duration) *time.Timer
}

type bucketWaiter struct {
	bytes   int
	readyAt time.Time
	notify  chan struct{}
}

type LimiterSpec struct {
	Mode     Mode
	PolicyID uint32
	TunnelID uint32
	Downlink BucketConfig
	Uplink   BucketConfig
}

type TunnelLimiters struct {
	Downlink *TokenBucket
	Uplink   *TokenBucket
}

type LimiterRegistry struct {
	mu      sync.Mutex
	buckets map[limiterKey]*TokenBucket
}

type limiterKey struct {
	scopeKind uint8
	scopeID   uint32
	direction Direction
}

const (
	scopeKindIndependent uint8 = 1
	scopeKindShared      uint8 = 2
)

func NewBucketConfig(rateBPS uint64) (BucketConfig, error) {
	if rateBPS == 0 {
		return BucketConfig{}, fmt.Errorf("bucket rate bps must be greater than 0")
	}

	burstFloat := math.Ceil(float64(rateBPS) / 8.0)
	if burstFloat > float64(maxIntValue()) {
		return BucketConfig{}, fmt.Errorf("bucket burst bytes overflow")
	}

	burstBytes := int(burstFloat)
	if burstBytes < 1 {
		burstBytes = 1
	}

	return BucketConfig{
		RateBPS:    rateBPS,
		BurstBytes: burstBytes,
	}, nil
}

func (c BucketConfig) Validate() error {
	if c.RateBPS == 0 {
		return fmt.Errorf("bucket rate bps must be greater than 0")
	}
	if c.BurstBytes <= 0 {
		return fmt.Errorf("bucket burst bytes must be greater than 0")
	}
	return nil
}

func NewTokenBucket(config BucketConfig) (*TokenBucket, error) {
	return newTokenBucketWithClock(config, time.Now, time.NewTimer)
}

func newTokenBucketWithClock(config BucketConfig, now func() time.Time, newTimer func(time.Duration) *time.Timer) (*TokenBucket, error) {
	if err := config.Validate(); err != nil {
		return nil, err
	}
	if now == nil {
		return nil, fmt.Errorf("bucket clock is nil")
	}
	if newTimer == nil {
		return nil, fmt.Errorf("bucket timer factory is nil")
	}

	current := now().UTC()
	return &TokenBucket{
		config:     config,
		available:  float64(config.BurstBytes),
		lastRefill: current,
		now:        now,
		newTimer:   newTimer,
	}, nil
}

func (b *TokenBucket) Config() BucketConfig {
	if b == nil {
		return BucketConfig{}
	}

	b.mu.Lock()
	defer b.mu.Unlock()
	return b.config
}

func (b *TokenBucket) WaitN(ctx context.Context, bytes int) error {
	if b == nil {
		return fmt.Errorf("token bucket is nil")
	}
	if bytes < 0 {
		return fmt.Errorf("wait bytes must be greater than or equal to 0")
	}
	if bytes == 0 {
		return nil
	}
	if ctx == nil {
		return fmt.Errorf("wait context is nil")
	}

	waiter, wait, err := b.reserve(bytes)
	if err != nil {
		return err
	}
	if waiter == nil || wait <= 0 {
		return nil
	}

	for {
		timer := b.newTimer(wait)
		select {
		case <-ctx.Done():
			stopTimer(timer)
			b.cancelWaiter(waiter)
			return ctx.Err()
		case <-waiter.notify:
			stopTimer(timer)
		case <-timer.C:
			b.finishWaiter(waiter)
			return nil
		}

		wait = time.Until(waiter.readyAt)
		if wait <= 0 {
			b.finishWaiter(waiter)
			return nil
		}
	}
}

func (b *TokenBucket) reserve(bytes int) (*bucketWaiter, time.Duration, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	now := b.now().UTC()
	b.refillLocked(now)
	b.available -= float64(bytes)
	if b.available >= 0 {
		return nil, 0, nil
	}

	wait := b.durationForDeficitLocked(-b.available)
	waiter := &bucketWaiter{
		bytes:   bytes,
		readyAt: now.Add(wait),
		notify:  make(chan struct{}, 1),
	}
	b.waiters = append(b.waiters, waiter)
	return waiter, wait, nil
}

func (b *TokenBucket) cancelWaiter(waiter *bucketWaiter) {
	b.mu.Lock()
	defer b.mu.Unlock()

	index := b.indexOfWaiterLocked(waiter)
	if index < 0 {
		return
	}

	now := b.now().UTC()
	b.refillLocked(now)
	b.available += float64(waiter.bytes)
	if b.available > float64(b.config.BurstBytes) {
		b.available = float64(b.config.BurstBytes)
	}

	b.waiters = append(b.waiters[:index], b.waiters[index+1:]...)
	b.recalculateWaitersLocked(index, now)
}

func (b *TokenBucket) finishWaiter(waiter *bucketWaiter) {
	b.mu.Lock()
	defer b.mu.Unlock()

	index := b.indexOfWaiterLocked(waiter)
	if index < 0 {
		return
	}
	b.waiters = append(b.waiters[:index], b.waiters[index+1:]...)
}

func (b *TokenBucket) indexOfWaiterLocked(waiter *bucketWaiter) int {
	for index, current := range b.waiters {
		if current == waiter {
			return index
		}
	}
	return -1
}

func (b *TokenBucket) recalculateWaitersLocked(start int, now time.Time) {
	if start < 0 || start >= len(b.waiters) {
		return
	}

	runningAvailable := b.available
	for _, waiter := range b.waiters[start:] {
		runningAvailable += float64(waiter.bytes)
	}

	for _, waiter := range b.waiters[start:] {
		runningAvailable -= float64(waiter.bytes)
		readyAt := now
		if runningAvailable < 0 {
			readyAt = now.Add(b.durationForDeficitLocked(-runningAvailable))
		}
		if !readyAt.Equal(waiter.readyAt) {
			waiter.readyAt = readyAt
			notifyWaiter(waiter)
		}
	}
}

func (b *TokenBucket) refillLocked(now time.Time) {
	if !now.After(b.lastRefill) {
		return
	}

	refillBytes := now.Sub(b.lastRefill).Seconds() * b.bytesPerSecond()
	if refillBytes <= 0 {
		b.lastRefill = now
		return
	}

	b.available += refillBytes
	if b.available > float64(b.config.BurstBytes) {
		b.available = float64(b.config.BurstBytes)
	}
	b.lastRefill = now
}

func (b *TokenBucket) bytesPerSecond() float64 {
	return float64(b.config.RateBPS) / 8.0
}

func (b *TokenBucket) durationForDeficitLocked(deficit float64) time.Duration {
	if deficit <= 0 {
		return 0
	}

	bytesPerSecond := b.bytesPerSecond()
	if bytesPerSecond <= 0 {
		return 0
	}

	nanoseconds := math.Ceil(deficit * float64(time.Second) / bytesPerSecond)
	if nanoseconds < 1 {
		nanoseconds = 1
	}
	if nanoseconds > float64(math.MaxInt64) {
		return time.Duration(math.MaxInt64)
	}
	return time.Duration(nanoseconds)
}

func NewLimiterRegistry() *LimiterRegistry {
	return &LimiterRegistry{
		buckets: make(map[limiterKey]*TokenBucket),
	}
}

func (r *LimiterRegistry) Limiters(spec LimiterSpec) (TunnelLimiters, error) {
	if r == nil {
		return TunnelLimiters{}, fmt.Errorf("limiter registry is nil")
	}
	if err := validateLimiterSpec(spec); err != nil {
		return TunnelLimiters{}, err
	}

	downlink, err := r.getOrCreate(spec, DirectionDownlink, spec.Downlink)
	if err != nil {
		return TunnelLimiters{}, err
	}
	uplink, err := r.getOrCreate(spec, DirectionUplink, spec.Uplink)
	if err != nil {
		return TunnelLimiters{}, err
	}

	return TunnelLimiters{
		Downlink: downlink,
		Uplink:   uplink,
	}, nil
}

func (r *LimiterRegistry) getOrCreate(spec LimiterSpec, direction Direction, config BucketConfig) (*TokenBucket, error) {
	key, err := limiterKeyFor(spec, direction)
	if err != nil {
		return nil, err
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	if bucket, ok := r.buckets[key]; ok {
		if bucket.Config() != config {
			return nil, fmt.Errorf("limiter config mismatch for %s scope %d %s", spec.Mode, key.scopeID, direction)
		}
		return bucket, nil
	}

	bucket, err := NewTokenBucket(config)
	if err != nil {
		return nil, err
	}
	r.buckets[key] = bucket
	return bucket, nil
}

func validateLimiterSpec(spec LimiterSpec) error {
	switch spec.Mode {
	case ModeIndependent:
		if spec.TunnelID == 0 {
			return fmt.Errorf("independent limiter requires tunnel id")
		}
	case ModeShared:
		if spec.PolicyID == 0 {
			return fmt.Errorf("shared limiter requires policy id")
		}
	default:
		return fmt.Errorf("limiter mode must be independent or shared")
	}

	if err := spec.Downlink.Validate(); err != nil {
		return fmt.Errorf("downlink bucket: %w", err)
	}
	if err := spec.Uplink.Validate(); err != nil {
		return fmt.Errorf("uplink bucket: %w", err)
	}
	return nil
}

func limiterKeyFor(spec LimiterSpec, direction Direction) (limiterKey, error) {
	switch direction {
	case DirectionDownlink, DirectionUplink:
	default:
		return limiterKey{}, fmt.Errorf("unsupported limiter direction %q", direction)
	}

	switch spec.Mode {
	case ModeIndependent:
		return limiterKey{
			scopeKind: scopeKindIndependent,
			scopeID:   spec.TunnelID,
			direction: direction,
		}, nil
	case ModeShared:
		return limiterKey{
			scopeKind: scopeKindShared,
			scopeID:   spec.PolicyID,
			direction: direction,
		}, nil
	default:
		return limiterKey{}, fmt.Errorf("limiter mode must be independent or shared")
	}
}

func notifyWaiter(waiter *bucketWaiter) {
	select {
	case waiter.notify <- struct{}{}:
	default:
	}
}

func stopTimer(timer *time.Timer) {
	if timer == nil {
		return
	}
	if !timer.Stop() {
		select {
		case <-timer.C:
		default:
		}
	}
}

func maxIntValue() int {
	return int(^uint(0) >> 1)
}
