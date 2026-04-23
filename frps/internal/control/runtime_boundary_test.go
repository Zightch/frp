package control

import (
	"io"
	"log/slog"
	"net"
	"testing"

	"github.com/zightch/frp/frps/pkg/protocol"
	"github.com/zightch/frp/frps/pkg/testsupport"
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
	if _, ok := registrySnapshot.sessions[0].runtime.activeTunnelIDs[7]; !ok {
		t.Fatalf("expected runtime snapshot to expose active tunnel ids, got %#v", registrySnapshot.sessions[0].runtime.activeTunnelIDs)
	}
	if len(registrySnapshot.sessions[0].runtime.attachedListeners) != 1 {
		t.Fatalf("expected runtime snapshot to expose attached listeners, got %#v", registrySnapshot.sessions[0].runtime.attachedListeners)
	}
	if registrySnapshot.sessions[0].runtime.attachedListeners[0].port != uint16(listener.Addr().(*net.TCPAddr).Port) {
		t.Fatalf("unexpected attached listener port: %#v", registrySnapshot.sessions[0].runtime.attachedListeners)
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

func TestSessionConfigApplyTracksPendingAndAppliedRecoveryModes(t *testing.T) {
	session := newSessionState(11, GroupRuntime{ID: 1, Name: "group-a"}, ConfigSnapshot{Version: 1}, 0)

	fullSnapshot := ConfigSnapshot{
		Version: 2,
		Tunnels: []protocol.TunnelEntry{
			{TunnelID: 7, Protocol: protocol.ProtocolTCP, TunnelFlags: protocol.TunnelFlagEnabled},
		},
	}
	pushOp, err := session.prepareConfigPush(GroupRuntime{ID: 1, Name: "group-a"}, fullSnapshot)
	if err != nil {
		t.Fatalf("prepare config push: %v", err)
	}
	if pushOp.requestID == 0 {
		t.Fatal("expected non-zero request id")
	}
	if got := session.recoveryModeValue(); got != testsupport.RecoveryModePendingFullConfig {
		t.Fatalf("expected pending full-config recovery mode, got %s", got)
	}

	applied, err := session.acceptConfigAck(pushOp.requestID, fullSnapshot.Version)
	if err != nil {
		t.Fatalf("accept config ack: %v", err)
	}
	if applied.recoveryMode != testsupport.RecoveryModeRunning {
		t.Fatalf("expected running recovery mode after ack, got %s", applied.recoveryMode)
	}
	if got := session.recoveryModeValue(); got != testsupport.RecoveryModeRunning {
		t.Fatalf("expected running recovery mode on session, got %s", got)
	}

	emptySnapshot := ConfigSnapshot{Version: 3}
	emptyPush, err := session.prepareConfigPush(GroupRuntime{ID: 1, Name: "group-a"}, emptySnapshot)
	if err != nil {
		t.Fatalf("prepare empty config push: %v", err)
	}
	if got := session.recoveryModeValue(); got != testsupport.RecoveryModePendingEmptyConfig {
		t.Fatalf("expected pending empty-config recovery mode, got %s", got)
	}

	emptyApplied, err := session.acceptConfigAck(emptyPush.requestID, emptySnapshot.Version)
	if err != nil {
		t.Fatalf("accept empty config ack: %v", err)
	}
	if emptyApplied.recoveryMode != testsupport.RecoveryModeEmptyConfig {
		t.Fatalf("expected empty-config recovery mode after ack, got %s", emptyApplied.recoveryMode)
	}
	if got := session.recoveryModeValue(); got != testsupport.RecoveryModeEmptyConfig {
		t.Fatalf("expected empty-config recovery mode on session, got %s", got)
	}
}

func TestServerPlanSessionRuntimeStartSeparatesActiveAndPendingTunnels(t *testing.T) {
	activeListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen active tunnel: %v", err)
	}
	defer closeStartedTunnelListeners([]net.Listener{activeListener}, nil)

	activePort := uint16(activeListener.Addr().(*net.TCPAddr).Port)
	session := newSessionState(
		11,
		GroupRuntime{ID: 1, Name: "group-a", EffectiveIP: "127.0.0.1"},
		ConfigSnapshot{
			Version: 4,
			Tunnels: []protocol.TunnelEntry{
				{TunnelID: 6, Protocol: protocol.ProtocolTCP},
				{TunnelID: 7, Protocol: protocol.ProtocolTCP, TunnelFlags: protocol.TunnelFlagEnabled, RemoteStart: activePort, RemoteEnd: activePort},
				{TunnelID: 8, Protocol: protocol.ProtocolTCP, TunnelFlags: protocol.TunnelFlagEnabled, RemoteStart: activePort + 1, RemoteEnd: activePort + 1},
			},
		},
		0,
	)
	session.runtimeMu.Lock()
	session.runtime.listeners.started = true
	session.runtime.generation = 4
	session.runtime.listeners.tcp[7] = []net.Listener{activeListener}
	session.runtimeMu.Unlock()

	server := NewServer(
		Options{WriteTimeout: 0},
		slog.New(slog.NewTextHandler(io.Discard, nil)),
		"test-server",
	)

	plan := server.planSessionRuntimeStart(newSessionRuntimeStartTarget(nil, slog.New(slog.NewTextHandler(io.Discard, nil)), session))
	if !plan.activeRuntime {
		t.Fatal("expected plan to mark active runtime")
	}
	if plan.bindIP != "127.0.0.1" {
		t.Fatalf("unexpected bind ip: %q", plan.bindIP)
	}
	if len(plan.targetTunnels) != 1 || plan.targetTunnels[0].TunnelID != 8 {
		t.Fatalf("unexpected start targets: %#v", plan.targetTunnels)
	}
	if len(plan.clearIssueTunnelIDs) != 2 {
		t.Fatalf("unexpected clear-issue tunnel ids: %#v", plan.clearIssueTunnelIDs)
	}
	if plan.clearIssueTunnelIDs[0] != 6 || plan.clearIssueTunnelIDs[1] != 7 {
		t.Fatalf("unexpected clear-issue ordering: %#v", plan.clearIssueTunnelIDs)
	}
}
