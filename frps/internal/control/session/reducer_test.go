package session

import "testing"

func TestReduceSessionAttachedSchedulesHelloAndReconcile(t *testing.T) {
	state := NewState(7, 42)

	next, actions := Reduce(state, SessionAttached{ConnID: "conn-1", HelloRequestID: 7})

	if !next.Conn.Attached || next.Conn.ConnID != "conn-1" {
		t.Fatalf("expected attached control connection, got %+v", next.Conn)
	}
	if next.Phase != SessionPhaseSyncingConfig {
		t.Fatalf("expected syncing-config phase, got %v", next.Phase)
	}
	if len(actions) != 2 {
		t.Fatalf("expected 2 actions, got %d", len(actions))
	}
	hello, ok := actions[0].(ActionSendServerHello)
	if !ok {
		t.Fatalf("expected first action to send server hello, got %T", actions[0])
	}
	if hello.RequestID != 7 {
		t.Fatalf("expected hello request id 7, got %d", hello.RequestID)
	}
	if _, ok := actions[1].(ActionRequestReconcile); !ok {
		t.Fatalf("expected second action to request reconcile, got %T", actions[1])
	}
}

func TestReduceConfigAckPromotesPendingSnapshot(t *testing.T) {
	state := NewState(7, 42)
	state.Conn = ControlConnState{Attached: true, ConnID: "conn-1"}
	state.Pending = &PendingConfigPush{
		RequestID: 9,
		Snapshot: DesiredRuntimeSnapshot{
			Version:     100,
			EffectiveIP: "127.0.0.1",
			Tunnels: []DesiredTunnelRuntime{
				{TunnelID: 1, Protocol: "tcp", Enabled: true, RemoteStart: 8080, RemoteEnd: 8080},
			},
		},
	}

	next, actions := Reduce(state, ConfigAckReceived{RequestID: 9, ConfigVersion: 100})

	if next.Pending != nil {
		t.Fatalf("expected pending config cleared")
	}
	if next.Applied == nil || next.Applied.Snapshot.Version != 100 {
		t.Fatalf("expected applied snapshot version 100, got %+v", next.Applied)
	}
	if next.Epoch != 1 {
		t.Fatalf("expected epoch advanced to 1, got %d", next.Epoch)
	}
	if next.RuntimePhase != RuntimePhaseBinding {
		t.Fatalf("expected runtime phase binding, got %v", next.RuntimePhase)
	}
	if len(actions) != 1 {
		t.Fatalf("expected 1 action, got %d", len(actions))
	}
	if _, ok := actions[0].(ActionPrepareBindings); !ok {
		t.Fatalf("expected prepare-bindings action, got %T", actions[0])
	}
}

func TestReconcilePushesDesiredSnapshotWhenAppliedDiffers(t *testing.T) {
	state := NewState(7, 42)
	state.Conn = ControlConnState{Attached: true, ConnID: "conn-1"}
	state.Desired = &DesiredRuntimeSnapshot{
		Version:     200,
		EffectiveIP: "127.0.0.1",
	}

	next, actions := Reconcile(state)

	if next.Pending == nil {
		t.Fatalf("expected pending config to be created")
	}
	if next.Pending.RequestID == 0 {
		t.Fatalf("expected non-zero request id")
	}
	if len(actions) != 1 {
		t.Fatalf("expected 1 action, got %d", len(actions))
	}
	push, ok := actions[0].(ActionPushConfig)
	if !ok {
		t.Fatalf("expected push-config action, got %T", actions[0])
	}
	if push.Snapshot.Version != 200 {
		t.Fatalf("expected pushed snapshot version 200, got %d", push.Snapshot.Version)
	}
}

func TestReduceBindingCloseRequestsRecovery(t *testing.T) {
	state := NewState(7, 42)
	state.Conn = ControlConnState{Attached: true, ConnID: "conn-1"}
	state.RuntimePhase = RuntimePhaseActive
	key := BindingKey{Protocol: "tcp", EffectiveIP: "127.0.0.1", Port: 8080}
	state.Bindings[key] = BindingState{Key: key, Phase: BindingPhaseActive, Epoch: 1}

	next, actions := Reduce(state, BindingClosed{Key: key, Reason: "listener closed"})

	if next.RuntimePhase != RuntimePhaseRecovering {
		t.Fatalf("expected recovering runtime phase, got %v", next.RuntimePhase)
	}
	if len(actions) != 1 {
		t.Fatalf("expected 1 action, got %d", len(actions))
	}
	if _, ok := actions[0].(ActionRequestReconcile); !ok {
		t.Fatalf("expected reconcile request action, got %T", actions[0])
	}
}
