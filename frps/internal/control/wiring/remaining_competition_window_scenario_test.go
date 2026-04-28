//go:build testhooks

package wiring

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/zightch/frp/frps/internal/testhooks"
	"github.com/zightch/frp/frps/pkg/protocol"
	sharedtestsupport "github.com/zightch/frp/frps/pkg/testsupport"
)

func TestServerScenarioSkipsOverlappingRuntimeScanRoundDuringRecoveryPush(t *testing.T) {
	controller := testhooks.NewController()
	restoreHooks := testhooks.Install(controller)
	defer restoreHooks()

	tokenID, tokenHash := fixedTestToken()
	host := mustParseTestHost(t, "127.0.0.1")

	listenKey := ListenKey{Protocol: "tcp", IP: "127.0.0.1", Port: uint16(freeTCPPort(t))}
	repo := &scriptedRuntimeRepository{
		group: GroupRuntime{
			ID:               1,
			Name:             "group-a",
			Enabled:          true,
			EffectiveIP:      "127.0.0.1",
			ClientSecretHash: tokenHash,
			Snapshot: ConfigSnapshot{
				Version:       1,
				GeneratedAtMs: 100,
			},
		},
		listStarted: make(chan struct{}, 2),
		allowList:   make(chan struct{}),
	}
	listenerFactory := NewScriptedListenerFactory()
	server := NewServer(
		Options{
			Repository:      repo,
			Network:         staticSnapshotReader{snapshot: localIPv4Snapshot("127.0.0.1")},
			ListenerFactory: listenerFactory,
		},
		slog.New(slog.NewTextHandler(io.Discard, nil)),
		"test-server",
	)

	clientConn, done, initialFrame := authenticateServerSession(t, server, tokenID, tokenHash)
	defer clientConn.Close()

	initialPush, err := protocol.UnmarshalConfigPush(initialFrame.Body)
	if err != nil {
		t.Fatalf("unmarshal initial config.push: %v", err)
	}
	if initialPush.ConfigVersion != 1 || len(initialPush.Tunnels) != 0 {
		t.Fatalf("unexpected initial empty config.push: %#v", initialPush)
	}
	writeConfigAck(t, clientConn, initialFrame.RequestID, initialPush.ConfigVersion)
	waitForActiveGroupSession(t, server, repo.group.ID)

	active, ok := server.activeSession(repo.group.ID)
	if !ok || active == nil || active.session == nil {
		t.Fatalf("active session for group %d not found", repo.group.ID)
	}
	waitForIdleConfig(t, active.session)

	repo.SetGroup(GroupRuntime{
		ID:               1,
		Name:             "group-a",
		Enabled:          true,
		EffectiveIP:      "127.0.0.1",
		ClientSecretHash: tokenHash,
		Snapshot: ConfigSnapshot{
			Version:       2,
			GeneratedAtMs: 200,
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
	})

	firstScanDone := make(chan error, 1)
	go func() {
		firstScanDone <- server.scanNonListeningTunnelRuntimeIssues(context.Background())
	}()

	select {
	case <-repo.listStarted:
	case <-time.After(time.Second):
		t.Fatal("first scan did not reach repository list")
	}

	secondScanDone := make(chan error, 1)
	go func() {
		secondScanDone <- server.scanNonListeningTunnelRuntimeIssues(context.Background())
	}()

	waitForTestHook(t, controller, "runtime.scan.skip_overlap", 1)
	select {
	case <-repo.listStarted:
		t.Fatal("expected overlapping scan to skip before second repository list")
	default:
	}

	select {
	case err := <-secondScanDone:
		if err != nil {
			t.Fatalf("overlapping scan returned error: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("overlapping scan did not return after being skipped")
	}

	repo.ReleaseList()

	recoveryFrame := readMessage(t, clientConn)
	if recoveryFrame.Type != protocol.TypeConfigPush {
		t.Fatalf("expected recovery config.push, got %s", recoveryFrame.Type.String())
	}
	recoveryPush, err := protocol.UnmarshalConfigPush(recoveryFrame.Body)
	if err != nil {
		t.Fatalf("unmarshal recovery config.push: %v", err)
	}
	if recoveryPush.ConfigVersion != 2 || len(recoveryPush.Tunnels) != 1 || recoveryPush.Tunnels[0].TunnelID != 7 {
		t.Fatalf("unexpected recovery config.push: %#v", recoveryPush)
	}

	pendingState := waitForObservedState(t, server, listenerFactory, func(observed sharedtestsupport.ObservedState) bool {
		session := findObservedSession(observed.Server, repo.group.ID)
		return session != nil &&
			session.Pending != nil &&
			session.Pending.Version == 2 &&
			session.Pending.TunnelCount == 1 &&
			session.RecoveryMode == sharedtestsupport.RecoveryModePendingFullConfig &&
			countObservedListeners(observed.Server, repo.group.ID, 7) == 0 &&
			handleCountForPort(observed, listenKey) == 0
	})
	if pendingState.Server.GroupSlots[repo.group.ID] == 0 {
		t.Fatalf("expected group slot to stay occupied while recovery push is pending, got %#v", pendingState.Server.GroupSlots)
	}

	select {
	case err := <-firstScanDone:
		if err != nil {
			t.Fatalf("first scan failed: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("first scan did not complete")
	}

	writeConfigAck(t, clientConn, recoveryFrame.RequestID, recoveryPush.ConfigVersion)

	finalState := waitForObservedState(t, server, listenerFactory, func(observed sharedtestsupport.ObservedState) bool {
		session := findObservedSession(observed.Server, repo.group.ID)
		return session != nil &&
			session.Pending == nil &&
			session.SnapshotVersion == 2 &&
			session.LastAckedConfigVersion == 2 &&
			session.RecoveryMode == sharedtestsupport.RecoveryModeRunning &&
			countObservedListeners(observed.Server, repo.group.ID, 7) == 1 &&
			handleCountForPort(observed, listenKey) == 1
	})
	finalSession := requireObservedSession(t, finalState.Server, repo.group.ID)
	if finalState.Server.GroupSlots[repo.group.ID] != finalSession.SessionID {
		t.Fatalf("expected active session to keep group slot after overlapping scan skip, got %#v", finalState.Server.GroupSlots)
	}

	if hits := controller.Hits("control.config_push.before_write"); len(hits) != 2 {
		t.Fatalf("expected exactly one recovery config.push in addition to startup, got %#v", hits)
	}
	if frame, err := readMessageWithin(clientConn, 200*time.Millisecond); err == nil {
		t.Fatalf("expected no duplicate frame after overlapping scan skip, got %s", frame.Type.String())
	} else if !isTimeoutError(err) {
		t.Fatalf("expected idle connection after overlapping scan skip, got %v", err)
	}

	assertHeartbeatStillWorks(t, clientConn)

	_ = clientConn.Close()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("server connection did not exit")
	}
}

func TestServerScenarioScanReusesFreshNetworkSnapshotBeforeRecoveryPush(t *testing.T) {
	tokenID, tokenHash := fixedTestToken()
	host := mustParseTestHost(t, "127.0.0.1")

	initialListenKey := ListenKey{Protocol: "tcp", IP: "127.0.0.1", Port: uint16(freeTCPPort(t))}
	recoveredListenKey := ListenKey{Protocol: "tcp", IP: "127.0.0.2", Port: uint16(freeTCPPortExcept(t, int(initialListenKey.Port)))}
	network := &mutableSnapshotReader{snapshot: localIPv4Snapshot("127.0.0.1")}
	repo := &mutableRepository{
		group: GroupRuntime{
			ID:               1,
			Name:             "group-a",
			Enabled:          true,
			EffectiveIP:      "127.0.0.1",
			ClientSecretHash: tokenHash,
			Snapshot: ConfigSnapshot{
				Version:       1,
				GeneratedAtMs: 100,
				Tunnels: []protocol.TunnelEntry{
					{
						TunnelID:    7,
						Protocol:    protocol.ProtocolTCP,
						TunnelFlags: protocol.TunnelFlagEnabled,
						RemoteStart: initialListenKey.Port,
						RemoteEnd:   initialListenKey.Port,
						LocalHost:   host,
						LocalStart:  2200,
						LocalEnd:    2200,
					},
				},
			},
		},
	}
	listenerFactory := NewScriptedListenerFactory()
	server := NewServer(
		Options{
			Repository:      repo,
			Network:         network,
			ListenerFactory: listenerFactory,
		},
		slog.New(slog.NewTextHandler(io.Discard, nil)),
		"test-server",
	)

	clientConn, done, initialFrame := authenticateServerSession(t, server, tokenID, tokenHash)
	defer clientConn.Close()

	initialPush, err := protocol.UnmarshalConfigPush(initialFrame.Body)
	if err != nil {
		t.Fatalf("unmarshal initial config.push: %v", err)
	}
	writeConfigAck(t, clientConn, initialFrame.RequestID, initialPush.ConfigVersion)

	runningState := waitForObservedState(t, server, listenerFactory, func(observed sharedtestsupport.ObservedState) bool {
		session := findObservedSession(observed.Server, repo.group.ID)
		return session != nil &&
			session.Pending == nil &&
			session.RecoveryMode == sharedtestsupport.RecoveryModeRunning &&
			countObservedListeners(observed.Server, repo.group.ID, 7) == 1 &&
			handleCountForPort(observed, initialListenKey) == 1
	})
	if runningState.Server.GroupSlots[repo.group.ID] == 0 {
		t.Fatalf("expected running session to occupy group slot, got %#v", runningState.Server.GroupSlots)
	}

	repo.group = GroupRuntime{
		ID:               1,
		Name:             "group-a",
		Enabled:          true,
		EffectiveIP:      "127.0.0.2",
		ClientSecretHash: tokenHash,
		Snapshot: ConfigSnapshot{
			Version:       2,
			GeneratedAtMs: 200,
			Tunnels: []protocol.TunnelEntry{
				{
					TunnelID:    7,
					Protocol:    protocol.ProtocolTCP,
					TunnelFlags: protocol.TunnelFlagEnabled,
					RemoteStart: recoveredListenKey.Port,
					RemoteEnd:   recoveredListenKey.Port,
					LocalHost:   host,
					LocalStart:  2200,
					LocalEnd:    2200,
				},
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
		t.Fatalf("expected empty config.push after effective_ip becomes unavailable, got %s", emptyFrame.Type.String())
	}
	emptyPush, err := protocol.UnmarshalConfigPush(emptyFrame.Body)
	if err != nil {
		t.Fatalf("unmarshal empty config.push: %v", err)
	}
	if emptyPush.ConfigVersion != 2 || len(emptyPush.Tunnels) != 0 {
		t.Fatalf("unexpected empty config.push: %#v", emptyPush)
	}
	writeConfigAck(t, clientConn, emptyFrame.RequestID, emptyPush.ConfigVersion)

	select {
	case <-refreshDone:
	case <-time.After(time.Second):
		t.Fatal("effective_ip refresh did not complete")
	}

	emptyState := waitForObservedState(t, server, listenerFactory, func(observed sharedtestsupport.ObservedState) bool {
		session := findObservedSession(observed.Server, repo.group.ID)
		tunnel := findObservedTunnel(observed.Server, repo.group.ID, 7)
		return session != nil &&
			session.Pending == nil &&
			session.RecoveryMode == sharedtestsupport.RecoveryModeEmptyConfig &&
			session.SnapshotVersion == 2 &&
			session.SnapshotTunnelCount == 0 &&
			countObservedListeners(observed.Server, repo.group.ID, 7) == 0 &&
			handleCountForPort(observed, initialListenKey) == 0 &&
			handleCountForPort(observed, recoveredListenKey) == 0 &&
			tunnel != nil &&
			strings.Contains(tunnel.RuntimeIssue, "当前不存在于本机") &&
			tunnel.FinalStatus == "异常"
	})
	if emptyState.Server.GroupSlots[repo.group.ID] == 0 {
		t.Fatalf("expected empty-config session to keep group slot, got %#v", emptyState.Server.GroupSlots)
	}

	controller := testhooks.NewController()
	controller.AddBarrier("runtime.scan.before_group_recover", 1)
	restoreHooks := testhooks.Install(controller)
	defer restoreHooks()

	scanDone := make(chan error, 1)
	go func() {
		scanDone <- server.scanNonListeningTunnelRuntimeIssues(context.Background())
	}()

	waitForTestHook(t, controller, "runtime.scan.before_group_recover", 1)
	network.setSnapshot(localIPv4Snapshot("127.0.0.1", "127.0.0.2"))
	if !controller.Release("runtime.scan.before_group_recover", 1) {
		t.Fatal("release runtime.scan.before_group_recover barrier failed")
	}

	recoveryFrame := readMessage(t, clientConn)
	if recoveryFrame.Type != protocol.TypeConfigPush {
		t.Fatalf("expected recovered full config.push, got %s", recoveryFrame.Type.String())
	}
	recoveryPush, err := protocol.UnmarshalConfigPush(recoveryFrame.Body)
	if err != nil {
		t.Fatalf("unmarshal recovered full config.push: %v", err)
	}
	if recoveryPush.ConfigVersion != 2 || len(recoveryPush.Tunnels) != 1 || recoveryPush.Tunnels[0].RemoteStart != recoveredListenKey.Port {
		t.Fatalf("unexpected recovered full config.push: %#v", recoveryPush)
	}

	pendingState := waitForObservedState(t, server, listenerFactory, func(observed sharedtestsupport.ObservedState) bool {
		session := findObservedSession(observed.Server, repo.group.ID)
		tunnel := findObservedTunnel(observed.Server, repo.group.ID, 7)
		return session != nil &&
			session.Pending != nil &&
			session.Pending.Version == 2 &&
			session.Pending.TunnelCount == 1 &&
			session.Pending.EffectiveIP == "127.0.0.2" &&
			session.RecoveryMode == sharedtestsupport.RecoveryModePendingFullConfig &&
			countObservedListeners(observed.Server, repo.group.ID, 7) == 0 &&
			handleCountForPort(observed, initialListenKey) == 0 &&
			handleCountForPort(observed, recoveredListenKey) == 0 &&
			tunnel != nil &&
			strings.Contains(tunnel.RuntimeIssue, "当前不存在于本机") &&
			tunnel.FinalStatus == "异常"
	})
	if pendingState.Server.GroupSlots[repo.group.ID] == 0 {
		t.Fatalf("expected group slot to remain occupied while mid-round recovery push is pending, got %#v", pendingState.Server.GroupSlots)
	}

	select {
	case err := <-scanDone:
		if err != nil {
			t.Fatalf("scan after network snapshot refresh failed: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("scan after network snapshot refresh did not complete")
	}

	writeConfigAck(t, clientConn, recoveryFrame.RequestID, recoveryPush.ConfigVersion)

	finalState := waitForObservedState(t, server, listenerFactory, func(observed sharedtestsupport.ObservedState) bool {
		session := findObservedSession(observed.Server, repo.group.ID)
		tunnel := findObservedTunnel(observed.Server, repo.group.ID, 7)
		return session != nil &&
			session.Pending == nil &&
			session.RecoveryMode == sharedtestsupport.RecoveryModeRunning &&
			session.SnapshotVersion == 2 &&
			session.LastAckedConfigVersion == 2 &&
			countObservedListeners(observed.Server, repo.group.ID, 7) == 1 &&
			handleCountForPort(observed, initialListenKey) == 0 &&
			handleCountForPort(observed, recoveredListenKey) == 1 &&
			tunnel != nil &&
			strings.TrimSpace(tunnel.RuntimeIssue) == "" &&
			tunnel.FinalStatus == "启用"
	})
	if len(finalState.Server.Listeners) != 1 {
		t.Fatalf("expected exactly one listener after mid-round network recovery, got %#v", finalState.Server.Listeners)
	}

	assertHeartbeatStillWorks(t, clientConn)

	_ = clientConn.Close()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("server connection did not exit")
	}
}

func TestServerScenarioShutdownDuringInitialRuntimeScanDoesNotOpenControlListenerAfterRelease(t *testing.T) {
	host := mustParseTestHost(t, "127.0.0.1")

	controlPort := freeTCPPort(t)
	remotePort := freeTCPPortExcept(t, controlPort)
	repo := &blockingRepository{
		group: GroupRuntime{
			ID:          1,
			Name:        "group-a",
			Enabled:     true,
			EffectiveIP: "127.0.0.1",
			Snapshot: ConfigSnapshot{
				Tunnels: []protocol.TunnelEntry{
					{
						TunnelID:    7,
						Protocol:    protocol.ProtocolTCP,
						TunnelFlags: protocol.TunnelFlagEnabled,
						RemoteStart: uint16(remotePort),
						RemoteEnd:   uint16(remotePort),
						LocalHost:   host,
						LocalStart:  22,
						LocalEnd:    22,
					},
				},
			},
		},
		loadAllStarted: make(chan struct{}, 1),
		allowLoadAll:   make(chan struct{}),
	}

	server := NewServer(
		Options{
			Addr:            net.JoinHostPort("127.0.0.1", strconv.Itoa(controlPort)),
			Repository:      repo,
			Network:         staticSnapshotReader{snapshot: localIPv4Snapshot("127.0.0.1")},
			RuntimeScanPoll: time.Hour,
		},
		slog.New(slog.NewTextHandler(io.Discard, nil)),
		"test-server",
	)

	serveDone := make(chan error, 1)
	go func() {
		serveDone <- server.ListenAndServe(context.Background())
	}()

	select {
	case <-repo.loadAllStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("initial runtime scan did not start")
	}

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), time.Second)
	defer shutdownCancel()
	shutdownDone := make(chan error, 1)
	go func() {
		shutdownDone <- server.Shutdown(shutdownCtx)
	}()

	select {
	case err := <-shutdownDone:
		if err != nil {
			t.Fatalf("shutdown during initial scan failed: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("shutdown during initial scan did not return")
	}

	close(repo.allowLoadAll)

	select {
	case err := <-serveDone:
		if err != nil {
			t.Fatalf("listen and serve returned error after shutdown during initial scan: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("listen and serve did not exit after shutdown during initial scan")
	}

	if conn, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(controlPort)), 100*time.Millisecond); err == nil {
		_ = conn.Close()
		t.Fatal("expected control listener to stay closed after shutdown during initial scan")
	}

	observed := server.ObserveState()
	if observed.ControlListenerOpen || observed.LoginGateOpen {
		t.Fatalf("expected control listener and login gate to stay closed after shutdown, got %#v", observed)
	}
}

func TestServerScenarioShutdownWhileScanRecoveryBindBlockedLeavesNoListenerLeak(t *testing.T) {
	tokenID, tokenHash := fixedTestToken()
	host := mustParseTestHost(t, "127.0.0.1")

	listenKey := ListenKey{Protocol: "tcp", IP: "127.0.0.1", Port: uint16(freeTCPPort(t))}
	listenerFactory := NewScriptedListenerFactory()
	listenerFactory.SetExternallyOccupied(listenKey, true)
	repo := &scriptedRuntimeRepository{
		group: GroupRuntime{
			ID:               1,
			Name:             "group-a",
			Enabled:          true,
			EffectiveIP:      "127.0.0.1",
			ClientSecretHash: tokenHash,
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
		tunnel := findObservedTunnel(observed.Server, repo.group.ID, 7)
		return tunnel != nil &&
			strings.Contains(tunnel.RuntimeIssue, "端口冲突") &&
			tunnel.FinalStatus == "异常" &&
			countObservedListeners(observed.Server, repo.group.ID, 7) == 0 &&
			handleCountForPort(observed, listenKey) == 0
	})
	if blockedState.Server.GroupSlots[repo.group.ID] == 0 {
		t.Fatalf("expected blocked session to keep group slot before shutdown, got %#v", blockedState.Server.GroupSlots)
	}

	controller := testhooks.NewController()
	controller.AddBarrier("control.listener.before_bind", 1)
	controller.AddBarrier("control.listener.before_bind", 2)
	restoreHooks := testhooks.Install(controller)
	defer restoreHooks()

	listenerFactory.SetExternallyOccupied(listenKey, false)
	scanDone := make(chan error, 1)
	go func() {
		scanDone <- server.scanNonListeningTunnelRuntimeIssues(context.Background())
	}()

	probeHit := waitForTestHookHit(t, controller, "control.listener.before_bind", 1)
	if got := probeHit.Fields["kind"]; got != "runtime_probe" {
		t.Fatalf("expected first recovery bind to be runtime_probe, got %#v", probeHit.Fields)
	}
	if !controller.Release("control.listener.before_bind", 1) {
		t.Fatal("release runtime_probe barrier failed")
	}

	startHit := waitForTestHookHit(t, controller, "control.listener.before_bind", 2)
	if got := startHit.Fields["kind"]; got != "runtime_start" {
		t.Fatalf("expected second recovery bind to be runtime_start, got %#v", startHit.Fields)
	}

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer shutdownCancel()
	shutdownDone := make(chan error, 1)
	go func() {
		shutdownDone <- server.Shutdown(shutdownCtx)
	}()

	if !controller.Release("control.listener.before_bind", 2) {
		t.Fatal("release runtime_start barrier failed")
	}

	select {
	case err := <-scanDone:
		if err != nil {
			t.Fatalf("scan recovery during shutdown failed: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("scan recovery during shutdown did not complete")
	}

	select {
	case err := <-shutdownDone:
		if err != nil && !errors.Is(err, context.Canceled) {
			t.Fatalf("shutdown during scan recovery failed: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("shutdown during scan recovery did not complete")
	}

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("session did not exit after shutdown raced with scan recovery bind")
	}

	finalState := waitForObservedState(t, server, listenerFactory, func(observed sharedtestsupport.ObservedState) bool {
		return len(observed.Server.Sessions) == 0 &&
			len(observed.Server.GroupSlots) == 0 &&
			countObservedListeners(observed.Server, repo.group.ID, 7) == 0 &&
			handleCountForPort(observed, listenKey) == 0
	})
	if len(finalState.Server.Listeners) != 0 {
		t.Fatalf("expected no listeners after shutdown raced with scan recovery bind, got %#v", finalState.Server.Listeners)
	}
	assertNoListenerHandles(t, finalState, listenKey)
}

func TestServerScenarioShutdownWhileRefreshBindBlockedDoesNotAttachStaleListener(t *testing.T) {
	tokenID, tokenHash := fixedTestToken()
	host := mustParseTestHost(t, "127.0.0.1")

	initialListenKey := ListenKey{Protocol: "tcp", IP: "127.0.0.1", Port: uint16(freeTCPPort(t))}
	refreshedListenKey := ListenKey{Protocol: "tcp", IP: "127.0.0.1", Port: uint16(freeTCPPortExcept(t, int(initialListenKey.Port)))}
	listenerFactory := NewScriptedListenerFactory()
	repo := &scriptedRuntimeRepository{
		group: GroupRuntime{
			ID:               1,
			Name:             "group-a",
			Enabled:          true,
			EffectiveIP:      "127.0.0.1",
			ClientSecretHash: tokenHash,
			Snapshot: ConfigSnapshot{
				Version:       1,
				GeneratedAtMs: 100,
				Tunnels: []protocol.TunnelEntry{
					{
						TunnelID:    7,
						Protocol:    protocol.ProtocolTCP,
						TunnelFlags: protocol.TunnelFlagEnabled,
						RemoteStart: initialListenKey.Port,
						RemoteEnd:   initialListenKey.Port,
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

	runningState := waitForObservedState(t, server, listenerFactory, func(observed sharedtestsupport.ObservedState) bool {
		session := findObservedSession(observed.Server, repo.group.ID)
		return session != nil &&
			session.Pending == nil &&
			session.RecoveryMode == sharedtestsupport.RecoveryModeRunning &&
			countObservedListeners(observed.Server, repo.group.ID, 7) == 1 &&
			handleCountForPort(observed, initialListenKey) == 1
	})
	if runningState.Server.GroupSlots[repo.group.ID] == 0 {
		t.Fatalf("expected running session to occupy group slot before refresh shutdown race, got %#v", runningState.Server.GroupSlots)
	}

	controller := testhooks.NewController()
	controller.AddBarrier("control.listener.before_bind", 1)
	restoreHooks := testhooks.Install(controller)
	defer restoreHooks()

	repo.SetGroup(GroupRuntime{
		ID:               1,
		Name:             "group-a",
		Enabled:          true,
		EffectiveIP:      "127.0.0.1",
		ClientSecretHash: tokenHash,
		Snapshot: ConfigSnapshot{
			Version:       2,
			GeneratedAtMs: 200,
			Tunnels: []protocol.TunnelEntry{
				{
					TunnelID:    7,
					Protocol:    protocol.ProtocolTCP,
					TunnelFlags: protocol.TunnelFlagEnabled,
					RemoteStart: refreshedListenKey.Port,
					RemoteEnd:   refreshedListenKey.Port,
					LocalHost:   host,
					LocalStart:  2200,
					LocalEnd:    2200,
				},
			},
		},
	})

	refreshDone := make(chan struct{})
	go func() {
		defer close(refreshDone)
		server.RefreshGroup(repo.group.ID)
	}()

	recoveryFrame := readMessage(t, clientConn)
	if recoveryFrame.Type != protocol.TypeConfigPush {
		t.Fatalf("expected refresh config.push, got %s", recoveryFrame.Type.String())
	}
	recoveryPush, err := protocol.UnmarshalConfigPush(recoveryFrame.Body)
	if err != nil {
		t.Fatalf("unmarshal refresh config.push: %v", err)
	}
	if recoveryPush.ConfigVersion != 2 || len(recoveryPush.Tunnels) != 1 || recoveryPush.Tunnels[0].RemoteStart != refreshedListenKey.Port {
		t.Fatalf("unexpected refresh config.push: %#v", recoveryPush)
	}
	writeConfigAck(t, clientConn, recoveryFrame.RequestID, recoveryPush.ConfigVersion)

	bindHit := waitForTestHookHit(t, controller, "control.listener.before_bind", 1)
	if got := bindHit.Fields["kind"]; got != "runtime_start" {
		t.Fatalf("expected blocked refresh bind to come from runtime_start, got %#v", bindHit.Fields)
	}
	if got := bindHit.Fields["port"]; got != refreshedListenKey.Port {
		t.Fatalf("expected blocked refresh bind to target refreshed port %d, got %#v", refreshedListenKey.Port, bindHit.Fields)
	}

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer shutdownCancel()
	shutdownDone := make(chan error, 1)
	go func() {
		shutdownDone <- server.Shutdown(shutdownCtx)
	}()

	if !controller.Release("control.listener.before_bind", 1) {
		t.Fatal("release blocked refresh bind failed")
	}

	select {
	case <-refreshDone:
	case <-time.After(2 * time.Second):
		t.Fatal("refresh did not complete after shutdown raced with blocked bind")
	}

	select {
	case err := <-shutdownDone:
		if err != nil && !errors.Is(err, context.Canceled) {
			t.Fatalf("shutdown during refresh bind failed: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("shutdown during refresh bind did not complete")
	}

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("session did not exit after shutdown raced with refresh bind")
	}

	finalState := waitForObservedState(t, server, listenerFactory, func(observed sharedtestsupport.ObservedState) bool {
		return len(observed.Server.Sessions) == 0 &&
			len(observed.Server.GroupSlots) == 0 &&
			countObservedListeners(observed.Server, repo.group.ID, 7) == 0 &&
			handleCountForPort(observed, initialListenKey) == 0 &&
			handleCountForPort(observed, refreshedListenKey) == 0
	})
	if len(finalState.Server.Listeners) != 0 {
		t.Fatalf("expected no listeners after shutdown raced with refresh bind, got %#v", finalState.Server.Listeners)
	}
	assertNoListenerHandles(t, finalState, initialListenKey, refreshedListenKey)
}
