package control

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/zightch/frp/frps/internal/clock"
	controlruntime "github.com/zightch/frp/frps/internal/control/runtime"
	"github.com/zightch/frp/frps/internal/system"
	"github.com/zightch/frp/frps/pkg/protocol"
)

func TestStartTunnelListenersUsesInjectedListenerFactory(t *testing.T) {
	factory := NewScriptedListenerFactory()
	server := NewServer(
		Options{
			ListenerFactory: factory,
		},
		slog.New(slog.NewTextHandler(io.Discard, nil)),
		"test-server",
	)

	tunnel := protocol.TunnelEntry{
		TunnelID:    7,
		Protocol:    protocol.ProtocolTCP,
		TunnelFlags: protocol.TunnelFlagEnabled,
		RemoteStart: 7000,
		RemoteEnd:   7000,
	}

	started, err := server.startTunnelListeners(controlruntime.NewTunnelRuntimeStartContext(1, 2, tunnel, "127.0.0.1"))
	if err != nil {
		t.Fatalf("start tunnel listeners: %v", err)
	}
	defer controlruntime.CloseStartedTunnelListeners(started.TCPListeners, started.UDPListeners)

	calls := factory.Calls()
	if len(calls) != 1 {
		t.Fatalf("unexpected listener call count: %d", len(calls))
	}
	if calls[0].Op != "listen_tcp" {
		t.Fatalf("unexpected listener op: %q", calls[0].Op)
	}
	if calls[0].Bind.Kind != BindKindRuntimeStart {
		t.Fatalf("unexpected bind kind: %q", calls[0].Bind.Kind)
	}
}

func TestProbeTunnelRuntimeIssueUsesInjectedListenerFactory(t *testing.T) {
	factory := NewScriptedListenerFactory()
	factory.SetExternallyOccupied(ListenKey{
		Protocol: "tcp",
		IP:       "127.0.0.1",
		Port:     7001,
	}, true)

	server := NewServer(
		Options{
			ListenerFactory: factory,
		},
		slog.New(slog.NewTextHandler(io.Discard, nil)),
		"test-server",
	)

	tunnel := protocol.TunnelEntry{
		TunnelID:    7,
		Protocol:    protocol.ProtocolTCP,
		TunnelFlags: protocol.TunnelFlagEnabled,
		RemoteStart: 7001,
		RemoteEnd:   7001,
	}

	reason := server.probeTunnelRuntimeIssue(1, "127.0.0.1", tunnel)
	if !strings.Contains(reason, "端口冲突") {
		t.Fatalf("unexpected runtime issue reason: %q", reason)
	}
}

func TestRuntimeIssuePollingUsesInjectedManualScheduler(t *testing.T) {
	manual := clock.NewManual(time.Date(2026, 4, 22, 10, 0, 0, 0, time.UTC))
	repo := &countingRepository{
		group: GroupRuntime{
			ID:          1,
			Name:        "group-a",
			Enabled:     true,
			EffectiveIP: system.AnyIPv4,
		},
	}

	server := NewServer(
		Options{
			Repository:      repo,
			Clock:           manual,
			Scheduler:       manual,
			RuntimeScanPoll: time.Second,
			ListenerFactory: NewScriptedListenerFactory(),
		},
		slog.New(slog.NewTextHandler(io.Discard, nil)),
		"test-server",
	)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	server.startRuntimeIssuePolling(ctx)
	if repo.listCount != 0 {
		t.Fatalf("poller should stay idle before manual advance: %d", repo.listCount)
	}

	manual.Advance(time.Second)
	manual.WaitIdle()

	if repo.listCount != 1 {
		t.Fatalf("unexpected list count after one poll tick: %d", repo.listCount)
	}

	cancel()

	done := make(chan struct{})
	go func() {
		defer close(done)
		server.scanWG.Wait()
	}()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("runtime polling did not shut down after cancel")
	}
}

type countingRepository struct {
	group     GroupRuntime
	listCount int
}

func (r *countingRepository) LoadGroupRuntimeByClientID(context.Context, [16]byte) (GroupRuntime, error) {
	return r.group, nil
}

func (r *countingRepository) LoadGroupRuntimeByID(context.Context, int64) (GroupRuntime, error) {
	return r.group, nil
}

func (r *countingRepository) ListGroupRuntimes(context.Context) ([]GroupRuntime, error) {
	r.listCount++
	return []GroupRuntime{r.group}, nil
}
