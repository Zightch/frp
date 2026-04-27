package bind

import (
	"context"
	"errors"
	"testing"

	"github.com/zightch/frp/frps/internal/control/session"
)

func TestMemoryManagerPrepareStartStop(t *testing.T) {
	manager := NewMemoryManager()

	req := PrepareBindingsRequest{
		GroupID:     1,
		SessionID:   10,
		Epoch:       2,
		EffectiveIP: "127.0.0.1",
		Tunnels: []session.DesiredTunnelRuntime{
			{
				TunnelID:    3,
				Protocol:    "tcp",
				Enabled:     true,
				RemoteStart: 8080,
				RemoteEnd:   8081,
			},
		},
	}

	prepared, err := manager.Prepare(context.Background(), req)
	if err != nil {
		t.Fatalf("expected prepare success, got %v", err)
	}
	if len(prepared.Keys) != 2 {
		t.Fatalf("expected 2 keys, got %d", len(prepared.Keys))
	}

	tunnelIDs := make(map[session.BindingKey]uint32, len(prepared.Keys))
	for _, key := range prepared.Keys {
		tunnelIDs[key] = 3
	}
	if err := manager.Start(context.Background(), StartBindingsRequest{
		GroupID:   1,
		SessionID: 10,
		Epoch:     2,
		TunnelIDs: tunnelIDs,
		Keys:      prepared.Keys,
	}); err != nil {
		t.Fatalf("expected start success, got %v", err)
	}

	_, err = manager.Prepare(context.Background(), PrepareBindingsRequest{
		GroupID:     2,
		SessionID:   11,
		Epoch:       1,
		EffectiveIP: "127.0.0.1",
		Tunnels: []session.DesiredTunnelRuntime{
			{
				TunnelID:    4,
				Protocol:    "tcp",
				Enabled:     true,
				RemoteStart: 8081,
				RemoteEnd:   8081,
			},
		},
	})
	if !errors.Is(err, ErrBindingConflict) {
		t.Fatalf("expected binding conflict, got %v", err)
	}

	if err := manager.Stop(context.Background(), StopBindingsRequest{
		GroupID:   1,
		SessionID: 10,
		Keys:      prepared.Keys,
	}); err != nil {
		t.Fatalf("expected stop success, got %v", err)
	}
}
