package clock

import (
	"context"
	"time"
)

type Scheduler interface {
	Go(ctx context.Context, name string, fn func(context.Context)) Task
	Every(ctx context.Context, name string, interval time.Duration, fn func(context.Context, time.Time)) Task
}

type RealClock struct{}

type realScheduler struct{}

type task struct {
	done chan struct{}
}

func NewRealClock() RealClock {
	return RealClock{}
}

func NewRealScheduler() Scheduler {
	return &realScheduler{}
}

func (RealClock) Now() time.Time {
	return time.Now().UTC()
}

func (t *task) Done() <-chan struct{} {
	return t.done
}

func newTask() *task {
	return &task{done: make(chan struct{})}
}

func (s *realScheduler) Go(ctx context.Context, _ string, fn func(context.Context)) Task {
	task := newTask()
	go func() {
		defer close(task.done)
		fn(ctx)
	}()
	return task
}

func (s *realScheduler) Every(ctx context.Context, _ string, interval time.Duration, fn func(context.Context, time.Time)) Task {
	task := newTask()
	go func() {
		defer close(task.done)

		ticker := time.NewTicker(interval)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case firedAt := <-ticker.C:
				fn(ctx, firedAt.UTC())
			}
		}
	}()
	return task
}
