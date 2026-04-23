package control

import (
	"io"
	"log/slog"
	"testing"

	"github.com/zightch/frp/frps/internal/system"
	"github.com/zightch/frp/frps/pkg/protocol"
)

func TestRuntimeCoordinatorPlanRefreshDeduplicatesMatchingPendingSnapshot(t *testing.T) {
	server := newRuntimeCoordinatorTestServer("127.0.0.1")

	currentSnapshot := runtimeCoordinatorTestSnapshot(t, 1, 7, 7000)
	currentGroup := runtimeCoordinatorTestGroup("group-a", "127.0.0.1", true, currentSnapshot)
	session := newSessionState(11, currentGroup, currentSnapshot, 0)

	nextSnapshot := runtimeCoordinatorTestSnapshot(t, 2, 7, 7000)
	nextGroup := runtimeCoordinatorTestGroup("group-a", "127.0.0.1", true, nextSnapshot)
	if err := session.reconfigure(nextGroup, nextSnapshot, 101); err != nil {
		t.Fatalf("set pending config: %v", err)
	}

	plan := server.runtimeCoordinator().planRefresh(newRuntimeRecoveryTarget(nil, nil, session), nextGroup)
	if plan.action != runtimeRecoveryActionNoop {
		t.Fatalf("expected noop refresh plan, got %s", plan.action.String())
	}
	if plan.snapshot.Version != nextSnapshot.Version {
		t.Fatalf("expected deduplicated snapshot version %d, got %d", nextSnapshot.Version, plan.snapshot.Version)
	}
}

func TestRuntimeCoordinatorPlanRefreshClosesWhenDifferentPendingSnapshotExists(t *testing.T) {
	server := newRuntimeCoordinatorTestServer("127.0.0.1")

	currentSnapshot := runtimeCoordinatorTestSnapshot(t, 1, 7, 7000)
	currentGroup := runtimeCoordinatorTestGroup("group-a", "127.0.0.1", true, currentSnapshot)
	session := newSessionState(11, currentGroup, currentSnapshot, 0)

	pendingSnapshot := runtimeCoordinatorTestSnapshot(t, 2, 7, 7001)
	pendingGroup := runtimeCoordinatorTestGroup("group-a", "127.0.0.1", true, pendingSnapshot)
	if err := session.reconfigure(pendingGroup, pendingSnapshot, 101); err != nil {
		t.Fatalf("set pending config: %v", err)
	}

	nextSnapshot := runtimeCoordinatorTestSnapshot(t, 3, 7, 7002)
	nextGroup := runtimeCoordinatorTestGroup("group-a", "127.0.0.1", true, nextSnapshot)
	plan := server.runtimeCoordinator().planRefresh(newRuntimeRecoveryTarget(nil, nil, session), nextGroup)
	if plan.action != runtimeRecoveryActionCloseSession {
		t.Fatalf("expected close_session refresh plan, got %s", plan.action.String())
	}
}

func TestRuntimeCoordinatorPlanRefreshRebindsWhenOnlyEffectiveIPChanges(t *testing.T) {
	server := newRuntimeCoordinatorTestServer("127.0.0.1", "127.0.0.2")

	currentSnapshot := runtimeCoordinatorTestSnapshot(t, 2, 7, 7000)
	currentGroup := runtimeCoordinatorTestGroup("group-a", "127.0.0.1", true, currentSnapshot)
	session := newSessionState(11, currentGroup, currentSnapshot, 0)

	nextGroup := runtimeCoordinatorTestGroup("group-a", "127.0.0.2", true, runtimeCoordinatorTestSnapshot(t, 3, 7, 7000))
	plan := server.runtimeCoordinator().planRefresh(newRuntimeRecoveryTarget(nil, nil, session), nextGroup)
	if plan.action != runtimeRecoveryActionRebindRuntime {
		t.Fatalf("expected rebind_runtime refresh plan, got %s", plan.action.String())
	}
}

func TestRuntimeCoordinatorPlanRefreshShrinksToEmptyWhenEffectiveIPUnavailable(t *testing.T) {
	server := newRuntimeCoordinatorTestServer("127.0.0.1")

	currentSnapshot := runtimeCoordinatorTestSnapshot(t, 2, 7, 7000)
	currentGroup := runtimeCoordinatorTestGroup("group-a", "127.0.0.1", true, currentSnapshot)
	session := newSessionState(11, currentGroup, currentSnapshot, 0)

	nextSnapshot := runtimeCoordinatorTestSnapshot(t, 3, 7, 7000)
	nextGroup := runtimeCoordinatorTestGroup("group-a", "10.0.0.1", true, nextSnapshot)
	plan := server.runtimeCoordinator().planRefresh(newRuntimeRecoveryTarget(nil, nil, session), nextGroup)
	if plan.action != runtimeRecoveryActionPushEmptyConfig {
		t.Fatalf("expected push_empty_config refresh plan, got %s", plan.action.String())
	}
	if len(plan.snapshot.Tunnels) != 0 {
		t.Fatalf("expected empty config snapshot after effective_ip failure, got %d tunnels", len(plan.snapshot.Tunnels))
	}
}

func TestRuntimeCoordinatorPlanScannedRecoveryEnsuresListenersForHealthyRuntime(t *testing.T) {
	server := newRuntimeCoordinatorTestServer("127.0.0.1")

	currentSnapshot := runtimeCoordinatorTestSnapshot(t, 2, 7, 7000)
	currentGroup := runtimeCoordinatorTestGroup("group-a", "127.0.0.1", true, currentSnapshot)
	session := newSessionState(11, currentGroup, currentSnapshot, 0)

	plan := server.runtimeCoordinator().planScannedRecovery(
		newRuntimeRecoveryTarget(nil, nil, session),
		currentGroup,
		currentSnapshot.Tunnels,
		nil,
		map[uint32]string{},
	)
	if plan.action != runtimeRecoveryActionEnsureListeners {
		t.Fatalf("expected ensure_listeners scanned recovery plan, got %s", plan.action.String())
	}
}

func TestRuntimeCoordinatorPlanScannedRecoveryPushesFullConfigAfterEmptyRuntimeRecovers(t *testing.T) {
	server := newRuntimeCoordinatorTestServer("127.0.0.1")

	currentSnapshot := ConfigSnapshot{Version: 2}
	currentGroup := runtimeCoordinatorTestGroup("group-a", "127.0.0.1", true, currentSnapshot)
	session := newSessionState(11, currentGroup, currentSnapshot, 0)

	nextSnapshot := runtimeCoordinatorTestSnapshot(t, 3, 7, 7000)
	nextGroup := runtimeCoordinatorTestGroup("group-a", "127.0.0.1", true, nextSnapshot)
	plan := server.runtimeCoordinator().planScannedRecovery(
		newRuntimeRecoveryTarget(nil, nil, session),
		nextGroup,
		nextSnapshot.Tunnels,
		nil,
		map[uint32]string{},
	)
	if plan.action != runtimeRecoveryActionPushFullConfig {
		t.Fatalf("expected push_full_config scanned recovery plan, got %s", plan.action.String())
	}
	if plan.snapshot.Version != nextSnapshot.Version {
		t.Fatalf("expected recovered config version %d, got %d", nextSnapshot.Version, plan.snapshot.Version)
	}
}

func newRuntimeCoordinatorTestServer(availableIPs ...string) *Server {
	snapshot := system.Snapshot{}
	if len(availableIPs) > 0 {
		snapshot.AvailableIPs = make([]system.IPAddress, 0, len(availableIPs))
		for _, ip := range availableIPs {
			snapshot.AvailableIPs = append(snapshot.AvailableIPs, system.IPAddress{
				Addr:   ip,
				Family: system.FamilyIPv4,
			})
		}
	}
	return NewServer(
		Options{
			Network: system.NewStaticSnapshotReader(snapshot),
		},
		slog.New(slog.NewTextHandler(io.Discard, nil)),
		"test",
	)
}

func runtimeCoordinatorTestGroup(name, effectiveIP string, enabled bool, snapshot ConfigSnapshot) GroupRuntime {
	return GroupRuntime{
		ID:               1,
		Name:             name,
		Enabled:          enabled,
		EffectiveIP:      effectiveIP,
		ClientSecretHash: [32]byte{1},
		Snapshot:         snapshot,
	}
}

func runtimeCoordinatorTestSnapshot(t *testing.T, version uint64, tunnelID uint32, remotePort uint16) ConfigSnapshot {
	t.Helper()

	host, err := protocol.ParseHost("127.0.0.1")
	if err != nil {
		t.Fatalf("parse host: %v", err)
	}

	return ConfigSnapshot{
		Version: version,
		Tunnels: []protocol.TunnelEntry{
			{
				TunnelID:    tunnelID,
				Protocol:    protocol.ProtocolTCP,
				TunnelFlags: protocol.TunnelFlagEnabled,
				RemoteStart: remotePort,
				RemoteEnd:   remotePort,
				LocalHost:   host,
				LocalStart:  80,
				LocalEnd:    80,
			},
		},
	}
}
