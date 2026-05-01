package ratepolicy

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"
)

func TestNewBucketConfig(t *testing.T) {
	config, err := NewBucketConfig(10_000_000)
	if err != nil {
		t.Fatalf("new bucket config: %v", err)
	}
	if config.RateBPS != 10_000_000 {
		t.Fatalf("unexpected rate bps: %d", config.RateBPS)
	}
	if config.BurstBytes != 1_250_000 {
		t.Fatalf("unexpected burst bytes: %d", config.BurstBytes)
	}

	small, err := NewBucketConfig(1)
	if err != nil {
		t.Fatalf("new small bucket config: %v", err)
	}
	if small.BurstBytes != 1 {
		t.Fatalf("expected minimum burst of 1 byte, got %d", small.BurstBytes)
	}
}

func TestTokenBucketWaitNAllowsLargeRequestToProgress(t *testing.T) {
	config, err := NewBucketConfig(8_000)
	if err != nil {
		t.Fatalf("new bucket config: %v", err)
	}

	bucket, err := NewTokenBucket(config)
	if err != nil {
		t.Fatalf("new token bucket: %v", err)
	}

	start := time.Now()
	if err := bucket.WaitN(context.Background(), 1_500); err != nil {
		t.Fatalf("wait large request: %v", err)
	}
	elapsed := time.Since(start)
	if elapsed < 350*time.Millisecond {
		t.Fatalf("expected large request to wait for refill, got %s", elapsed)
	}
	if elapsed > 2*time.Second {
		t.Fatalf("large request took too long to progress: %s", elapsed)
	}
}

func TestTokenBucketWaitNReturnsOnContextCancel(t *testing.T) {
	config, err := NewBucketConfig(8_000)
	if err != nil {
		t.Fatalf("new bucket config: %v", err)
	}

	bucket, err := NewTokenBucket(config)
	if err != nil {
		t.Fatalf("new token bucket: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	start := time.Now()
	err = bucket.WaitN(ctx, 2_000)
	elapsed := time.Since(start)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected deadline exceeded, got %v", err)
	}
	if elapsed > 300*time.Millisecond {
		t.Fatalf("context cancel returned too late: %s", elapsed)
	}
}

func TestTokenBucketCancelAdvancesLaterWaiter(t *testing.T) {
	config, err := NewBucketConfig(8_000)
	if err != nil {
		t.Fatalf("new bucket config: %v", err)
	}

	bucket, err := NewTokenBucket(config)
	if err != nil {
		t.Fatalf("new token bucket: %v", err)
	}

	ctxA, cancelA := context.WithCancel(context.Background())
	defer cancelA()

	waitA := make(chan error, 1)
	go func() {
		waitA <- bucket.WaitN(ctxA, 1_500)
	}()

	time.Sleep(20 * time.Millisecond)

	waitB := make(chan error, 1)
	startB := time.Now()
	go func() {
		waitB <- bucket.WaitN(context.Background(), 500)
	}()

	time.Sleep(100 * time.Millisecond)
	cancelA()

	select {
	case err := <-waitA:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("expected first waiter to be canceled, got %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("first waiter did not cancel in time")
	}

	select {
	case err := <-waitB:
		if err != nil {
			t.Fatalf("second waiter failed: %v", err)
		}
	case <-time.After(400 * time.Millisecond):
		t.Fatal("second waiter did not advance after cancellation")
	}

	if elapsed := time.Since(startB); elapsed > 350*time.Millisecond {
		t.Fatalf("second waiter advanced too slowly after cancellation: %s", elapsed)
	}
}

func TestLimiterRegistryIndependentAndSharedModes(t *testing.T) {
	downlink, err := NewBucketConfig(8_000)
	if err != nil {
		t.Fatalf("new downlink bucket config: %v", err)
	}
	uplink, err := NewBucketConfig(16_000)
	if err != nil {
		t.Fatalf("new uplink bucket config: %v", err)
	}

	registry := NewLimiterRegistry()

	independentA, err := registry.Limiters(LimiterSpec{
		Mode:     ModeIndependent,
		PolicyID: 7,
		TunnelID: 11,
		Downlink: downlink,
		Uplink:   uplink,
	})
	if err != nil {
		t.Fatalf("independent tunnel a: %v", err)
	}

	independentB, err := registry.Limiters(LimiterSpec{
		Mode:     ModeIndependent,
		PolicyID: 7,
		TunnelID: 12,
		Downlink: downlink,
		Uplink:   uplink,
	})
	if err != nil {
		t.Fatalf("independent tunnel b: %v", err)
	}

	if independentA.Downlink == independentB.Downlink {
		t.Fatal("independent mode must not share downlink bucket across tunnels")
	}
	if independentA.Uplink == independentB.Uplink {
		t.Fatal("independent mode must not share uplink bucket across tunnels")
	}
	if independentA.Downlink == independentA.Uplink {
		t.Fatal("downlink and uplink buckets must stay separate")
	}

	sharedA, err := registry.Limiters(LimiterSpec{
		Mode:     ModeShared,
		PolicyID: 9,
		TunnelID: 21,
		Downlink: downlink,
		Uplink:   uplink,
	})
	if err != nil {
		t.Fatalf("shared tunnel a: %v", err)
	}

	sharedB, err := registry.Limiters(LimiterSpec{
		Mode:     ModeShared,
		PolicyID: 9,
		TunnelID: 22,
		Downlink: downlink,
		Uplink:   uplink,
	})
	if err != nil {
		t.Fatalf("shared tunnel b: %v", err)
	}

	if sharedA.Downlink != sharedB.Downlink {
		t.Fatal("shared mode must reuse downlink bucket across tunnels in one policy")
	}
	if sharedA.Uplink != sharedB.Uplink {
		t.Fatal("shared mode must reuse uplink bucket across tunnels in one policy")
	}
	if sharedA.Downlink == sharedA.Uplink {
		t.Fatal("shared mode must still keep directions separate")
	}
}

func TestLimiterRegistryRejectsConfigMismatch(t *testing.T) {
	downlink, err := NewBucketConfig(8_000)
	if err != nil {
		t.Fatalf("new downlink bucket config: %v", err)
	}
	uplink, err := NewBucketConfig(16_000)
	if err != nil {
		t.Fatalf("new uplink bucket config: %v", err)
	}
	otherDownlink, err := NewBucketConfig(32_000)
	if err != nil {
		t.Fatalf("new other downlink bucket config: %v", err)
	}

	registry := NewLimiterRegistry()
	_, err = registry.Limiters(LimiterSpec{
		Mode:     ModeShared,
		PolicyID: 5,
		TunnelID: 31,
		Downlink: downlink,
		Uplink:   uplink,
	})
	if err != nil {
		t.Fatalf("seed shared limiter: %v", err)
	}

	_, err = registry.Limiters(LimiterSpec{
		Mode:     ModeShared,
		PolicyID: 5,
		TunnelID: 32,
		Downlink: otherDownlink,
		Uplink:   uplink,
	})
	if err == nil {
		t.Fatal("expected shared limiter config mismatch to fail")
	}
}

func TestLimiterRegistryIndependentLimitersDoNotCompeteAcrossTunnels(t *testing.T) {
	config, err := NewBucketConfig(16_000)
	if err != nil {
		t.Fatalf("new bucket config: %v", err)
	}

	registry := NewLimiterRegistry()
	limitersA, err := registry.Limiters(LimiterSpec{
		Mode:     ModeIndependent,
		PolicyID: 7,
		TunnelID: 11,
		Downlink: config,
		Uplink:   config,
	})
	if err != nil {
		t.Fatalf("independent tunnel a: %v", err)
	}
	limitersB, err := registry.Limiters(LimiterSpec{
		Mode:     ModeIndependent,
		PolicyID: 7,
		TunnelID: 12,
		Downlink: config,
		Uplink:   config,
	})
	if err != nil {
		t.Fatalf("independent tunnel b: %v", err)
	}

	waitA := make(chan error, 1)
	go func() {
		waitA <- limitersA.Downlink.WaitN(context.Background(), 3_000)
	}()

	time.Sleep(20 * time.Millisecond)

	waitB := make(chan error, 1)
	startB := time.Now()
	go func() {
		waitB <- limitersB.Downlink.WaitN(context.Background(), 3_000)
	}()

	select {
	case err := <-waitA:
		if err != nil {
			t.Fatalf("independent tunnel a wait failed: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("independent tunnel a wait did not finish in time")
	}

	select {
	case err := <-waitB:
		if err != nil {
			t.Fatalf("independent tunnel b wait failed: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("independent tunnel b wait did not finish in time")
	}

	if elapsed := time.Since(startB); elapsed > 1200*time.Millisecond {
		t.Fatalf("independent tunnel unexpectedly waited like a shared limiter: %s", elapsed)
	}
}

func TestLimiterRegistrySharedLimitersCompeteAcrossTunnels(t *testing.T) {
	config, err := NewBucketConfig(16_000)
	if err != nil {
		t.Fatalf("new bucket config: %v", err)
	}

	registry := NewLimiterRegistry()
	limitersA, err := registry.Limiters(LimiterSpec{
		Mode:     ModeShared,
		PolicyID: 9,
		TunnelID: 21,
		Downlink: config,
		Uplink:   config,
	})
	if err != nil {
		t.Fatalf("shared tunnel a: %v", err)
	}
	limitersB, err := registry.Limiters(LimiterSpec{
		Mode:     ModeShared,
		PolicyID: 9,
		TunnelID: 22,
		Downlink: config,
		Uplink:   config,
	})
	if err != nil {
		t.Fatalf("shared tunnel b: %v", err)
	}

	waitA := make(chan error, 1)
	go func() {
		waitA <- limitersA.Downlink.WaitN(context.Background(), 3_000)
	}()

	time.Sleep(20 * time.Millisecond)

	waitB := make(chan error, 1)
	startB := time.Now()
	go func() {
		waitB <- limitersB.Downlink.WaitN(context.Background(), 3_000)
	}()

	select {
	case err := <-waitA:
		if err != nil {
			t.Fatalf("shared tunnel a wait failed: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("shared tunnel a wait did not finish in time")
	}

	select {
	case err := <-waitB:
		if err != nil {
			t.Fatalf("shared tunnel b wait failed: %v", err)
		}
	case <-time.After(4 * time.Second):
		t.Fatal("shared tunnel b wait did not finish in time")
	}

	elapsed := time.Since(startB)
	if elapsed < 1400*time.Millisecond {
		t.Fatalf("shared tunnel did not observe competition delay: %s", elapsed)
	}
	if elapsed > 3*time.Second {
		t.Fatalf("shared tunnel competition wait took too long: %s", elapsed)
	}
}

func TestWritePayloadSplitsByLimiterBurst(t *testing.T) {
	writes := make([]string, 0, 3)
	limiter := &fakeLimiter{
		config: BucketConfig{RateBPS: 8_000, BurstBytes: 3},
	}

	err := WritePayload(context.Background(), limiter, 64*1024, []byte("payload"), func(chunk []byte) error {
		writes = append(writes, string(chunk))
		return nil
	})
	if err != nil {
		t.Fatalf("write payload: %v", err)
	}

	if len(writes) != 3 {
		t.Fatalf("unexpected chunk count: %#v", writes)
	}
	if writes[0] != "pay" || writes[1] != "loa" || writes[2] != "d" {
		t.Fatalf("unexpected chunk sequence: %#v", writes)
	}
	if len(limiter.waits) != 3 || limiter.waits[0] != 3 || limiter.waits[1] != 3 || limiter.waits[2] != 1 {
		t.Fatalf("unexpected wait sizes: %#v", limiter.waits)
	}
}

func TestWritePayloadReturnsOnContextCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	limiter := &fakeLimiter{
		config: BucketConfig{RateBPS: 8_000, BurstBytes: 3},
		wait: func(ctx context.Context, bytes int) error {
			cancel()
			<-ctx.Done()
			return ctx.Err()
		},
	}

	err := WritePayload(ctx, limiter, 64*1024, []byte("payload"), func(chunk []byte) error {
		return fmt.Errorf("write callback must not be reached")
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context canceled, got %v", err)
	}
}

type fakeLimiter struct {
	config BucketConfig
	waits  []int
	wait   func(context.Context, int) error
}

func (l *fakeLimiter) Config() BucketConfig {
	return l.config
}

func (l *fakeLimiter) WaitN(ctx context.Context, bytes int) error {
	l.waits = append(l.waits, bytes)
	if l.wait != nil {
		return l.wait(ctx, bytes)
	}
	return nil
}
