package testsupport

import (
	"context"
	"testing"
	"time"

	"github.com/zightch/frp/frps/internal/testhooks"
	sharedtestsupport "github.com/zightch/frp/frps/pkg/testsupport"
)

type fakeBarrierController struct {
	waited   []BarrierRef
	released []BarrierRef
}

func (f *fakeBarrierController) WaitUntilHit(_ context.Context, point string, hitIndex int) (testhooks.Hit, error) {
	f.waited = append(f.waited, BarrierRef{Point: point, HitIndex: hitIndex})
	return testhooks.Hit{Point: point, Index: hitIndex}, nil
}

func (f *fakeBarrierController) Release(point string, hitIndex int) bool {
	f.released = append(f.released, BarrierRef{Point: point, HitIndex: hitIndex})
	return true
}

type fakeClockController struct {
	advanced []time.Duration
}

func (f *fakeClockController) Advance(d time.Duration) {
	f.advanced = append(f.advanced, d)
}

func (f *fakeClockController) WaitIdle() {}

func TestRunnerRecordsObservationsAndTrace(t *testing.T) {
	barriers := &fakeBarrierController{}
	clock := &fakeClockController{}
	runner := &Runner{
		Hooks: barriers,
		Clock: clock,
		Observe: func() sharedtestsupport.ObservedState {
			return sharedtestsupport.ObservedState{
				App: sharedtestsupport.AppObservedState{
					InitialRuntimeScanDone: true,
					ControlListenerOpen:    true,
					LoginGateOpen:          true,
				},
			}
		},
		Now: func() time.Time { return time.Unix(10, 0) },
	}

	result, err := runner.Run(context.Background(), Scenario{
		Name: "startup",
		Steps: []ScenarioStep{
			{
				Name:    "wait-and-observe",
				Actor:   "frps",
				WaitFor: []BarrierRef{{Point: "startup.initial_scan.after_full_scan", HitIndex: 1}},
				Release: []BarrierRef{{Point: "startup.initial_scan.after_full_scan", HitIndex: 1}},
				Advance: time.Second,
				Observe: "after-initial-scan",
				Assert: []InvariantFunc{
					sharedtestsupport.CheckStartupGate,
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("run scenario: %v", err)
	}
	if len(result.Trace) != 1 {
		t.Fatalf("expected 1 trace entry, got %d", len(result.Trace))
	}
	if len(result.Observations) != 1 {
		t.Fatalf("expected 1 observation, got %d", len(result.Observations))
	}
	if len(barriers.waited) != 1 || len(barriers.released) != 1 {
		t.Fatalf("unexpected barrier trace: waited=%d released=%d", len(barriers.waited), len(barriers.released))
	}
	if len(clock.advanced) != 1 || clock.advanced[0] != time.Second {
		t.Fatalf("unexpected clock advance trace: %#v", clock.advanced)
	}
}
