package testhooks

import (
	"context"
	"testing"
	"time"
)

func TestControllerBarrierBlocksUntilRelease(t *testing.T) {
	controller := NewController()
	restore := Install(controller)
	defer restore()

	controller.AddBarrier("startup.initial_scan.after_full_scan", 1)

	done := make(chan struct{})
	go func() {
		defer close(done)
		Point("startup.initial_scan.after_full_scan", F("scan_round", 1))
	}()

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	hit, err := controller.WaitUntilHit(ctx, "startup.initial_scan.after_full_scan", 1)
	if err != nil {
		t.Fatalf("wait hit: %v", err)
	}
	if hit.Index != 1 {
		t.Fatalf("unexpected hit index: %d", hit.Index)
	}
	if got := hit.Fields["scan_round"]; got != 1 {
		t.Fatalf("unexpected field value: %#v", got)
	}

	select {
	case <-done:
		t.Fatal("point should still be blocked before release")
	default:
	}

	if !controller.Release("startup.initial_scan.after_full_scan", 1) {
		t.Fatal("expected release to succeed")
	}

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("blocked point did not resume after release")
	}
}
