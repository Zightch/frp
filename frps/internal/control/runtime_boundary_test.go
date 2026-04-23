package control

import (
	"net"
	"testing"

	"github.com/zightch/frp/frps/pkg/protocol"
)

func TestRuntimeIssueStoreKeepsNewerConfigVersion(t *testing.T) {
	store := newRuntimeIssueStore()

	store.recordForConfig(7, 2, "newer issue")
	store.recordForConfig(7, 1, "")

	issues := store.snapshotReasons()
	if got := issues[7]; got != "newer issue" {
		t.Fatalf("expected newer config version issue to win, got %#v", issues)
	}

	store.recordForConfig(7, 2, "")
	if issues := store.snapshotReasons(); len(issues) != 0 {
		t.Fatalf("expected runtime issue to clear on matching config version, got %#v", issues)
	}
}

func TestRuntimeRegistryBuildsSnapshotAndActiveRuntimeGroups(t *testing.T) {
	host, err := protocol.ParseHost("127.0.0.1")
	if err != nil {
		t.Fatalf("parse host: %v", err)
	}

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen tcp: %v", err)
	}
	defer closeStartedTunnelListeners([]net.Listener{listener}, nil)

	group := GroupRuntime{
		ID:          1,
		Name:        "group-a",
		EffectiveIP: "127.0.0.1",
	}
	snapshot := ConfigSnapshot{
		Version: 3,
		Tunnels: []protocol.TunnelEntry{
			{
				TunnelID:    7,
				Protocol:    protocol.ProtocolTCP,
				TunnelFlags: protocol.TunnelFlagEnabled,
				RemoteStart: uint16(listener.Addr().(*net.TCPAddr).Port),
				RemoteEnd:   uint16(listener.Addr().(*net.TCPAddr).Port),
				LocalHost:   host,
				LocalStart:  22,
				LocalEnd:    22,
			},
		},
	}
	session := newSessionState(11, group, snapshot, 0)
	session.runtimeMu.Lock()
	session.runtime.listeners.started = true
	session.runtime.generation = snapshot.Version
	session.runtime.listeners.tcp[7] = []net.Listener{listener}
	session.runtimeMu.Unlock()

	clientConn, serverConn := net.Pipe()
	defer clientConn.Close()
	defer serverConn.Close()

	registry := newRuntimeRegistry()
	if !registry.reserveGroupSlot(group.ID, session.ID) {
		t.Fatal("expected group slot reservation to succeed")
	}
	registry.register(serverConn, session)

	registrySnapshot := registry.snapshot()
	if got := registrySnapshot.groupSlots[group.ID]; got != session.ID {
		t.Fatalf("unexpected group slot snapshot: %#v", registrySnapshot.groupSlots)
	}
	if len(registrySnapshot.sessions) != 1 {
		t.Fatalf("expected one runtime session snapshot, got %#v", registrySnapshot.sessions)
	}
	if registrySnapshot.sessions[0].runtime.generation != snapshot.Version {
		t.Fatalf("unexpected runtime generation in snapshot: %#v", registrySnapshot.sessions[0].runtime)
	}

	activeGroups := registry.activeRuntimeGroups(nil)
	if len(activeGroups) != 1 {
		t.Fatalf("expected one active runtime group, got %#v", activeGroups)
	}
	if activeGroups[0].group.ID != group.ID || len(activeGroups[0].snapshot.Tunnels) != 1 || activeGroups[0].snapshot.Tunnels[0].TunnelID != 7 {
		t.Fatalf("unexpected active runtime group snapshot: %#v", activeGroups)
	}
	if groups := registry.activeRuntimeGroups(session); len(groups) != 0 {
		t.Fatalf("expected excluded session to be absent from runtime groups, got %#v", groups)
	}
}
