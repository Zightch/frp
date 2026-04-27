package controlv2

import (
	"context"
	"testing"
	"time"

	"github.com/zightch/frp/frps/internal/controlv2/session"
)

func TestServerHandleAuthenticatedClientLoadsDesiredRuntime(t *testing.T) {
	clientID := [16]byte{0x01, 0x02, 0x03}
	repo := stubRuntimeRepo{
		record: DesiredRuntimeRecord{
			GroupID: 7,
			Snapshot: session.DesiredRuntimeSnapshot{
				Version:       11,
				GeneratedAtMs: 22,
				EffectiveIP:   "127.0.0.1",
				Tunnels: []session.DesiredTunnelRuntime{
					{
						TunnelID:    9,
						Protocol:    "tcp",
						Enabled:     true,
						RemoteStart: 20000,
						RemoteEnd:   20000,
						LocalHost:   "127.0.0.1",
						LocalStart:  22,
						LocalEnd:    22,
					},
				},
			},
		},
	}

	server, err := NewServer(Options{
		Repo:       repo,
		Supervisor: NewSupervisor(session.NoopExecutor{}),
	})
	if err != nil {
		t.Fatalf("new server: %v", err)
	}
	defer server.Shutdown()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	agent, err := server.HandleAuthenticatedClient(ctx, clientID, 42, "conn-a")
	if err != nil {
		t.Fatalf("handle authenticated client: %v", err)
	}

	deadline := time.Now().Add(2 * time.Second)
	for {
		state := agent.State()
		if state.GroupID == 7 && state.SessionID == 42 && state.Conn.ConnID == "conn-a" && state.Pending != nil {
			if state.Pending.Snapshot.Version != 11 {
				t.Fatalf("unexpected pending snapshot: %#v", state.Pending.Snapshot)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("session state did not converge: %#v", agent.State())
		}
		time.Sleep(10 * time.Millisecond)
	}
}

type stubRuntimeRepo struct {
	record DesiredRuntimeRecord
}

func (r stubRuntimeRepo) LoadDesiredRuntimeByClientID(context.Context, [16]byte) (DesiredRuntimeRecord, error) {
	return r.record, nil
}

func (r stubRuntimeRepo) LoadDesiredRuntimeByGroupID(context.Context, int64) (DesiredRuntimeRecord, error) {
	return r.record, nil
}

func (r stubRuntimeRepo) ListDesiredRuntimes(context.Context) ([]DesiredRuntimeRecord, error) {
	return []DesiredRuntimeRecord{r.record}, nil
}
