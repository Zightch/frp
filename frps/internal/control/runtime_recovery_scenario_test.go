//go:build testhooks

package control

import (
	"context"
	"crypto/sha256"
	"io"
	"log/slog"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/zightch/frp/frps/internal/clock"
	"github.com/zightch/frp/frps/internal/system"
	"github.com/zightch/frp/frps/internal/testhooks"
	"github.com/zightch/frp/frps/pkg/protocol"
	sharedtestsupport "github.com/zightch/frp/frps/pkg/testsupport"
)

func TestServerScenarioRecoversNonListeningTunnelAfterPollingSeesOccupancyGone(t *testing.T) {
	controller := testhooks.NewController()
	restoreHooks := testhooks.Install(controller)
	defer restoreHooks()

	var tokenID [16]byte
	copy(tokenID[:], []byte("token-id-1234567"))

	var tokenSecret [32]byte
	copy(tokenSecret[:], []byte("0123456789abcdef0123456789abcdef"))
	tokenHash := sha256.Sum256(tokenSecret[:])

	host, err := protocol.ParseHost("127.0.0.1")
	if err != nil {
		t.Fatalf("parse host: %v", err)
	}

	manual := clock.NewManual(time.Unix(0, 0))
	listenerFactory := NewScriptedListenerFactory()
	listenKey := ListenKey{Protocol: "tcp", IP: "127.0.0.1", Port: 21001}
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
			Clock:           manual,
			Scheduler:       manual,
			ListenerFactory: listenerFactory,
			RuntimeScanPoll: time.Second,
		},
		slog.New(slog.NewTextHandler(io.Discard, nil)),
		"test-server",
	)

	pollCtx, pollCancel := context.WithCancel(context.Background())
	defer pollCancel()
	server.startRuntimeIssuePolling(pollCtx)

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
			strings.Contains(tunnel.RuntimeIssue, "端口冲突") &&
			tunnel.FinalStatus == "异常" &&
			countObservedListeners(observed.Server, repo.group.ID, 7) == 0
	})

	blockedSession := requireObservedSession(t, blockedState.Server, repo.group.ID)
	blockedTunnel := requireObservedTunnel(t, blockedState.Server, repo.group.ID, 7)
	if blockedSession.Pending != nil {
		t.Fatalf("expected no pending config after startup bind failure, got %#v", blockedSession.Pending)
	}
	if blockedSession.LastAckedConfigVersion != 1 {
		t.Fatalf("expected startup ack to settle before polling recovery, got %#v", blockedSession)
	}
	if blockedTunnel.FinalStatus != "异常" || !strings.Contains(blockedTunnel.RuntimeIssue, "端口冲突") {
		t.Fatalf("unexpected blocked tunnel state: %#v", blockedTunnel)
	}
	if handleCountForPort(blockedState, listenKey) != 0 {
		t.Fatalf("expected no listener handle while port remains externally occupied, got %#v", blockedState.Listeners)
	}

	listenerFactory.SetExternallyOccupied(listenKey, false)
	manual.Advance(time.Second)
	manual.WaitIdle()
	waitForTestHook(t, controller, "runtime.scan.after_round", 1)

	recoveredState := waitForObservedState(t, server, listenerFactory, func(observed sharedtestsupport.ObservedState) bool {
		session := findObservedSession(observed.Server, repo.group.ID)
		tunnel := findObservedTunnel(observed.Server, repo.group.ID, 7)
		return session != nil &&
			strings.TrimSpace(tunnel.RuntimeIssue) == "" &&
			tunnel.FinalStatus == "启用" &&
			countObservedListeners(observed.Server, repo.group.ID, 7) == 1 &&
			handleCountForPort(observed, listenKey) == 1
	})

	recoveredSession := requireObservedSession(t, recoveredState.Server, repo.group.ID)
	recoveredTunnel := requireObservedTunnel(t, recoveredState.Server, repo.group.ID, 7)
	if recoveredSession.RecoveryMode != sharedtestsupport.RecoveryModeRunning {
		t.Fatalf("expected session to return to running after polling recovery, got %#v", recoveredSession)
	}
	if recoveredTunnel.FinalStatus != "启用" || recoveredTunnel.RuntimeIssue != "" {
		t.Fatalf("unexpected recovered tunnel state: %#v", recoveredTunnel)
	}
	if !listenerWorldHasCall(recoveredState, listenKey, BindKindRuntimeProbe) || !listenerWorldHasCall(recoveredState, listenKey, BindKindRuntimeStart) {
		t.Fatalf("expected polling recovery to probe then start listener, got %#v", recoveredState.Listeners)
	}

	assertHeartbeatStillWorks(t, clientConn)

	_ = clientConn.Close()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("server connection did not exit")
	}
}

func TestServerScenarioUsesFreshSnapshotWhenRepositoryChangesDuringLoginHandshake(t *testing.T) {
	var tokenID [16]byte
	copy(tokenID[:], []byte("token-id-1234567"))

	var tokenSecret [32]byte
	copy(tokenSecret[:], []byte("0123456789abcdef0123456789abcdef"))
	tokenHash := sha256.Sum256(tokenSecret[:])

	host, err := protocol.ParseHost("127.0.0.1")
	if err != nil {
		t.Fatalf("parse host: %v", err)
	}

	oldListenKey := ListenKey{Protocol: "tcp", IP: "127.0.0.1", Port: 21011}
	newListenKey := ListenKey{Protocol: "tcp", IP: "127.0.0.1", Port: 21012}
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
						RemoteStart: oldListenKey.Port,
						RemoteEnd:   oldListenKey.Port,
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
			Network:         staticSnapshotReader{snapshot: localIPv4Snapshot("127.0.0.1")},
			ListenerFactory: listenerFactory,
		},
		slog.New(slog.NewTextHandler(io.Discard, nil)),
		"test-server",
	)

	clientRaw, serverRaw := net.Pipe()
	clientConn := &connWithRemoteAddr{
		Conn:   clientRaw,
		remote: &net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: 10001},
	}
	serverConn := &connWithRemoteAddr{
		Conn:   serverRaw,
		remote: &net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: 20001},
	}
	defer clientConn.Close()

	done := make(chan struct{})
	server.registerConn(serverConn)
	server.connWG.Add(1)
	go func() {
		defer close(done)
		server.handleConnection(serverConn)
	}()

	authBeginBody, err := protocol.MarshalAuthBegin(protocol.AuthBegin{
		TokenID:       tokenID,
		ClientVersion: "test-client",
		Hostname:      "node-1",
		OS:            protocol.OSLinux,
		Arch:          protocol.ArchAMD64,
	})
	if err != nil {
		t.Fatalf("marshal auth.begin: %v", err)
	}
	writeMessage(t, clientConn, protocol.Frame{
		Type:      protocol.TypeAuthBegin,
		RequestID: 1,
		Body:      authBeginBody,
	})

	challengeFrame := readMessage(t, clientConn)
	if challengeFrame.Type != protocol.TypeAuthChallenge {
		t.Fatalf("expected auth.challenge, got %s", challengeFrame.Type.String())
	}
	challenge, err := protocol.UnmarshalAuthChallenge(challengeFrame.Body)
	if err != nil {
		t.Fatalf("unmarshal auth.challenge: %v", err)
	}

	repo.group.Snapshot = ConfigSnapshot{
		Version:       2,
		GeneratedAtMs: 200,
		Tunnels: []protocol.TunnelEntry{
			{
				TunnelID:    7,
				Protocol:    protocol.ProtocolTCP,
				TunnelFlags: protocol.TunnelFlagEnabled,
				RemoteStart: newListenKey.Port,
				RemoteEnd:   newListenKey.Port,
				LocalHost:   host,
				LocalStart:  2300,
				LocalEnd:    2300,
			},
		},
	}

	authFinishBody, err := protocol.MarshalAuthFinish(protocol.AuthFinish{
		ChallengeID: challenge.ChallengeID,
		Response:    protocol.ChallengeResponse(tokenHash, challenge.Nonce),
	})
	if err != nil {
		t.Fatalf("marshal auth.finish: %v", err)
	}
	writeMessage(t, clientConn, protocol.Frame{
		Type:      protocol.TypeAuthFinish,
		RequestID: 2,
		Body:      authFinishBody,
	})

	helloFrame := readMessage(t, clientConn)
	if helloFrame.Type != protocol.TypeServerHello {
		t.Fatalf("expected server.hello, got %s", helloFrame.Type.String())
	}

	configFrame := readMessage(t, clientConn)
	if configFrame.Type != protocol.TypeConfigPush {
		t.Fatalf("expected config.push, got %s", configFrame.Type.String())
	}
	configPush, err := protocol.UnmarshalConfigPush(configFrame.Body)
	if err != nil {
		t.Fatalf("unmarshal config.push: %v", err)
	}
	if configPush.ConfigVersion != 2 {
		t.Fatalf("expected refreshed login snapshot version 2, got %#v", configPush)
	}
	if len(configPush.Tunnels) != 1 || configPush.Tunnels[0].RemoteStart != newListenKey.Port {
		t.Fatalf("expected login snapshot to use updated tunnel set, got %#v", configPush.Tunnels)
	}

	writeConfigAck(t, clientConn, configFrame.RequestID, configPush.ConfigVersion)

	finalState := waitForObservedState(t, server, listenerFactory, func(observed sharedtestsupport.ObservedState) bool {
		session := findObservedSession(observed.Server, repo.group.ID)
		return session != nil &&
			session.SnapshotVersion == 2 &&
			session.LastAckedConfigVersion == 2 &&
			countObservedListeners(observed.Server, repo.group.ID, 7) == 1 &&
			handleCountForPort(observed, newListenKey) == 1
	})

	finalSession := requireObservedSession(t, finalState.Server, repo.group.ID)
	if finalSession.SnapshotVersion != 2 || finalSession.LastAckedConfigVersion != 2 {
		t.Fatalf("expected active session to settle on refreshed login snapshot, got %#v", finalSession)
	}
	if handleCountForPort(finalState, oldListenKey) != 0 {
		t.Fatalf("expected stale login listener to stay absent, got %#v", finalState.Listeners)
	}
	if observedListenerPort(finalState.Server, repo.group.ID, 7) != newListenKey.Port {
		t.Fatalf("expected only refreshed login listener port to be active, got %#v", finalState.Server.Listeners)
	}

	assertHeartbeatStillWorks(t, clientConn)

	_ = clientConn.Close()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("server connection did not exit")
	}
}

func TestServerScenarioKeepsSessionAliveWhenPortBecomesOccupiedRightAfterStartupAck(t *testing.T) {
	controller := testhooks.NewController()
	controller.AddBarrier("control.listener.before_bind", 1)
	restoreHooks := testhooks.Install(controller)
	defer restoreHooks()

	var tokenID [16]byte
	copy(tokenID[:], []byte("token-id-1234567"))

	var tokenSecret [32]byte
	copy(tokenSecret[:], []byte("0123456789abcdef0123456789abcdef"))
	tokenHash := sha256.Sum256(tokenSecret[:])

	host, err := protocol.ParseHost("127.0.0.1")
	if err != nil {
		t.Fatalf("parse host: %v", err)
	}

	listenKey := ListenKey{Protocol: "tcp", IP: "127.0.0.1", Port: 21021}
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

	bindHit := waitForTestHookHit(t, controller, "control.listener.before_bind", 1)
	if got := bindHit.Fields["kind"]; got != "runtime_start" {
		t.Fatalf("expected initial startup bind barrier to come from runtime_start, got %#v", bindHit.Fields)
	}

	preFaultState := sharedtestsupport.ObservedState{Server: server.ObserveState()}
	preFaultSession := requireObservedSession(t, preFaultState.Server, repo.group.ID)
	if preFaultSession.Pending != nil {
		t.Fatalf("expected pending config to clear before startup bind begins, got %#v", preFaultSession.Pending)
	}
	if preFaultSession.LastAckedConfigVersion != 1 {
		t.Fatalf("expected startup ack to be accepted before bind barrier, got %#v", preFaultSession)
	}

	listenerFactory.SetExternallyOccupied(listenKey, true)
	if !controller.Release("control.listener.before_bind", 1) {
		t.Fatal("release control.listener.before_bind[1] failed")
	}

	failedState := waitForObservedState(t, server, listenerFactory, func(observed sharedtestsupport.ObservedState) bool {
		session := findObservedSession(observed.Server, repo.group.ID)
		tunnel := findObservedTunnel(observed.Server, repo.group.ID, 7)
		return session != nil &&
			strings.Contains(tunnel.RuntimeIssue, "端口冲突") &&
			tunnel.FinalStatus == "异常" &&
			countObservedListeners(observed.Server, repo.group.ID, 7) == 0 &&
			handleCountForPort(observed, listenKey) == 0
	})

	failedSession := requireObservedSession(t, failedState.Server, repo.group.ID)
	failedTunnel := requireObservedTunnel(t, failedState.Server, repo.group.ID, 7)
	if failedSession.Pending != nil {
		t.Fatalf("expected no pending config after immediate startup occupancy fault, got %#v", failedSession.Pending)
	}
	if failedState.Server.GroupSlots[repo.group.ID] != failedSession.SessionID {
		t.Fatalf("expected group slot to stay occupied by active session, got %#v", failedState.Server.GroupSlots)
	}
	if failedTunnel.FinalStatus != "异常" || !strings.Contains(failedTunnel.RuntimeIssue, "端口冲突") {
		t.Fatalf("unexpected tunnel state after immediate occupancy fault: %#v", failedTunnel)
	}

	assertHeartbeatStillWorks(t, clientConn)

	_ = clientConn.Close()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("server connection did not exit")
	}
}

func TestServerScenarioShrinksToEmptyConfigWhenEffectiveIPBecomesInvalidRightAfterStartupAck(t *testing.T) {
	controller := testhooks.NewController()
	controller.AddBarrier("control.listener.before_bind", 1)
	restoreHooks := testhooks.Install(controller)
	defer restoreHooks()

	var tokenID [16]byte
	copy(tokenID[:], []byte("token-id-1234567"))

	var tokenSecret [32]byte
	copy(tokenSecret[:], []byte("0123456789abcdef0123456789abcdef"))
	tokenHash := sha256.Sum256(tokenSecret[:])

	host, err := protocol.ParseHost("127.0.0.1")
	if err != nil {
		t.Fatalf("parse host: %v", err)
	}

	listenKey := ListenKey{Protocol: "tcp", IP: "127.0.0.1", Port: 21031}
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
	network := &mutableSnapshotReader{
		snapshot: localIPv4Snapshot("127.0.0.1"),
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

	configPush, err := protocol.UnmarshalConfigPush(configFrame.Body)
	if err != nil {
		t.Fatalf("unmarshal config.push: %v", err)
	}
	writeConfigAck(t, clientConn, configFrame.RequestID, configPush.ConfigVersion)

	bindHit := waitForTestHookHit(t, controller, "control.listener.before_bind", 1)
	if got := bindHit.Fields["kind"]; got != "runtime_start" {
		t.Fatalf("expected initial startup bind barrier to come from runtime_start, got %#v", bindHit.Fields)
	}
	waitForActiveGroupSession(t, server, repo.group.ID)

	active, ok := server.activeSession(repo.group.ID)
	if !ok || active == nil || active.session == nil {
		t.Fatalf("active session for group %d not found", repo.group.ID)
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
				RemoteStart: listenKey.Port,
				RemoteEnd:   listenKey.Port,
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
		t.Fatalf("unexpected empty config.push after immediate effective_ip fault: %#v", emptyPush)
	}

	pendingState := waitForObservedState(t, server, listenerFactory, func(observed sharedtestsupport.ObservedState) bool {
		session := findObservedSession(observed.Server, repo.group.ID)
		return session != nil &&
			session.Pending != nil &&
			session.Pending.Version == 2 &&
			session.Pending.TunnelCount == 0 &&
			session.RecoveryMode == sharedtestsupport.RecoveryModePendingEmptyConfig
	})

	pendingSession := requireObservedSession(t, pendingState.Server, repo.group.ID)
	if pendingSession.Pending == nil || pendingSession.Pending.Version != 2 || pendingSession.Pending.TunnelCount != 0 {
		t.Fatalf("unexpected pending empty config state: %#v", pendingSession)
	}

	if !controller.Release("control.listener.before_bind", 1) {
		t.Fatal("release control.listener.before_bind[1] failed")
	}
	writeConfigAck(t, clientConn, emptyFrame.RequestID, emptyPush.ConfigVersion)

	select {
	case <-refreshDone:
	case <-time.After(time.Second):
		t.Fatal("refresh did not complete")
	}
	waitForIdleConfig(t, active.session)

	finalState := waitForObservedState(t, server, listenerFactory, func(observed sharedtestsupport.ObservedState) bool {
		session := findObservedSession(observed.Server, repo.group.ID)
		tunnel := findObservedTunnel(observed.Server, repo.group.ID, 7)
		return session != nil &&
			session.Pending == nil &&
			session.SnapshotVersion == 2 &&
			session.SnapshotTunnelCount == 0 &&
			session.RecoveryMode == sharedtestsupport.RecoveryModeEmptyConfig &&
			countObservedListeners(observed.Server, repo.group.ID, 7) == 0 &&
			handleCountForPort(observed, listenKey) == 0 &&
			strings.Contains(tunnel.RuntimeIssue, "当前不存在于本机") &&
			tunnel.FinalStatus == "异常"
	})

	finalSession := requireObservedSession(t, finalState.Server, repo.group.ID)
	finalTunnel := requireObservedTunnel(t, finalState.Server, repo.group.ID, 7)
	if finalSession.SnapshotTunnelCount != 0 || finalSession.RecoveryMode != sharedtestsupport.RecoveryModeEmptyConfig {
		t.Fatalf("unexpected session state after immediate effective_ip fault: %#v", finalSession)
	}
	if !strings.Contains(finalTunnel.RuntimeIssue, "当前不存在于本机") || finalTunnel.FinalStatus != "异常" {
		t.Fatalf("unexpected tunnel state after immediate effective_ip fault: %#v", finalTunnel)
	}

	assertHeartbeatStillWorks(t, clientConn)

	_ = clientConn.Close()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("server connection did not exit")
	}
}

func localIPv4Snapshot(addrs ...string) system.Snapshot {
	snapshot := system.Snapshot{
		AvailableIPs: make([]system.IPAddress, 0, len(addrs)),
	}
	for _, addr := range addrs {
		snapshot.AvailableIPs = append(snapshot.AvailableIPs, system.IPAddress{
			Addr:   addr,
			Family: system.FamilyIPv4,
		})
	}
	return snapshot
}

func observeServerAndListeners(server *Server, listenerFactory *ScriptedListenerFactory) sharedtestsupport.ObservedState {
	observed := sharedtestsupport.ObservedState{
		Server: server.ObserveState(),
	}
	if listenerFactory != nil {
		listenerState := listenerFactory.ObserveState()
		observed.Listeners = &listenerState
	}
	return observed
}

func waitForObservedState(
	t *testing.T,
	server *Server,
	listenerFactory *ScriptedListenerFactory,
	predicate func(sharedtestsupport.ObservedState) bool,
) sharedtestsupport.ObservedState {
	t.Helper()

	deadline := time.Now().Add(2 * time.Second)
	for {
		observed := observeServerAndListeners(server, listenerFactory)
		if predicate(observed) {
			return observed
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for observed state: %#v", observed)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func findObservedSession(state sharedtestsupport.ServerObservedState, groupID int64) *sharedtestsupport.SessionObservedState {
	for index := range state.Sessions {
		if state.Sessions[index].GroupID == groupID {
			return &state.Sessions[index]
		}
	}
	return nil
}

func findObservedTunnel(state sharedtestsupport.ServerObservedState, groupID int64, tunnelID uint32) *sharedtestsupport.TunnelObservedState {
	for index := range state.Tunnels {
		if state.Tunnels[index].GroupID == groupID && state.Tunnels[index].TunnelID == tunnelID {
			return &state.Tunnels[index]
		}
	}
	return nil
}

func requireObservedTunnel(t *testing.T, state sharedtestsupport.ServerObservedState, groupID int64, tunnelID uint32) sharedtestsupport.TunnelObservedState {
	t.Helper()

	tunnel := findObservedTunnel(state, groupID, tunnelID)
	if tunnel == nil {
		t.Fatalf("observed tunnel for group %d tunnel %d not found: %#v", groupID, tunnelID, state.Tunnels)
	}
	return *tunnel
}

func handleCountForPort(state sharedtestsupport.ObservedState, key ListenKey) int {
	if state.Listeners == nil {
		return 0
	}
	for _, handle := range state.Listeners.Handles {
		if handle.Protocol == key.Protocol && handle.IP == key.IP && handle.Port == key.Port {
			return handle.Count
		}
	}
	return 0
}

func listenerWorldHasCall(state sharedtestsupport.ObservedState, key ListenKey, kind BindKind) bool {
	if state.Listeners == nil {
		return false
	}
	for _, call := range state.Listeners.Calls {
		if call.Protocol == key.Protocol && call.IP == key.IP && call.Port == key.Port && call.Kind == string(kind) && call.Op == "listen_tcp" {
			return true
		}
	}
	return false
}

func observedListenerPort(state sharedtestsupport.ServerObservedState, groupID int64, tunnelID uint32) uint16 {
	for _, listener := range state.Listeners {
		if listener.GroupID == groupID && listener.TunnelID == tunnelID {
			return listener.Port
		}
	}
	return 0
}

func assertHeartbeatStillWorks(t *testing.T, clientConn net.Conn) {
	t.Helper()

	pingBody, err := protocol.MarshalHeartbeatPing(protocol.HeartbeatPing{
		ClientUnixMs: uint64(time.Now().UTC().UnixMilli()),
	})
	if err != nil {
		t.Fatalf("marshal heartbeat.ping: %v", err)
	}
	writeMessage(t, clientConn, protocol.Frame{
		Type:      protocol.TypeHeartbeatPing,
		RequestID: 3,
		Body:      pingBody,
	})
	pongFrame := readMessage(t, clientConn)
	if pongFrame.Type != protocol.TypeHeartbeatPong || pongFrame.RequestID != 3 {
		t.Fatalf("unexpected heartbeat.pong: %#v", pongFrame)
	}
}
