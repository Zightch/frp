package testsupport

import (
	"context"
	"fmt"
	"time"

	"github.com/zightch/frp/frps/internal/testhooks"
	sharedtestsupport "github.com/zightch/frp/frps/pkg/testsupport"
)

type BarrierRef struct {
	Point    string
	HitIndex int
}

type BarrierController interface {
	WaitUntilHit(ctx context.Context, point string, hitIndex int) (testhooks.Hit, error)
	Release(point string, hitIndex int) bool
}

type ClockController interface {
	Advance(d time.Duration)
	WaitIdle()
}

type ObserveFunc func() sharedtestsupport.ObservedState

type StepFunc func(context.Context) error

type InvariantFunc func(sharedtestsupport.InvariantCheckInput) []sharedtestsupport.InvariantViolation

type Scenario struct {
	Name  string
	Steps []ScenarioStep
}

type ScenarioStep struct {
	Name    string
	Actor   string
	WaitFor []BarrierRef
	Release []BarrierRef
	Advance time.Duration
	Do      StepFunc
	Observe string
	Assert  []InvariantFunc
}

type StepTrace struct {
	Name        string
	Actor       string
	WaitedFor   []BarrierRef
	Released    []BarrierRef
	Advanced    time.Duration
	Observe     string
	StartedAt   time.Time
	CompletedAt time.Time
}

type Result struct {
	Observations map[string]sharedtestsupport.ObservedState
	Trace        []StepTrace
}

type ScenarioError struct {
	Scenario   string
	Step       string
	Actor      string
	Cause      error
	Violations []sharedtestsupport.InvariantViolation
}

func (e *ScenarioError) Error() string {
	if e == nil {
		return ""
	}
	switch {
	case e.Cause != nil:
		return fmt.Sprintf("scenario %s step %s actor %s failed: %v", e.Scenario, e.Step, e.Actor, e.Cause)
	case len(e.Violations) > 0:
		return fmt.Sprintf("scenario %s step %s actor %s failed invariant %s", e.Scenario, e.Step, e.Actor, e.Violations[0].Rule)
	default:
		return fmt.Sprintf("scenario %s step %s actor %s failed", e.Scenario, e.Step, e.Actor)
	}
}

type Runner struct {
	Hooks   BarrierController
	Clock   ClockController
	Observe ObserveFunc
	Now     func() time.Time
}

func (r *Runner) Run(ctx context.Context, scenario Scenario) (Result, error) {
	result := Result{
		Observations: make(map[string]sharedtestsupport.ObservedState),
	}

	now := time.Now
	if r != nil && r.Now != nil {
		now = r.Now
	}

	var lastObserved *sharedtestsupport.ObservedState

	for _, step := range scenario.Steps {
		trace := StepTrace{
			Name:      step.Name,
			Actor:     step.Actor,
			StartedAt: now(),
		}

		for _, barrier := range step.WaitFor {
			if r == nil || r.Hooks == nil {
				return result, &ScenarioError{
					Scenario: scenario.Name,
					Step:     step.Name,
					Actor:    step.Actor,
					Cause:    fmt.Errorf("barrier controller is not configured"),
				}
			}
			if _, err := r.Hooks.WaitUntilHit(ctx, barrier.Point, barrier.HitIndex); err != nil {
				return result, &ScenarioError{
					Scenario: scenario.Name,
					Step:     step.Name,
					Actor:    step.Actor,
					Cause:    err,
				}
			}
			trace.WaitedFor = append(trace.WaitedFor, barrier)
		}

		if step.Do != nil {
			if err := step.Do(ctx); err != nil {
				return result, &ScenarioError{
					Scenario: scenario.Name,
					Step:     step.Name,
					Actor:    step.Actor,
					Cause:    err,
				}
			}
		}

		if step.Advance > 0 {
			if r == nil || r.Clock == nil {
				return result, &ScenarioError{
					Scenario: scenario.Name,
					Step:     step.Name,
					Actor:    step.Actor,
					Cause:    fmt.Errorf("clock controller is not configured"),
				}
			}
			r.Clock.Advance(step.Advance)
			r.Clock.WaitIdle()
			trace.Advanced = step.Advance
		}

		for _, barrier := range step.Release {
			if r == nil || r.Hooks == nil {
				return result, &ScenarioError{
					Scenario: scenario.Name,
					Step:     step.Name,
					Actor:    step.Actor,
					Cause:    fmt.Errorf("barrier controller is not configured"),
				}
			}
			if !r.Hooks.Release(barrier.Point, barrier.HitIndex) {
				return result, &ScenarioError{
					Scenario: scenario.Name,
					Step:     step.Name,
					Actor:    step.Actor,
					Cause:    fmt.Errorf("release barrier %s[%d] failed", barrier.Point, barrier.HitIndex),
				}
			}
			trace.Released = append(trace.Released, barrier)
		}

		beforeObserved := lastObserved
		currentObserved := lastObserved
		if step.Observe != "" {
			if r == nil || r.Observe == nil {
				return result, &ScenarioError{
					Scenario: scenario.Name,
					Step:     step.Name,
					Actor:    step.Actor,
					Cause:    fmt.Errorf("observe function is not configured"),
				}
			}
			observed := sharedtestsupport.CloneObservedState(r.Observe())
			result.Observations[step.Observe] = observed
			currentObserved = &observed
			trace.Observe = step.Observe
		}

		if len(step.Assert) > 0 {
			for _, invariant := range step.Assert {
				violations := invariant(sharedtestsupport.InvariantCheckInput{
					Before: beforeObserved,
					After:  currentObserved,
				})
				if len(violations) != 0 {
					return result, &ScenarioError{
						Scenario:   scenario.Name,
						Step:       step.Name,
						Actor:      step.Actor,
						Violations: violations,
					}
				}
			}
		}

		if currentObserved != nil {
			lastObserved = currentObserved
		}

		trace.CompletedAt = now()
		result.Trace = append(result.Trace, trace)
	}

	return result, nil
}
