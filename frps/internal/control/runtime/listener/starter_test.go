package listener

import (
	"context"
	"crypto/tls"
	"errors"
	"strings"
	"testing"

	controlbind "github.com/zightch/frp/frps/internal/control/bind"
	"github.com/zightch/frp/frps/pkg/protocol"
)

func TestStarterUsesInjectedFactoryForTCP(t *testing.T) {
	factory := controlbind.NewScriptedListenerFactory()
	starter := NewStarter(StarterOptions{Factory: factory})

	tunnel := protocol.TunnelEntry{
		TunnelID:    7,
		Protocol:    protocol.ProtocolTCP,
		TunnelFlags: protocol.TunnelFlagEnabled,
		RemoteStart: 7000,
		RemoteEnd:   7000,
	}

	started, err := starter.StartTunnelListeners(NewTunnelRuntimeStartContext(1, 2, tunnel, "127.0.0.1"))
	if err != nil {
		t.Fatalf("start tunnel listeners: %v", err)
	}
	defer CloseStartedTunnelListeners(started.TCPListeners, started.UDPListeners)

	calls := factory.Calls()
	if len(calls) != 1 {
		t.Fatalf("unexpected listener call count: %d", len(calls))
	}
	if calls[0].Op != "listen_tcp" {
		t.Fatalf("unexpected listener op: %q", calls[0].Op)
	}
	if calls[0].Bind.Kind != controlbind.BindKindRuntimeStart {
		t.Fatalf("unexpected bind kind: %q", calls[0].Bind.Kind)
	}
}

func TestStarterUsesInjectedFactoryForUDP(t *testing.T) {
	factory := controlbind.NewScriptedListenerFactory()
	starter := NewStarter(StarterOptions{Factory: factory})

	tunnel := protocol.TunnelEntry{
		TunnelID:    8,
		Protocol:    protocol.ProtocolUDP,
		TunnelFlags: protocol.TunnelFlagEnabled,
		RemoteStart: 7001,
		RemoteEnd:   7001,
	}

	started, err := starter.StartTunnelListeners(NewTunnelRuntimeStartContext(1, 2, tunnel, "127.0.0.1"))
	if err != nil {
		t.Fatalf("start udp tunnel listeners: %v", err)
	}
	defer CloseStartedTunnelListeners(started.TCPListeners, started.UDPListeners)

	calls := factory.Calls()
	if len(calls) != 2 {
		t.Fatalf("unexpected listener call count: %d", len(calls))
	}
	if calls[0].Op != "resolve_udp" || calls[1].Op != "listen_udp" {
		t.Fatalf("unexpected listener ops: %#v", calls)
	}
	if calls[0].Bind.Kind != controlbind.BindKindRuntimeStart || calls[1].Bind.Kind != controlbind.BindKindRuntimeStart {
		t.Fatalf("unexpected bind kinds: %#v", calls)
	}
}

func TestStarterClosesStartedListenersOnLaterFailure(t *testing.T) {
	factory := controlbind.NewScriptedListenerFactory()
	factory.AddFailure(controlbind.ScriptedListenerFailure{
		Op:   "listen_tcp",
		Kind: controlbind.BindKindRuntimeStart,
		Key: controlbind.ListenKey{
			Protocol: "tcp",
			IP:       "127.0.0.1",
			Port:     7001,
		},
		Err: errors.New("injected bind failure"),
	})
	starter := NewStarter(StarterOptions{Factory: factory})

	tunnel := protocol.TunnelEntry{
		TunnelID:    7,
		Protocol:    protocol.ProtocolTCP,
		TunnelFlags: protocol.TunnelFlagEnabled,
		RemoteStart: 7000,
		RemoteEnd:   7001,
	}

	if _, err := starter.StartTunnelListeners(NewTunnelRuntimeStartContext(1, 2, tunnel, "127.0.0.1")); err == nil {
		t.Fatal("expected listener start failure")
	}

	state := factory.ObserveState()
	if len(state.Handles) != 0 {
		t.Fatalf("expected partial listener cleanup, got %#v", state.Handles)
	}
}

func TestStarterLoadsTCPListenerTLSBeforeBind(t *testing.T) {
	factory := controlbind.NewScriptedListenerFactory()
	starter := NewStarter(StarterOptions{
		Factory: factory,
		TLSLoader: func(_ context.Context, tunnelID uint32) (*tls.Config, error) {
			if tunnelID != 7 {
				t.Fatalf("unexpected tunnel id: %d", tunnelID)
			}
			return nil, errors.New("tls config failed")
		},
	})

	tunnel := protocol.TunnelEntry{
		TunnelID:    7,
		Protocol:    protocol.ProtocolTCP,
		TunnelFlags: protocol.TunnelFlagEnabled,
		RemoteStart: 7000,
		RemoteEnd:   7000,
	}

	if _, err := starter.StartTunnelListeners(NewTunnelRuntimeStartContext(1, 2, tunnel, "127.0.0.1")); err == nil {
		t.Fatal("expected tls config failure")
	}
	if calls := factory.Calls(); len(calls) != 0 {
		t.Fatalf("expected no bind before tls config success, got %#v", calls)
	}
}

func TestProbeTunnelRuntimeIssueUsesStarter(t *testing.T) {
	factory := controlbind.NewScriptedListenerFactory()
	factory.SetExternallyOccupied(controlbind.ListenKey{
		Protocol: "tcp",
		IP:       "127.0.0.1",
		Port:     7001,
	}, true)
	starter := NewStarter(StarterOptions{Factory: factory})

	tunnel := protocol.TunnelEntry{
		TunnelID:    7,
		Protocol:    protocol.ProtocolTCP,
		TunnelFlags: protocol.TunnelFlagEnabled,
		RemoteStart: 7001,
		RemoteEnd:   7001,
	}

	reason := ProbeTunnelRuntimeIssue(starter, 1, "127.0.0.1", tunnel)
	if !strings.Contains(reason, "端口冲突") {
		t.Fatalf("unexpected runtime issue reason: %q", reason)
	}
	calls := factory.Calls()
	if len(calls) != 1 || calls[0].Bind.Kind != controlbind.BindKindRuntimeProbe {
		t.Fatalf("expected one runtime probe call, got %#v", calls)
	}
}
