package session

import (
	"context"
	"sync"
)

type Executor interface {
	Execute(ctx context.Context, state SessionState, action Action) []Event
}

type Agent struct {
	executor Executor

	events chan Event
	done   chan struct{}

	mu    sync.RWMutex
	state SessionState
}

func NewAgent(initial SessionState, executor Executor) *Agent {
	return &Agent{
		executor: executor,
		events:   make(chan Event, 64),
		done:     make(chan struct{}),
		state:    cloneState(initial),
	}
}

func (a *Agent) Run(ctx context.Context) {
	defer close(a.done)

	for {
		select {
		case <-ctx.Done():
			a.apply(ctx, ShutdownRequested{})
			return
		case event, ok := <-a.events:
			if !ok {
				a.apply(ctx, ShutdownRequested{})
				return
			}
			a.apply(ctx, event)
			if a.State().Phase == SessionPhaseClosed {
				return
			}
		}
	}
}

func (a *Agent) Enqueue(event Event) bool {
	if a == nil {
		return false
	}

	select {
	case <-a.done:
		return false
	default:
	}

	select {
	case a.events <- event:
		return true
	case <-a.done:
		return false
	}
}

func (a *Agent) Stop() {
	if a == nil {
		return
	}

	select {
	case <-a.done:
		return
	default:
	}

	select {
	case a.events <- ShutdownRequested{}:
	default:
	}
}

func (a *Agent) Done() <-chan struct{} {
	if a == nil {
		return nil
	}
	return a.done
}

func (a *Agent) State() SessionState {
	if a == nil {
		return SessionState{}
	}
	a.mu.RLock()
	defer a.mu.RUnlock()
	return cloneState(a.state)
}

func (a *Agent) setState(state SessionState) {
	a.mu.Lock()
	a.state = cloneState(state)
	a.mu.Unlock()
}

func (a *Agent) apply(ctx context.Context, event Event) {
	if a == nil {
		return
	}

	state := a.State()
	queue := []Event{event}

	for len(queue) > 0 {
		current := queue[0]
		queue = queue[1:]

		next, actions := Reduce(state, current)
		state = next
		a.setState(state)

		for _, action := range actions {
			queue = append(queue, a.executeAction(ctx, state, action)...)
		}
	}
}

func (a *Agent) executeAction(ctx context.Context, state SessionState, action Action) []Event {
	switch typed := action.(type) {
	case ActionRequestReconcile:
		return []Event{ReconcileRequested{Reason: typed.Reason}}
	case ActionLogTransition:
		return nil
	default:
		if a.executor == nil {
			return nil
		}
		return a.executor.Execute(ctx, state, action)
	}
}

type NoopExecutor struct{}

func (NoopExecutor) Execute(context.Context, SessionState, Action) []Event {
	return nil
}
