package executor

import (
	"errors"
	"testing"

	controlruntime "github.com/zightch/frp/frps/internal/control/runtime"
	controlsession "github.com/zightch/frp/frps/internal/control/session"
)

func TestBlockReasonForRuntimeError(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want controlsession.BlockReason
	}{
		{
			name: "invalid effective ip",
			err: &controlruntime.GroupEffectiveIPStartError{
				EffectiveIP: "bad",
				Kind:        controlruntime.GroupEffectiveIPStartErrorInvalid,
				Cause:       errors.New("invalid"),
			},
			want: controlsession.BlockReasonEffectiveIPInvalid,
		},
		{
			name: "not local effective ip",
			err: &controlruntime.GroupEffectiveIPStartError{
				EffectiveIP: "192.0.2.1",
				Kind:        controlruntime.GroupEffectiveIPStartErrorNotLocal,
			},
			want: controlsession.BlockReasonEffectiveIPNotLocal,
		},
		{
			name: "port conflict",
			err:  errors.New("端口冲突: bind tcp 10001"),
			want: controlsession.BlockReasonPortConflict,
		},
		{
			name: "generic listener failure",
			err:  errors.New("listen tcp failed"),
			want: controlsession.BlockReasonListenerStartFailed,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := BlockReasonForRuntimeError(tt.err); got != tt.want {
				t.Fatalf("unexpected block reason: got %v want %v", got, tt.want)
			}
		})
	}
}

func TestBindingFailureEventsMapsEveryKey(t *testing.T) {
	keys := []controlsession.BindingKey{
		{Protocol: "tcp", EffectiveIP: "127.0.0.1", Port: 10001},
		{Protocol: "udp", EffectiveIP: "127.0.0.1", Port: 10002},
	}

	events := BindingFailureEvents(keys, errors.New("bind conflict"))
	if len(events) != len(keys) {
		t.Fatalf("unexpected event count: got %d want %d", len(events), len(keys))
	}
	for index, event := range events {
		failed, ok := event.(controlsession.BindingStartFailed)
		if !ok {
			t.Fatalf("event %d should be BindingStartFailed, got %T", index, event)
		}
		if failed.Key != keys[index] {
			t.Fatalf("event %d key mismatch: got %#v want %#v", index, failed.Key, keys[index])
		}
		if failed.Reason != controlsession.BlockReasonPortConflict {
			t.Fatalf("event %d reason mismatch: got %v", index, failed.Reason)
		}
	}
}

func TestBindingOutcomeEventsUsesActiveTunnelsAndIssues(t *testing.T) {
	state := controlsession.NewState(1, 11)
	state.Applied = &controlsession.AppliedRuntimeSnapshot{
		Snapshot: controlsession.DesiredRuntimeSnapshot{
			Tunnels: []controlsession.DesiredTunnelRuntime{
				{
					TunnelID:    7,
					Protocol:    "tcp",
					Enabled:     true,
					RemoteStart: 10001,
					RemoteEnd:   10001,
				},
				{
					TunnelID:    8,
					Protocol:    "udp",
					Enabled:     true,
					RemoteStart: 10002,
					RemoteEnd:   10002,
				},
			},
		},
	}
	keys := []controlsession.BindingKey{
		{Protocol: "tcp", EffectiveIP: "127.0.0.1", Port: 10001},
		{Protocol: "udp", EffectiveIP: "127.0.0.1", Port: 10002},
	}

	events := BindingOutcomeEvents(state, keys, map[uint32]struct{}{7: {}}, map[int64]string{
		8: "端口冲突: bind udp 10002",
	})
	if len(events) != 2 {
		t.Fatalf("unexpected event count: %d", len(events))
	}
	if started, ok := events[0].(controlsession.BindingStarted); !ok || started.Key != keys[0] {
		t.Fatalf("expected first binding started, got %#v", events[0])
	}
	failed, ok := events[1].(controlsession.BindingStartFailed)
	if !ok {
		t.Fatalf("expected second binding failed, got %T", events[1])
	}
	if failed.Key != keys[1] || failed.Reason != controlsession.BlockReasonPortConflict || failed.Message == "" {
		t.Fatalf("unexpected failed event: %#v", failed)
	}
}

func TestBindingOutcomeEventsUsesDefaultMissingListenerMessage(t *testing.T) {
	state := controlsession.NewState(1, 11)
	state.Applied = &controlsession.AppliedRuntimeSnapshot{
		Snapshot: controlsession.DesiredRuntimeSnapshot{
			Tunnels: []controlsession.DesiredTunnelRuntime{
				{
					TunnelID:    7,
					Protocol:    "tcp",
					Enabled:     true,
					RemoteStart: 10001,
					RemoteEnd:   10001,
				},
			},
		},
	}
	key := controlsession.BindingKey{Protocol: "tcp", EffectiveIP: "127.0.0.1", Port: 10001}

	events := BindingOutcomeEvents(state, []controlsession.BindingKey{key}, nil, nil)
	if len(events) != 1 {
		t.Fatalf("unexpected event count: %d", len(events))
	}
	failed, ok := events[0].(controlsession.BindingStartFailed)
	if !ok {
		t.Fatalf("expected binding failure, got %T", events[0])
	}
	if failed.Message != "TCP listener did not start" {
		t.Fatalf("unexpected default message: %q", failed.Message)
	}
	if failed.Reason != controlsession.BlockReasonListenerStartFailed {
		t.Fatalf("unexpected reason: %v", failed.Reason)
	}
}
