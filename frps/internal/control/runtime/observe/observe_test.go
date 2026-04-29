package observe

import (
	"testing"

	controlruntime "github.com/zightch/frp/frps/internal/control/runtime"
	controlsession "github.com/zightch/frp/frps/internal/control/session"
	"github.com/zightch/frp/frps/pkg/protocol"
	"github.com/zightch/frp/frps/pkg/testsupport"
)

func TestBuildProjectsAndSortsConstructedState(t *testing.T) {
	groups := []controlruntime.GroupRuntime{
		{
			ID:          2,
			Name:        "group-b",
			Enabled:     true,
			EffectiveIP: "127.0.0.1",
			Snapshot: controlruntime.ConfigSnapshot{
				Version: 1,
				Tunnels: []protocol.TunnelEntry{
					tunnel(9, protocol.ProtocolTCP, 22000, true),
				},
			},
		},
		{
			ID:          1,
			Name:        "group-a",
			Enabled:     true,
			EffectiveIP: "127.0.0.1",
			Snapshot: controlruntime.ConfigSnapshot{
				Version: 2,
				Tunnels: []protocol.TunnelEntry{
					tunnel(4, protocol.ProtocolTCP, 22000, true),
					tunnel(3, protocol.ProtocolTCP, 23000, false),
					tunnel(2, protocol.ProtocolUDP, 24000, true),
					tunnel(1, protocol.ProtocolTCP, 25000, true),
				},
			},
		},
	}
	sessions := []controlruntime.SessionSnapshot{
		{
			GroupID:   2,
			SessionID: 20,
			Config: controlruntime.ObservedConfigState{
				State: controlsession.NewState(2, 20),
				Group: groups[0],
			},
			State: controlsession.NewState(2, 20),
			Runtime: controlruntime.ObservedState{
				ListenersStarted: true,
				Generation:       1,
				ActiveTunnelIDs:  map[uint32]struct{}{9: {}},
				AttachedListeners: []controlruntime.ObservedListener{
					{TunnelID: 9, Protocol: "tcp", BindIP: "127.0.0.1", Port: 22000},
				},
				Connections: []controlruntime.ObservedConnection{
					{ConnectionID: 3, Kind: "tcp_stream", Protocol: "tcp", TunnelID: 9, RemotePort: 22000},
				},
			},
		},
		{
			GroupID:   1,
			SessionID: 10,
			Config: controlruntime.ObservedConfigState{
				State: controlsession.NewState(1, 10),
				Group: groups[1],
			},
			State: controlsession.NewState(1, 10),
			Runtime: controlruntime.ObservedState{
				ListenersStarted: true,
				Generation:       2,
				ActiveTunnelIDs:  map[uint32]struct{}{1: {}},
				AttachedListeners: []controlruntime.ObservedListener{
					{TunnelID: 1, Protocol: "tcp", BindIP: "127.0.0.1", Port: 25000},
				},
				Connections: []controlruntime.ObservedConnection{
					{ConnectionID: 2, Kind: "udp_session", Protocol: "udp", TunnelID: 2, RemotePort: 24000},
					{ConnectionID: 1, Kind: "tcp_stream", Protocol: "tcp", TunnelID: 1, RemotePort: 25000},
				},
			},
		},
	}

	state := Build(Input{
		Lifecycle: LifecycleSnapshot{
			InitialRuntimeScanDone: true,
			ControlListenerOpen:    true,
		},
		GroupSlots:    map[int64]uint64{2: 20, 1: 10},
		Sessions:      sessions,
		Groups:        groups,
		RuntimeIssues: map[int64]string{2: `生效 IP "127.0.0.2" 当前不存在于本机，无法启动监听`},
	})

	if !state.LoginGateOpen || len(state.GroupSlots) != 2 {
		t.Fatalf("unexpected lifecycle projection: %#v", state)
	}
	if len(state.Sessions) != 2 || state.Sessions[0].GroupID != 1 || state.Sessions[1].GroupID != 2 {
		t.Fatalf("sessions were not sorted by group/session: %#v", state.Sessions)
	}
	if len(state.Tunnels) != 5 {
		t.Fatalf("expected all tunnels to be projected, got %#v", state.Tunnels)
	}
	assertTunnel(t, state.Tunnels[0], 1, 1, false, "", "启用")
	assertTunnel(t, state.Tunnels[1], 1, 2, false, "effective_ip_not_local", "异常")
	assertTunnel(t, state.Tunnels[2], 1, 3, false, "", "禁用")
	assertTunnel(t, state.Tunnels[3], 1, 4, true, "", "冲突")
	assertTunnel(t, state.Tunnels[4], 2, 9, true, "", "冲突")
	if len(state.Listeners) != 2 || state.Listeners[0].GroupID != 1 || state.Listeners[1].GroupID != 2 {
		t.Fatalf("listeners were not projected and sorted: %#v", state.Listeners)
	}
	if len(state.MissingListeners) != 2 {
		t.Fatalf("expected missing listener projection for non-listening enabled tunnels, got %#v", state.MissingListeners)
	}
	if len(state.Connections) != 3 || state.Connections[0].GroupID != 1 || state.Connections[0].ConnectionID != 1 {
		t.Fatalf("connections were not projected and sorted: %#v", state.Connections)
	}
}

func TestRuntimeIssueKind(t *testing.T) {
	cases := map[string]string{
		"":                "",
		"生效 IP 无效":        "effective_ip_invalid",
		"当前不存在于本机":        "effective_ip_not_local",
		"端口冲突":            "runtime_bind_conflict",
		"listen tcp fail": "runtime_bind_error",
	}
	for reason, expected := range cases {
		if got := RuntimeIssueKind(reason); got != expected {
			t.Fatalf("RuntimeIssueKind(%q) = %q, want %q", reason, got, expected)
		}
	}
}

func tunnel(id uint32, proto uint8, port uint16, enabled bool) protocol.TunnelEntry {
	entry := protocol.TunnelEntry{
		TunnelID:    id,
		Protocol:    proto,
		RemoteStart: port,
		RemoteEnd:   port,
	}
	if enabled {
		entry.TunnelFlags = protocol.TunnelFlagEnabled
	}
	return entry
}

func assertTunnel(t *testing.T, tunnel testsupport.TunnelObservedState, groupID int64, tunnelID uint32, staticConflict bool, runtimeKind, finalStatus string) {
	t.Helper()
	if tunnel.GroupID != groupID ||
		tunnel.TunnelID != tunnelID ||
		tunnel.StaticConflict != staticConflict ||
		tunnel.RuntimeKind != runtimeKind ||
		tunnel.FinalStatus != finalStatus {
		t.Fatalf("unexpected tunnel projection: %#v", tunnel)
	}
}
