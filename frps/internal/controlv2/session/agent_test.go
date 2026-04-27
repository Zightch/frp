package session

import (
	"context"
	"testing"
	"time"
)

func TestAgentProcessesReconcileFollowUp(t *testing.T) {
	initial := NewState(7, 42)
	initial.Desired = &DesiredRuntimeSnapshot{
		Version:     123,
		EffectiveIP: "127.0.0.1",
	}

	agent := NewAgent(initial, NoopExecutor{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go agent.Run(ctx)

	if !agent.Enqueue(SessionAttached{ConnID: "conn-1"}) {
		t.Fatalf("expected enqueue to succeed")
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		state := agent.State()
		if state.Pending != nil {
			if state.Pending.Snapshot.Version != 123 {
				t.Fatalf("expected pending snapshot version 123, got %d", state.Pending.Snapshot.Version)
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}

	t.Fatalf("expected reconcile follow-up to create pending config")
}
