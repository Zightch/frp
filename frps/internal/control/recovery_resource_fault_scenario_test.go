//go:build testhooks

package control

import (
	"context"
	"crypto/sha256"
	"io"
	"log/slog"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/zightch/frp/frps/internal/system"
	"github.com/zightch/frp/frps/pkg/protocol"
	sharedtestsupport "github.com/zightch/frp/frps/pkg/testsupport"
)

func TestServerScenarioKeepsSessionAliveWhenTCPRecoveryBindFailsWithPermissionDenied(t *testing.T) {
	tokenID, tokenHash := fixedTestToken()
	host := mustParseTestHost(t, "127.0.0.1")

	listenKey := ListenKey{Protocol: "tcp", IP: "127.0.0.1", Port: uint16(freeTCPPort(t))}
	listenerFactory := NewScriptedListenerFactory()
	listenerFactory.SetExternallyOccupied(listenKey, true)

	repo := &mutableRepository{
		group: GroupRuntime{
			ID:          1,
			Name:        "group-a",
			Enabled:     true,
			EffectiveIP: "127.0.0.1",
			TokenHash:   tokenHash,
			Snapshot: ConfigSnapshot{
				Version:       1,
				GeneratedAtMs: 100,
				Tunnels: []protocol.TunnelEntry{
					{
						TunnelID:    7,
						Protocol:    protocol.ProtocolTCP,
						TunnelFlags: protocol.TunnelFlagEnabled,
						RemoteStart: listenKey.Port,
						RemoteEnd:   listenKey.Port,
						LocalHost:   host,
						LocalStart:  2200,
						LocalEnd:    2200,
					},
				},
			},
		},
	}

	server := NewServer(
		Options{
			Repository:      repo,
			Network:         staticSnapshotReader{snapshot: localIPv4Snapshot("127.0.0.1")},
			ListenerFactory: listenerFactory,
		},
		slog.New(slog.NewTextHandler(io.Discard, nil)),
		"test-server",
	)

	clientConn, done, configFrame := authenticateServerSession(t, server, tokenID, tokenHash)
	defer clientConn.Close()

	configPush, err := protocol.UnmarshalConfigPush(configFrame.Body)
	if err != nil {
		t.Fatalf("unmarshal config.push: %v", err)
	}
	writeConfigAck(t, clientConn, configFrame.RequestID, configPush.ConfigVersion)

	blockedState := waitForObservedState(t, server, listenerFactory, func(observed sharedtestsupport.ObservedState) bool {
		session := findObservedSession(observed.Server, repo.group.ID)
		tunnel := findObservedTunnel(observed.Server, repo.group.ID, 7)
		return session != nil &&
			tunnel != nil &&
			strings.Contains(tunnel.RuntimeIssue, "端口冲突") &&
			tunnel.FinalStatus == "异常" &&
			countObservedListeners(observed.Server, repo.group.ID, 7) == 0 &&
			handleCountForPort(observed, listenKey) == 0
	})

	blockedSession := requireObservedSession(t, blockedState.Server, repo.group.ID)
	if blockedState.Server.GroupSlots[repo.group.ID] != blockedSession.SessionID {
		t.Fatalf("expected blocked session to keep group slot, got %#v", blockedState.Server.GroupSlots)
	}

	listenerFactory.AddFailure(ScriptedListenerFailure{
		Op:         "listen_tcp",
		Kind:       BindKindRuntimeStart,
		Key:        listenKey,
		Occurrence: 1,
		Err:        syscall.EACCES,
	})
	listenerFactory.SetExternallyOccupied(listenKey, false)

	if err := server.scanNonListeningTunnelRuntimeIssues(context.Background()); err != nil {
		t.Fatalf("scan runtime issues after tcp permission fault prep: %v", err)
	}

	expectedReason := buildTunnelListenerStartReason(protocol.ProtocolTCP, listenKey.IP, listenKey.Port, syscall.EACCES)
	failedState := waitForObservedState(t, server, listenerFactory, func(observed sharedtestsupport.ObservedState) bool {
		session := findObservedSession(observed.Server, repo.group.ID)
		tunnel := findObservedTunnel(observed.Server, repo.group.ID, 7)
		return session != nil &&
			tunnel != nil &&
			tunnel.RuntimeIssue == expectedReason &&
			tunnel.FinalStatus == "异常" &&
			countObservedListeners(observed.Server, repo.group.ID, 7) == 0 &&
			handleCountForPort(observed, listenKey) == 0
	})

	failedSession := requireObservedSession(t, failedState.Server, repo.group.ID)
	if failedSession.Pending != nil {
		t.Fatalf("expected no pending config after tcp permission fault, got %#v", failedSession.Pending)
	}
	if failedState.Server.GroupSlots[repo.group.ID] != failedSession.SessionID {
		t.Fatalf("expected session to stay active after tcp permission fault, got %#v", failedState.Server.GroupSlots)
	}
	if issues := server.TunnelRuntimeIssues(); issues[7] != expectedReason {
		t.Fatalf("expected tcp permission runtime issue to stay visible, got %#v", issues)
	}
	assertNoListenerHandles(t, failedState, listenKey)

	assertHeartbeatStillWorks(t, clientConn)
	assertNoExtraControlFrame(t, clientConn, "expected no config frame during same-snapshot tcp recovery")

	if err := server.scanNonListeningTunnelRuntimeIssues(context.Background()); err != nil {
		t.Fatalf("scan runtime issues after tcp permission fault clears: %v", err)
	}

	recoveredState := waitForObservedState(t, server, listenerFactory, func(observed sharedtestsupport.ObservedState) bool {
		session := findObservedSession(observed.Server, repo.group.ID)
		tunnel := findObservedTunnel(observed.Server, repo.group.ID, 7)
		return session != nil &&
			tunnel != nil &&
			strings.TrimSpace(tunnel.RuntimeIssue) == "" &&
			tunnel.FinalStatus == "启用" &&
			session.RecoveryMode == sharedtestsupport.RecoveryModeRunning &&
			countObservedListeners(observed.Server, repo.group.ID, 7) == 1 &&
			handleCountForPort(observed, listenKey) == 1
	})

	if issues := server.TunnelRuntimeIssues(); len(issues) != 0 {
		t.Fatalf("expected tcp permission runtime issue to clear after recovery, got %#v", issues)
	}
	assertSingleListenerHandles(t, recoveredState, listenKey)

	_ = clientConn.Close()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("server connection did not exit")
	}
}

func TestServerScenarioClosesPartiallyStartedTCPListenersAcrossRepeatedEMFILERecoveryChurn(t *testing.T) {
	tokenID, tokenHash := fixedTestToken()
	host := mustParseTestHost(t, "127.0.0.1")

	rangeStart := freeTCPPortRange(t, 4)
	listenKeys := []ListenKey{
		{Protocol: "tcp", IP: "127.0.0.1", Port: uint16(rangeStart)},
		{Protocol: "tcp", IP: "127.0.0.1", Port: uint16(rangeStart + 1)},
		{Protocol: "tcp", IP: "127.0.0.1", Port: uint16(rangeStart + 2)},
		{Protocol: "tcp", IP: "127.0.0.1", Port: uint16(rangeStart + 3)},
	}
	failingKey := listenKeys[len(listenKeys)-1]

	listenerFactory := NewScriptedListenerFactory()
	for range 3 {
		listenerFactory.AddFailure(ScriptedListenerFailure{
			Op:         "listen_tcp",
			Kind:       BindKindRuntimeStart,
			Key:        failingKey,
			Occurrence: 1,
			Err:        syscall.EMFILE,
		})
	}

	repo := &mutableRepository{
		group: GroupRuntime{
			ID:          1,
			Name:        "group-a",
			Enabled:     true,
			EffectiveIP: "127.0.0.1",
			TokenHash:   tokenHash,
			Snapshot: ConfigSnapshot{
				Version:       1,
				GeneratedAtMs: 100,
			},
		},
	}

	server := NewServer(
		Options{
			Repository:      repo,
			Network:         staticSnapshotReader{snapshot: localIPv4Snapshot("127.0.0.1")},
			ListenerFactory: listenerFactory,
		},
		slog.New(slog.NewTextHandler(io.Discard, nil)),
		"test-server",
	)

	clientConn, done, configFrame := authenticateServerSession(t, server, tokenID, tokenHash)
	defer clientConn.Close()

	initialPush, err := protocol.UnmarshalConfigPush(configFrame.Body)
	if err != nil {
		t.Fatalf("unmarshal initial config.push: %v", err)
	}
	if initialPush.ConfigVersion != 1 || len(initialPush.Tunnels) != 0 {
		t.Fatalf("unexpected initial empty config.push: %#v", initialPush)
	}
	writeConfigAck(t, clientConn, configFrame.RequestID, initialPush.ConfigVersion)
	waitForActiveGroupSession(t, server, repo.group.ID)
	active, ok := server.activeSession(repo.group.ID)
	if !ok || active == nil {
		t.Fatalf("active session for group %d not found", repo.group.ID)
	}
	waitForIdleConfig(t, active.session)

	repo.group.Snapshot = ConfigSnapshot{
		Version:       2,
		GeneratedAtMs: 200,
		Tunnels: []protocol.TunnelEntry{
			{
				TunnelID:    7,
				Protocol:    protocol.ProtocolTCP,
				TunnelFlags: protocol.TunnelFlagEnabled,
				RemoteStart: uint16(rangeStart),
				RemoteEnd:   uint16(rangeStart + 3),
				LocalHost:   host,
				LocalStart:  2200,
				LocalEnd:    2200,
			},
		},
	}

	refreshDone := make(chan struct{})
	go func() {
		defer close(refreshDone)
		server.RefreshGroup(repo.group.ID)
	}()

	fullFrame := readMessage(t, clientConn)
	if fullFrame.Type != protocol.TypeConfigPush {
		t.Fatalf("expected refreshed config.push, got %s", fullFrame.Type.String())
	}
	fullPush, err := protocol.UnmarshalConfigPush(fullFrame.Body)
	if err != nil {
		t.Fatalf("unmarshal refreshed config.push: %v", err)
	}
	if fullPush.ConfigVersion != 2 || len(fullPush.Tunnels) != 1 {
		t.Fatalf("unexpected refreshed config.push: %#v", fullPush)
	}
	writeConfigAck(t, clientConn, fullFrame.RequestID, fullPush.ConfigVersion)

	select {
	case <-refreshDone:
	case <-time.After(time.Second):
		t.Fatal("refresh did not complete")
	}

	expectedReason := buildTunnelListenerStartReason(protocol.ProtocolTCP, failingKey.IP, failingKey.Port, syscall.EMFILE)
	failedApplyState := waitForObservedState(t, server, listenerFactory, func(observed sharedtestsupport.ObservedState) bool {
		session := findObservedSession(observed.Server, repo.group.ID)
		tunnel := findObservedTunnel(observed.Server, repo.group.ID, 7)
		return session != nil &&
			tunnel != nil &&
			session.Pending == nil &&
			session.SnapshotVersion == 2 &&
			session.LastAckedConfigVersion == 2 &&
			tunnel.RuntimeIssue == expectedReason &&
			tunnel.FinalStatus == "异常" &&
			countObservedListeners(observed.Server, repo.group.ID, 7) == 0
	})

	assertNoListenerHandles(t, failedApplyState, listenKeys...)
	if issues := server.TunnelRuntimeIssues(); issues[7] != expectedReason {
		t.Fatalf("expected emfile runtime issue after initial full-config apply, got %#v", issues)
	}

	for attempt := 1; attempt <= 2; attempt++ {
		if err := server.scanNonListeningTunnelRuntimeIssues(context.Background()); err != nil {
			t.Fatalf("scan runtime issues for emfile churn attempt %d: %v", attempt, err)
		}

		failedScanState := waitForObservedState(t, server, listenerFactory, func(observed sharedtestsupport.ObservedState) bool {
			tunnel := findObservedTunnel(observed.Server, repo.group.ID, 7)
			return tunnel != nil &&
				tunnel.RuntimeIssue == expectedReason &&
				tunnel.FinalStatus == "异常" &&
				countObservedListeners(observed.Server, repo.group.ID, 7) == 0
		})
		assertNoListenerHandles(t, failedScanState, listenKeys...)
		assertNoExtraControlFrame(t, clientConn, "expected no config frame during emfile listener-recovery scan")
	}

	assertHeartbeatStillWorks(t, clientConn)

	if err := server.scanNonListeningTunnelRuntimeIssues(context.Background()); err != nil {
		t.Fatalf("scan runtime issues after emfile churn clears: %v", err)
	}

	recoveredState := waitForObservedState(t, server, listenerFactory, func(observed sharedtestsupport.ObservedState) bool {
		session := findObservedSession(observed.Server, repo.group.ID)
		tunnel := findObservedTunnel(observed.Server, repo.group.ID, 7)
		return session != nil &&
			tunnel != nil &&
			strings.TrimSpace(tunnel.RuntimeIssue) == "" &&
			tunnel.FinalStatus == "启用" &&
			session.RecoveryMode == sharedtestsupport.RecoveryModeRunning &&
			countObservedListeners(observed.Server, repo.group.ID, 7) == len(listenKeys)
	})

	assertSingleListenerHandles(t, recoveredState, listenKeys...)
	if issues := server.TunnelRuntimeIssues(); len(issues) != 0 {
		t.Fatalf("expected emfile runtime issue to clear after final recovery, got %#v", issues)
	}

	_ = clientConn.Close()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("server connection did not exit")
	}
}

func TestServerScenarioKeepsSessionAliveWhenUDPRecoveryBindFailsWithPermissionDenied(t *testing.T) {
	tokenID, tokenHash := fixedTestToken()
	host := mustParseTestHost(t, "127.0.0.1")

	listenKey := ListenKey{Protocol: "udp", IP: "127.0.0.1", Port: uint16(freeTCPPort(t))}
	listenerFactory := NewScriptedListenerFactory()
	listenerFactory.SetExternallyOccupied(listenKey, true)

	repo := &mutableRepository{
		group: GroupRuntime{
			ID:          1,
			Name:        "group-a",
			Enabled:     true,
			EffectiveIP: "127.0.0.1",
			TokenHash:   tokenHash,
			Snapshot: ConfigSnapshot{
				Version:       1,
				GeneratedAtMs: 100,
				Tunnels: []protocol.TunnelEntry{
					{
						TunnelID:    7,
						Protocol:    protocol.ProtocolUDP,
						TunnelFlags: protocol.TunnelFlagEnabled,
						RemoteStart: listenKey.Port,
						RemoteEnd:   listenKey.Port,
						LocalHost:   host,
						LocalStart:  2200,
						LocalEnd:    2200,
					},
				},
			},
		},
	}

	server := NewServer(
		Options{
			Repository:      repo,
			Network:         staticSnapshotReader{snapshot: localIPv4Snapshot("127.0.0.1")},
			ListenerFactory: listenerFactory,
		},
		slog.New(slog.NewTextHandler(io.Discard, nil)),
		"test-server",
	)

	clientConn, done, configFrame := authenticateServerSession(t, server, tokenID, tokenHash)
	defer clientConn.Close()

	configPush, err := protocol.UnmarshalConfigPush(configFrame.Body)
	if err != nil {
		t.Fatalf("unmarshal config.push: %v", err)
	}
	writeConfigAck(t, clientConn, configFrame.RequestID, configPush.ConfigVersion)

	blockedState := waitForObservedState(t, server, listenerFactory, func(observed sharedtestsupport.ObservedState) bool {
		session := findObservedSession(observed.Server, repo.group.ID)
		tunnel := findObservedTunnel(observed.Server, repo.group.ID, 7)
		return session != nil &&
			tunnel != nil &&
			strings.Contains(tunnel.RuntimeIssue, "端口冲突") &&
			tunnel.FinalStatus == "异常" &&
			countObservedListeners(observed.Server, repo.group.ID, 7) == 0 &&
			handleCountForPort(observed, listenKey) == 0
	})

	blockedSession := requireObservedSession(t, blockedState.Server, repo.group.ID)
	if blockedState.Server.GroupSlots[repo.group.ID] != blockedSession.SessionID {
		t.Fatalf("expected udp blocked session to keep group slot, got %#v", blockedState.Server.GroupSlots)
	}

	listenerFactory.AddFailure(ScriptedListenerFailure{
		Op:         "listen_udp",
		Kind:       BindKindRuntimeStart,
		Key:        listenKey,
		Occurrence: 1,
		Err:        syscall.EACCES,
	})
	listenerFactory.SetExternallyOccupied(listenKey, false)

	if err := server.scanNonListeningTunnelRuntimeIssues(context.Background()); err != nil {
		t.Fatalf("scan runtime issues after udp permission fault prep: %v", err)
	}

	expectedReason := buildTunnelListenerStartReason(protocol.ProtocolUDP, listenKey.IP, listenKey.Port, syscall.EACCES)
	failedState := waitForObservedState(t, server, listenerFactory, func(observed sharedtestsupport.ObservedState) bool {
		session := findObservedSession(observed.Server, repo.group.ID)
		tunnel := findObservedTunnel(observed.Server, repo.group.ID, 7)
		return session != nil &&
			tunnel != nil &&
			tunnel.RuntimeIssue == expectedReason &&
			tunnel.FinalStatus == "异常" &&
			countObservedListeners(observed.Server, repo.group.ID, 7) == 0 &&
			handleCountForPort(observed, listenKey) == 0
	})

	failedSession := requireObservedSession(t, failedState.Server, repo.group.ID)
	if failedSession.Pending != nil {
		t.Fatalf("expected no pending config after udp permission fault, got %#v", failedSession.Pending)
	}
	if failedState.Server.GroupSlots[repo.group.ID] != failedSession.SessionID {
		t.Fatalf("expected udp session to stay active after permission fault, got %#v", failedState.Server.GroupSlots)
	}
	if issues := server.TunnelRuntimeIssues(); issues[7] != expectedReason {
		t.Fatalf("expected udp permission runtime issue to stay visible, got %#v", issues)
	}
	assertNoListenerHandles(t, failedState, listenKey)

	assertHeartbeatStillWorks(t, clientConn)
	assertNoExtraControlFrame(t, clientConn, "expected no config frame during same-snapshot udp recovery")

	if err := server.scanNonListeningTunnelRuntimeIssues(context.Background()); err != nil {
		t.Fatalf("scan runtime issues after udp permission fault clears: %v", err)
	}

	recoveredState := waitForObservedState(t, server, listenerFactory, func(observed sharedtestsupport.ObservedState) bool {
		session := findObservedSession(observed.Server, repo.group.ID)
		tunnel := findObservedTunnel(observed.Server, repo.group.ID, 7)
		return session != nil &&
			tunnel != nil &&
			strings.TrimSpace(tunnel.RuntimeIssue) == "" &&
			tunnel.FinalStatus == "启用" &&
			session.RecoveryMode == sharedtestsupport.RecoveryModeRunning &&
			countObservedListeners(observed.Server, repo.group.ID, 7) == 1 &&
			handleCountForPort(observed, listenKey) == 1
	})

	assertSingleListenerHandles(t, recoveredState, listenKey)
	if issues := server.TunnelRuntimeIssues(); len(issues) != 0 {
		t.Fatalf("expected udp permission runtime issue to clear after recovery, got %#v", issues)
	}

	_ = clientConn.Close()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("server connection did not exit")
	}
}

func TestServerScenarioServesHeartbeatWhileEmptyAndFullRecoveryConfigsArePending(t *testing.T) {
	tokenID, tokenHash := fixedTestToken()
	host := mustParseTestHost(t, "127.0.0.1")

	port := uint16(freeTCPPort(t))
	initialListenKey := ListenKey{Protocol: "tcp", IP: "127.0.0.1", Port: port}
	recoveredListenKey := ListenKey{Protocol: "tcp", IP: "127.0.0.2", Port: port}
	listenerFactory := NewScriptedListenerFactory()

	repo := &mutableRepository{
		group: GroupRuntime{
			ID:          1,
			Name:        "group-a",
			Enabled:     true,
			EffectiveIP: "127.0.0.1",
			TokenHash:   tokenHash,
			Snapshot: ConfigSnapshot{
				Version:       1,
				GeneratedAtMs: 100,
				Tunnels: []protocol.TunnelEntry{
					{
						TunnelID:    7,
						Protocol:    protocol.ProtocolTCP,
						TunnelFlags: protocol.TunnelFlagEnabled,
						RemoteStart: port,
						RemoteEnd:   port,
						LocalHost:   host,
						LocalStart:  2200,
						LocalEnd:    2200,
					},
				},
			},
		},
	}
	network := &mutableSnapshotReader{
		snapshot: system.Snapshot{
			AvailableIPs: []system.IPAddress{
				{Addr: "127.0.0.1", Family: system.FamilyIPv4},
			},
		},
	}

	server := NewServer(
		Options{
			Repository:      repo,
			Network:         network,
			ListenerFactory: listenerFactory,
		},
		slog.New(slog.NewTextHandler(io.Discard, nil)),
		"test-server",
	)

	clientConn, done, configFrame := authenticateServerSession(t, server, tokenID, tokenHash)
	defer clientConn.Close()

	initialPush, err := protocol.UnmarshalConfigPush(configFrame.Body)
	if err != nil {
		t.Fatalf("unmarshal initial config.push: %v", err)
	}
	writeConfigAck(t, clientConn, configFrame.RequestID, initialPush.ConfigVersion)

	runningState := waitForObservedState(t, server, listenerFactory, func(observed sharedtestsupport.ObservedState) bool {
		session := findObservedSession(observed.Server, repo.group.ID)
		tunnel := findObservedTunnel(observed.Server, repo.group.ID, 7)
		return session != nil &&
			tunnel != nil &&
			session.Pending == nil &&
			session.RecoveryMode == sharedtestsupport.RecoveryModeRunning &&
			countObservedListeners(observed.Server, repo.group.ID, 7) == 1 &&
			handleCountForPort(observed, initialListenKey) == 1 &&
			handleCountForPort(observed, recoveredListenKey) == 0 &&
			strings.TrimSpace(tunnel.RuntimeIssue) == ""
	})
	if runningState.Server.GroupSlots[repo.group.ID] == 0 {
		t.Fatalf("expected running session to occupy group slot, got %#v", runningState.Server.GroupSlots)
	}

	repo.group.EffectiveIP = "127.0.0.2"
	repo.group.Snapshot = ConfigSnapshot{
		Version:       2,
		GeneratedAtMs: 200,
		Tunnels: []protocol.TunnelEntry{
			{
				TunnelID:    7,
				Protocol:    protocol.ProtocolTCP,
				TunnelFlags: protocol.TunnelFlagEnabled,
				RemoteStart: port,
				RemoteEnd:   port,
				LocalHost:   host,
				LocalStart:  2200,
				LocalEnd:    2200,
			},
		},
	}

	refreshDone := make(chan struct{})
	go func() {
		defer close(refreshDone)
		server.RefreshGroup(repo.group.ID)
	}()

	emptyFrame := readMessage(t, clientConn)
	if emptyFrame.Type != protocol.TypeConfigPush {
		t.Fatalf("expected empty config.push, got %s", emptyFrame.Type.String())
	}
	emptyPush, err := protocol.UnmarshalConfigPush(emptyFrame.Body)
	if err != nil {
		t.Fatalf("unmarshal empty config.push: %v", err)
	}
	if emptyPush.ConfigVersion != 2 || len(emptyPush.Tunnels) != 0 {
		t.Fatalf("unexpected empty config.push: %#v", emptyPush)
	}

	select {
	case <-refreshDone:
	case <-time.After(time.Second):
		t.Fatal("empty-config refresh did not complete")
	}

	pendingEmptyState := waitForObservedState(t, server, listenerFactory, func(observed sharedtestsupport.ObservedState) bool {
		session := findObservedSession(observed.Server, repo.group.ID)
		tunnel := findObservedTunnel(observed.Server, repo.group.ID, 7)
		return session != nil &&
			tunnel != nil &&
			session.Pending != nil &&
			session.Pending.Version == 2 &&
			session.Pending.TunnelCount == 0 &&
			session.RecoveryMode == sharedtestsupport.RecoveryModePendingEmptyConfig &&
			countObservedListeners(observed.Server, repo.group.ID, 7) == 0 &&
			handleCountForPort(observed, initialListenKey) == 0 &&
			handleCountForPort(observed, recoveredListenKey) == 0 &&
			strings.Contains(tunnel.RuntimeIssue, "当前不存在于本机")
	})
	assertNoListenerHandles(t, pendingEmptyState, initialListenKey, recoveredListenKey)

	assertHeartbeatStillWorks(t, clientConn)
	assertNoExtraControlFrame(t, clientConn, "expected no extra frame while empty config is pending")

	pendingEmptyAfterHeartbeat := waitForObservedState(t, server, listenerFactory, func(observed sharedtestsupport.ObservedState) bool {
		session := findObservedSession(observed.Server, repo.group.ID)
		return session != nil &&
			session.Pending != nil &&
			session.Pending.Version == 2 &&
			session.Pending.TunnelCount == 0 &&
			session.RecoveryMode == sharedtestsupport.RecoveryModePendingEmptyConfig &&
			countObservedListeners(observed.Server, repo.group.ID, 7) == 0
	})
	if pendingEmptyAfterHeartbeat.Server.GroupSlots[repo.group.ID] == 0 {
		t.Fatalf("expected group slot to remain occupied while empty config is pending, got %#v", pendingEmptyAfterHeartbeat.Server.GroupSlots)
	}

	writeConfigAck(t, clientConn, emptyFrame.RequestID, emptyPush.ConfigVersion)

	emptyState := waitForObservedState(t, server, listenerFactory, func(observed sharedtestsupport.ObservedState) bool {
		session := findObservedSession(observed.Server, repo.group.ID)
		tunnel := findObservedTunnel(observed.Server, repo.group.ID, 7)
		return session != nil &&
			tunnel != nil &&
			session.Pending == nil &&
			session.RecoveryMode == sharedtestsupport.RecoveryModeEmptyConfig &&
			session.SnapshotVersion == 2 &&
			session.LastAckedConfigVersion == 2 &&
			countObservedListeners(observed.Server, repo.group.ID, 7) == 0 &&
			strings.Contains(tunnel.RuntimeIssue, "当前不存在于本机")
	})
	if emptyState.Server.GroupSlots[repo.group.ID] == 0 {
		t.Fatalf("expected empty-config session to keep group slot, got %#v", emptyState.Server.GroupSlots)
	}

	network.setSnapshot(localIPv4Snapshot("127.0.0.1", "127.0.0.2"))

	scanDone := make(chan error, 1)
	go func() {
		scanDone <- server.scanNonListeningTunnelRuntimeIssues(context.Background())
	}()

	recoveredFrame := readMessage(t, clientConn)
	if recoveredFrame.Type != protocol.TypeConfigPush {
		t.Fatalf("expected recovered config.push, got %s", recoveredFrame.Type.String())
	}
	recoveredPush, err := protocol.UnmarshalConfigPush(recoveredFrame.Body)
	if err != nil {
		t.Fatalf("unmarshal recovered config.push: %v", err)
	}
	if recoveredPush.ConfigVersion != 2 || len(recoveredPush.Tunnels) != 1 {
		t.Fatalf("unexpected recovered config.push: %#v", recoveredPush)
	}

	pendingFullState := waitForObservedState(t, server, listenerFactory, func(observed sharedtestsupport.ObservedState) bool {
		session := findObservedSession(observed.Server, repo.group.ID)
		tunnel := findObservedTunnel(observed.Server, repo.group.ID, 7)
		return session != nil &&
			tunnel != nil &&
			session.Pending != nil &&
			session.Pending.Version == 2 &&
			session.Pending.TunnelCount == 1 &&
			session.RecoveryMode == sharedtestsupport.RecoveryModePendingFullConfig &&
			countObservedListeners(observed.Server, repo.group.ID, 7) == 0 &&
			handleCountForPort(observed, initialListenKey) == 0 &&
			handleCountForPort(observed, recoveredListenKey) == 0 &&
			strings.Contains(tunnel.RuntimeIssue, "当前不存在于本机")
	})
	assertNoListenerHandles(t, pendingFullState, initialListenKey, recoveredListenKey)

	assertHeartbeatStillWorks(t, clientConn)
	assertNoExtraControlFrame(t, clientConn, "expected no extra frame while full config is pending")

	pendingFullAfterHeartbeat := waitForObservedState(t, server, listenerFactory, func(observed sharedtestsupport.ObservedState) bool {
		session := findObservedSession(observed.Server, repo.group.ID)
		return session != nil &&
			session.Pending != nil &&
			session.Pending.Version == 2 &&
			session.Pending.TunnelCount == 1 &&
			session.RecoveryMode == sharedtestsupport.RecoveryModePendingFullConfig &&
			countObservedListeners(observed.Server, repo.group.ID, 7) == 0
	})
	if pendingFullAfterHeartbeat.Server.GroupSlots[repo.group.ID] == 0 {
		t.Fatalf("expected group slot to remain occupied while full config is pending, got %#v", pendingFullAfterHeartbeat.Server.GroupSlots)
	}

	writeConfigAck(t, clientConn, recoveredFrame.RequestID, recoveredPush.ConfigVersion)

	select {
	case err := <-scanDone:
		if err != nil {
			t.Fatalf("scan runtime issues for recovered full config: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("recovery scan did not complete")
	}

	finalState := waitForObservedState(t, server, listenerFactory, func(observed sharedtestsupport.ObservedState) bool {
		session := findObservedSession(observed.Server, repo.group.ID)
		tunnel := findObservedTunnel(observed.Server, repo.group.ID, 7)
		return session != nil &&
			tunnel != nil &&
			session.Pending == nil &&
			session.RecoveryMode == sharedtestsupport.RecoveryModeRunning &&
			session.SnapshotVersion == 2 &&
			session.LastAckedConfigVersion == 2 &&
			countObservedListeners(observed.Server, repo.group.ID, 7) == 1 &&
			handleCountForPort(observed, initialListenKey) == 0 &&
			handleCountForPort(observed, recoveredListenKey) == 1 &&
			strings.TrimSpace(tunnel.RuntimeIssue) == "" &&
			tunnel.FinalStatus == "启用"
	})
	assertNoListenerHandles(t, finalState, initialListenKey)
	assertSingleListenerHandles(t, finalState, recoveredListenKey)
	if issues := server.TunnelRuntimeIssues(); len(issues) != 0 {
		t.Fatalf("expected runtime issues to clear after pending-config heartbeat scenario, got %#v", issues)
	}

	_ = clientConn.Close()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("server connection did not exit")
	}
}

func fixedTestToken() ([16]byte, [32]byte) {
	var tokenID [16]byte
	copy(tokenID[:], []byte("token-id-1234567"))

	var tokenSecret [32]byte
	copy(tokenSecret[:], []byte("0123456789abcdef0123456789abcdef"))
	return tokenID, sha256.Sum256(tokenSecret[:])
}

func mustParseTestHost(t *testing.T, value string) protocol.Host {
	t.Helper()

	host, err := protocol.ParseHost(value)
	if err != nil {
		t.Fatalf("parse host %q: %v", value, err)
	}
	return host
}

func assertNoListenerHandles(t *testing.T, observed sharedtestsupport.ObservedState, keys ...ListenKey) {
	t.Helper()

	for _, key := range keys {
		if count := handleCountForPort(observed, key); count != 0 {
			t.Fatalf("expected no listener handles for %#v, got %#v", key, observed.Listeners)
		}
	}
}

func assertSingleListenerHandles(t *testing.T, observed sharedtestsupport.ObservedState, keys ...ListenKey) {
	t.Helper()

	for _, key := range keys {
		if count := handleCountForPort(observed, key); count != 1 {
			t.Fatalf("expected exactly one listener handle for %#v, got %#v", key, observed.Listeners)
		}
	}
}

func assertNoExtraControlFrame(t *testing.T, conn *connWithRemoteAddr, message string) {
	t.Helper()

	if frame, err := readMessageWithin(conn, 200*time.Millisecond); err == nil {
		t.Fatalf("%s, got %s", message, frame.Type.String())
	} else if !isTimeoutError(err) {
		t.Fatalf("expected idle control connection, got %v", err)
	}
}
