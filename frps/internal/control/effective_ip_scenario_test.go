//go:build testhooks

package control

import (
	"context"
	"crypto/sha256"
	"io"
	"log/slog"
	"net"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/zightch/frp/frps/internal/system"
	"github.com/zightch/frp/frps/internal/testhooks"
	"github.com/zightch/frp/frps/pkg/protocol"
	sharedtestsupport "github.com/zightch/frp/frps/pkg/testsupport"
)

func TestServerScenarioRejectsInitialInvalidEffectiveIPWithoutLeakingSessionState(t *testing.T) {
	controller := testhooks.NewController()
	controller.AddBarrier("control.config_ack.before_accept", 1)
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

	server := NewServer(
		Options{
			Repository: stubRepository{
				group: GroupRuntime{
					ID:               1,
					Name:             "group-a",
					Enabled:          true,
					EffectiveIP:      "127.0.0.2",
					ClientSecretHash: tokenHash,
					Snapshot: ConfigSnapshot{
						Version:       99,
						GeneratedAtMs: 1234,
						Tunnels: []protocol.TunnelEntry{
							{
								TunnelID:    7,
								Protocol:    protocol.ProtocolTCP,
								TunnelFlags: protocol.TunnelFlagEnabled,
								RemoteStart: 21001,
								RemoteEnd:   21001,
								LocalHost:   host,
								LocalStart:  22,
								LocalEnd:    22,
							},
						},
					},
				},
			},
			ReadTimeout:       time.Second,
			WriteTimeout:      time.Second,
			ChallengeTTL:      5 * time.Second,
			HeartbeatInterval: 2 * time.Second,
			Network: staticSnapshotReader{
				snapshot: system.Snapshot{
					AvailableIPs: []system.IPAddress{
						{Addr: "127.0.0.1", Family: system.FamilyIPv4},
					},
				},
			},
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

	waitForTestHook(t, controller, "control.config_ack.before_accept", 1)

	beforeReject := sharedtestsupport.ObservedState{Server: server.ObserveState()}
	session := requireObservedSession(t, beforeReject.Server, 1)
	if session.Pending == nil || session.Pending.Version != configPush.ConfigVersion {
		t.Fatalf("expected pending startup config before rejection, got %#v", session.Pending)
	}
	if got := beforeReject.Server.GroupSlots[1]; got != session.SessionID {
		t.Fatalf("expected group slot to be occupied by session %d, got %d", session.SessionID, got)
	}
	if len(beforeReject.Server.Listeners) != 0 {
		t.Fatalf("expected no listeners before startup ack is accepted, got %#v", beforeReject.Server.Listeners)
	}

	if !controller.Release("control.config_ack.before_accept", 1) {
		t.Fatal("release config_ack.before_accept barrier failed")
	}

	errorFrame := readMessage(t, clientConn)
	if errorFrame.Type != protocol.TypeError {
		t.Fatalf("expected error frame, got %s", errorFrame.Type.String())
	}
	errorBody, err := protocol.UnmarshalErrorBody(errorFrame.Body)
	if err != nil {
		t.Fatalf("unmarshal error frame: %v", err)
	}
	if errorBody.ErrorCode != protocol.ErrorCodeConfigApplyFailed {
		t.Fatalf("unexpected error code: %d", errorBody.ErrorCode)
	}
	if !strings.Contains(errorBody.Message, "当前不存在于本机") || !strings.Contains(errorBody.Message, "请联系管理员解决") {
		t.Fatalf("unexpected error message: %q", errorBody.Message)
	}

	if _, err := readMessageWithin(clientConn, time.Second); err == nil {
		t.Fatal("expected server to close the startup session after rejection")
	} else if !errorsIsEOFOrClosed(err) {
		t.Fatalf("unexpected read error after startup rejection: %v", err)
	}

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("server connection did not exit")
	}

	afterReject := sharedtestsupport.ObservedState{Server: server.ObserveState()}
	if len(afterReject.Server.Sessions) != 0 {
		t.Fatalf("expected rejected startup session to be removed, got %#v", afterReject.Server.Sessions)
	}
	if len(afterReject.Server.GroupSlots) != 0 {
		t.Fatalf("expected group slot to be cleared after rejection, got %#v", afterReject.Server.GroupSlots)
	}
	if issues := server.TunnelRuntimeIssues(); !strings.Contains(issues[7], "当前不存在于本机") {
		t.Fatalf("expected runtime issue for missing local effective_ip, got %#v", issues)
	}
}

func TestServerScenarioRecoversEmptyConfigOnlyAfterAckThenListenerBind(t *testing.T) {
	var tokenID [16]byte
	copy(tokenID[:], []byte("token-id-1234567"))

	var tokenSecret [32]byte
	copy(tokenSecret[:], []byte("0123456789abcdef0123456789abcdef"))
	tokenHash := sha256.Sum256(tokenSecret[:])

	host, err := protocol.ParseHost("127.0.0.1")
	if err != nil {
		t.Fatalf("parse host: %v", err)
	}

	tcpPort := freeTCPPort(t)
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
						RemoteStart: uint16(tcpPort),
						RemoteEnd:   uint16(tcpPort),
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
			Repository: repo,
			Network:    network,
		},
		slog.New(slog.NewTextHandler(io.Discard, nil)),
		"test-server",
	)

	clientConn, done, configFrame := authenticateServerSession(t, server, tokenID, tokenHash)
	defer clientConn.Close()

	configPush, err := protocol.UnmarshalConfigPush(configFrame.Body)
	if err != nil {
		t.Fatalf("unmarshal initial config.push: %v", err)
	}
	writeConfigAck(t, clientConn, configFrame.RequestID, configPush.ConfigVersion)

	active, ok := server.activeSession(repo.group.ID)
	if !ok || active == nil {
		waitForActiveGroupSession(t, server, repo.group.ID)
		active, ok = server.activeSession(repo.group.ID)
	}
	if !ok || active == nil {
		t.Fatalf("active session for group %d not found", repo.group.ID)
	}
	waitForIdleConfig(t, active.session)

	repo.group.EffectiveIP = "127.0.0.2"
	repo.group.Snapshot = ConfigSnapshot{
		Version:       2,
		GeneratedAtMs: 200,
		Tunnels: []protocol.TunnelEntry{
			{
				TunnelID:    7,
				Protocol:    protocol.ProtocolTCP,
				TunnelFlags: protocol.TunnelFlagEnabled,
				RemoteStart: uint16(tcpPort),
				RemoteEnd:   uint16(tcpPort),
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
	writeConfigAck(t, clientConn, emptyFrame.RequestID, emptyPush.ConfigVersion)

	select {
	case <-refreshDone:
	case <-time.After(time.Second):
		t.Fatal("refresh did not complete")
	}

	emptyState := waitForObservedState(t, server, nil, func(observed sharedtestsupport.ObservedState) bool {
		session := findObservedSession(observed.Server, repo.group.ID)
		return session != nil &&
			session.Pending == nil &&
			session.RecoveryMode == sharedtestsupport.RecoveryModeEmptyConfig &&
			session.SnapshotTunnelCount == 0 &&
			countObservedListeners(observed.Server, repo.group.ID, 7) == 0
	})
	if issues := server.TunnelRuntimeIssues(); !strings.Contains(issues[7], "当前不存在于本机") {
		t.Fatalf("expected runtime issue in empty-config mode, got %#v", issues)
	}

	controller := testhooks.NewController()
	controller.AddBarrier("control.config_ack.before_accept", 1)
	controller.AddBarrier("control.listener.before_bind", 1)
	controller.AddBarrier("control.listener.before_bind", 2)
	restoreHooks := testhooks.Install(controller)
	defer restoreHooks()

	network.setSnapshot(system.Snapshot{
		AvailableIPs: []system.IPAddress{
			{Addr: "127.0.0.1", Family: system.FamilyIPv4},
			{Addr: "127.0.0.2", Family: system.FamilyIPv4},
		},
	})

	scanDone := make(chan error, 1)
	go func() {
		scanDone <- server.scanNonListeningTunnelRuntimeIssues(context.Background())
	}()

	probeHit := waitForTestHookHit(t, controller, "control.listener.before_bind", 1)
	if got := probeHit.Fields["kind"]; got != "runtime_probe" {
		t.Fatalf("expected first bind hook to come from runtime probe, got %#v", probeHit.Fields)
	}
	if !controller.Release("control.listener.before_bind", 1) {
		t.Fatal("release listener.before_bind probe barrier failed")
	}

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

	pendingState := sharedtestsupport.ObservedState{Server: server.ObserveState()}
	pendingSession := requireObservedSession(t, pendingState.Server, repo.group.ID)
	if pendingSession.Pending == nil || pendingSession.Pending.Version != recoveredPush.ConfigVersion {
		t.Fatalf("expected pending full config before recovery ack, got %#v", pendingSession.Pending)
	}
	if pendingSession.RecoveryMode != sharedtestsupport.RecoveryModePendingFullConfig {
		t.Fatalf("expected pending full-config mode before recovery ack, got %s", pendingSession.RecoveryMode)
	}
	if countObservedListeners(pendingState.Server, repo.group.ID, 7) != 0 {
		t.Fatalf("expected listeners to stay down before recovery ack, got %#v", pendingState.Server.Listeners)
	}

	writeConfigAck(t, clientConn, recoveredFrame.RequestID, recoveredPush.ConfigVersion)
	waitForTestHook(t, controller, "control.config_ack.before_accept", 1)

	ackHeldState := sharedtestsupport.ObservedState{Server: server.ObserveState()}
	ackHeldSession := requireObservedSession(t, ackHeldState.Server, repo.group.ID)
	if ackHeldSession.Pending == nil {
		t.Fatalf("expected pending config while recovery ack barrier is held, got %#v", ackHeldSession)
	}
	if countObservedListeners(ackHeldState.Server, repo.group.ID, 7) != 0 {
		t.Fatalf("expected no listeners before recovery ack is accepted, got %#v", ackHeldState.Server.Listeners)
	}

	if !controller.Release("control.config_ack.before_accept", 1) {
		t.Fatal("release config_ack.before_accept barrier failed")
	}
	bindHit := waitForTestHookHit(t, controller, "control.listener.before_bind", 2)
	if got := bindHit.Fields["kind"]; got != "runtime_start" {
		t.Fatalf("expected second bind hook to come from runtime_start, got %#v", bindHit.Fields)
	}

	beforeBindState := sharedtestsupport.ObservedState{Server: server.ObserveState()}
	beforeBindSession := requireObservedSession(t, beforeBindState.Server, repo.group.ID)
	if beforeBindSession.Pending != nil {
		t.Fatalf("expected pending config to clear before listener bind, got %#v", beforeBindSession.Pending)
	}
	if beforeBindSession.LastAckedConfigVersion != recoveredPush.ConfigVersion {
		t.Fatalf("expected recovery ack to advance config version before bind, got %#v", beforeBindSession)
	}
	if countObservedListeners(beforeBindState.Server, repo.group.ID, 7) != 0 {
		t.Fatalf("expected no listeners before bind barrier releases, got %#v", beforeBindState.Server.Listeners)
	}
	if violations := sharedtestsupport.CheckEmptyConfigRecoveryOrder(sharedtestsupport.InvariantCheckInput{
		Before: &emptyState,
		After:  &beforeBindState,
	}); len(violations) != 0 {
		t.Fatalf("unexpected empty-config recovery order violations: %#v", violations)
	}
	assertTCPDialFails(t, tcpPort)

	if !controller.Release("control.listener.before_bind", 2) {
		t.Fatal("release listener.before_bind barrier failed")
	}

	select {
	case err := <-scanDone:
		if err != nil {
			t.Fatalf("scan runtime issues after effective_ip recovery: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("scan did not complete")
	}

	finalBindIP := ""
	deadline := time.Now().Add(2 * time.Second)
	for {
		observed := sharedtestsupport.ObservedState{Server: server.ObserveState()}
		if countObservedListeners(observed.Server, repo.group.ID, 7) == 1 {
			finalSession := requireObservedSession(t, observed.Server, repo.group.ID)
			if finalSession.RecoveryMode != sharedtestsupport.RecoveryModeRunning {
				t.Fatalf("expected running mode after recovery, got %s", finalSession.RecoveryMode)
			}
			if issues := server.TunnelRuntimeIssues(); len(issues) != 0 {
				t.Fatalf("expected runtime issues to clear after recovery, got %#v", issues)
			}
			finalBindIP = observedListenerBindIP(observed.Server, repo.group.ID, 7)
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("expected recovered listener to start, got %#v", observed.Server.Listeners)
		}
		time.Sleep(10 * time.Millisecond)
	}

	publicConn := waitForTCPDialAddress(t, net.JoinHostPort(finalBindIP, strconv.Itoa(tcpPort)))
	_ = publicConn.Close()

	_ = clientConn.Close()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("server connection did not exit")
	}
}

func TestServerScenarioReconnectAfterEmptyConfigClearsOldRuntimeIssueAndListeners(t *testing.T) {
	var tokenID [16]byte
	copy(tokenID[:], []byte("token-id-1234567"))

	var tokenSecret [32]byte
	copy(tokenSecret[:], []byte("0123456789abcdef0123456789abcdef"))
	tokenHash := sha256.Sum256(tokenSecret[:])

	host, err := protocol.ParseHost("127.0.0.1")
	if err != nil {
		t.Fatalf("parse host: %v", err)
	}

	port := uint16(freeTCPPort(t))
	initialListenKey := ListenKey{Protocol: "tcp", IP: "127.0.0.1", Port: port}
	recoveredListenKey := ListenKey{Protocol: "tcp", IP: "127.0.0.2", Port: port}
	listenerFactory := NewScriptedListenerFactory()
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

	firstConn, firstDone, configFrame := authenticateServerSession(t, server, tokenID, tokenHash)
	defer firstConn.Close()

	initialPush, err := protocol.UnmarshalConfigPush(configFrame.Body)
	if err != nil {
		t.Fatalf("unmarshal initial config.push: %v", err)
	}
	writeConfigAck(t, firstConn, configFrame.RequestID, initialPush.ConfigVersion)

	runningState := waitForObservedState(t, server, listenerFactory, func(observed sharedtestsupport.ObservedState) bool {
		session := findObservedSession(observed.Server, repo.group.ID)
		return session != nil &&
			session.Pending == nil &&
			session.RecoveryMode == sharedtestsupport.RecoveryModeRunning &&
			session.SnapshotVersion == 1 &&
			countObservedListeners(observed.Server, repo.group.ID, 7) == 1 &&
			handleCountForPort(observed, initialListenKey) == 1
	})

	firstSession := requireObservedSession(t, runningState.Server, repo.group.ID)
	firstSessionID := firstSession.SessionID

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

	emptyFrame := readMessage(t, firstConn)
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
	writeConfigAck(t, firstConn, emptyFrame.RequestID, emptyPush.ConfigVersion)

	select {
	case <-refreshDone:
	case <-time.After(time.Second):
		t.Fatal("refresh did not complete")
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
			strings.Contains(tunnel.RuntimeIssue, "当前不存在于本机")
	})

	emptySession := requireObservedSession(t, emptyState.Server, repo.group.ID)
	if emptyState.Server.GroupSlots[repo.group.ID] != emptySession.SessionID {
		t.Fatalf("expected empty-config session to keep group slot, got %#v", emptyState.Server.GroupSlots)
	}

	network.setSnapshot(localIPv4Snapshot("127.0.0.1", "127.0.0.2"))

	_ = firstConn.Close()
	select {
	case <-firstDone:
	case <-time.After(2 * time.Second):
		t.Fatal("empty-config session did not exit")
	}

	stoppedState := waitForObservedState(t, server, listenerFactory, func(observed sharedtestsupport.ObservedState) bool {
		return len(observed.Server.Sessions) == 0 &&
			len(observed.Server.GroupSlots) == 0 &&
			countObservedListeners(observed.Server, repo.group.ID, 7) == 0 &&
			handleCountForPort(observed, initialListenKey) == 0 &&
			handleCountForPort(observed, recoveredListenKey) == 0
	})
	if len(stoppedState.Server.Listeners) != 0 {
		t.Fatalf("expected no attached listeners after old empty-config session exit, got %#v", stoppedState.Server.Listeners)
	}

	secondConn, secondDone, secondConfigFrame := authenticateServerSession(t, server, tokenID, tokenHash)
	defer secondConn.Close()

	secondPush, err := protocol.UnmarshalConfigPush(secondConfigFrame.Body)
	if err != nil {
		t.Fatalf("unmarshal replacement config.push: %v", err)
	}
	if secondPush.ConfigVersion != 2 || len(secondPush.Tunnels) != 1 {
		t.Fatalf("unexpected replacement config.push: %#v", secondPush)
	}
	writeConfigAck(t, secondConn, secondConfigFrame.RequestID, secondPush.ConfigVersion)

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

	finalSession := requireObservedSession(t, finalState.Server, repo.group.ID)
	if finalSession.SessionID == firstSessionID {
		t.Fatalf("expected reconnect to create a new session, got %#v", finalSession)
	}
	if finalState.Server.GroupSlots[repo.group.ID] != finalSession.SessionID {
		t.Fatalf("expected group slot to belong to replacement session, got %#v", finalState.Server.GroupSlots)
	}
	if issues := server.TunnelRuntimeIssues(); len(issues) != 0 {
		t.Fatalf("expected stale runtime issue to clear after reconnect, got %#v", issues)
	}

	assertHeartbeatStillWorks(t, secondConn)

	_ = secondConn.Close()
	select {
	case <-secondDone:
	case <-time.After(2 * time.Second):
		t.Fatal("replacement session did not exit")
	}
}

func TestServerScenarioReconnectAfterPendingRecoveryPushDropsOldPendingConfig(t *testing.T) {
	var tokenID [16]byte
	copy(tokenID[:], []byte("token-id-1234567"))

	var tokenSecret [32]byte
	copy(tokenSecret[:], []byte("0123456789abcdef0123456789abcdef"))
	tokenHash := sha256.Sum256(tokenSecret[:])

	host, err := protocol.ParseHost("127.0.0.1")
	if err != nil {
		t.Fatalf("parse host: %v", err)
	}

	port := uint16(freeTCPPort(t))
	initialListenKey := ListenKey{Protocol: "tcp", IP: "127.0.0.1", Port: port}
	recoveredListenKey := ListenKey{Protocol: "tcp", IP: "127.0.0.2", Port: port}
	listenerFactory := NewScriptedListenerFactory()
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

	firstConn, firstDone, configFrame := authenticateServerSession(t, server, tokenID, tokenHash)
	defer firstConn.Close()

	initialPush, err := protocol.UnmarshalConfigPush(configFrame.Body)
	if err != nil {
		t.Fatalf("unmarshal initial config.push: %v", err)
	}
	writeConfigAck(t, firstConn, configFrame.RequestID, initialPush.ConfigVersion)

	waitForObservedState(t, server, listenerFactory, func(observed sharedtestsupport.ObservedState) bool {
		session := findObservedSession(observed.Server, repo.group.ID)
		return session != nil &&
			session.Pending == nil &&
			session.RecoveryMode == sharedtestsupport.RecoveryModeRunning &&
			session.SnapshotVersion == 1 &&
			countObservedListeners(observed.Server, repo.group.ID, 7) == 1 &&
			handleCountForPort(observed, initialListenKey) == 1
	})

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

	emptyFrame := readMessage(t, firstConn)
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
	writeConfigAck(t, firstConn, emptyFrame.RequestID, emptyPush.ConfigVersion)

	select {
	case <-refreshDone:
	case <-time.After(time.Second):
		t.Fatal("refresh did not complete")
	}

	emptyState := waitForObservedState(t, server, listenerFactory, func(observed sharedtestsupport.ObservedState) bool {
		session := findObservedSession(observed.Server, repo.group.ID)
		return session != nil &&
			session.Pending == nil &&
			session.RecoveryMode == sharedtestsupport.RecoveryModeEmptyConfig &&
			session.SnapshotVersion == 2 &&
			session.SnapshotTunnelCount == 0 &&
			countObservedListeners(observed.Server, repo.group.ID, 7) == 0 &&
			handleCountForPort(observed, initialListenKey) == 0 &&
			handleCountForPort(observed, recoveredListenKey) == 0
	})

	firstSession := requireObservedSession(t, emptyState.Server, repo.group.ID)
	firstSessionID := firstSession.SessionID

	network.setSnapshot(localIPv4Snapshot("127.0.0.1", "127.0.0.2"))

	scanDone := make(chan error, 1)
	go func() {
		scanDone <- server.scanNonListeningTunnelRuntimeIssues(context.Background())
	}()

	recoveredFrame := readMessage(t, firstConn)
	if recoveredFrame.Type != protocol.TypeConfigPush {
		t.Fatalf("expected recovery config.push, got %s", recoveredFrame.Type.String())
	}
	recoveredPush, err := protocol.UnmarshalConfigPush(recoveredFrame.Body)
	if err != nil {
		t.Fatalf("unmarshal recovery config.push: %v", err)
	}
	if recoveredPush.ConfigVersion != 2 || len(recoveredPush.Tunnels) != 1 {
		t.Fatalf("unexpected recovery config.push: %#v", recoveredPush)
	}

	pendingState := waitForObservedState(t, server, listenerFactory, func(observed sharedtestsupport.ObservedState) bool {
		session := findObservedSession(observed.Server, repo.group.ID)
		return session != nil &&
			session.Pending != nil &&
			session.Pending.Version == 2 &&
			session.Pending.TunnelCount == 1 &&
			session.RecoveryMode == sharedtestsupport.RecoveryModePendingFullConfig &&
			countObservedListeners(observed.Server, repo.group.ID, 7) == 0 &&
			handleCountForPort(observed, initialListenKey) == 0 &&
			handleCountForPort(observed, recoveredListenKey) == 0
	})

	pendingSession := requireObservedSession(t, pendingState.Server, repo.group.ID)
	if pendingSession.SessionID != firstSessionID {
		t.Fatalf("expected pending recovery config to belong to original session, got %#v", pendingSession)
	}

	_ = firstConn.Close()
	select {
	case <-firstDone:
	case <-time.After(2 * time.Second):
		t.Fatal("original session with pending recovery config did not exit")
	}

	select {
	case err := <-scanDone:
		if err != nil {
			t.Fatalf("scan runtime issues after reconnect prep: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("scan did not complete")
	}

	stoppedState := waitForObservedState(t, server, listenerFactory, func(observed sharedtestsupport.ObservedState) bool {
		return len(observed.Server.Sessions) == 0 &&
			len(observed.Server.GroupSlots) == 0 &&
			countObservedListeners(observed.Server, repo.group.ID, 7) == 0 &&
			handleCountForPort(observed, initialListenKey) == 0 &&
			handleCountForPort(observed, recoveredListenKey) == 0
	})
	if len(stoppedState.Server.Listeners) != 0 {
		t.Fatalf("expected no attached listeners after pending recovery session exit, got %#v", stoppedState.Server.Listeners)
	}

	secondConn, secondDone, secondConfigFrame := authenticateServerSession(t, server, tokenID, tokenHash)
	defer secondConn.Close()

	secondPush, err := protocol.UnmarshalConfigPush(secondConfigFrame.Body)
	if err != nil {
		t.Fatalf("unmarshal replacement config.push: %v", err)
	}
	if secondPush.ConfigVersion != 2 || len(secondPush.Tunnels) != 1 {
		t.Fatalf("unexpected replacement config.push: %#v", secondPush)
	}
	writeConfigAck(t, secondConn, secondConfigFrame.RequestID, secondPush.ConfigVersion)

	finalState := waitForObservedState(t, server, listenerFactory, func(observed sharedtestsupport.ObservedState) bool {
		session := findObservedSession(observed.Server, repo.group.ID)
		return session != nil &&
			session.Pending == nil &&
			session.RecoveryMode == sharedtestsupport.RecoveryModeRunning &&
			session.SnapshotVersion == 2 &&
			session.LastAckedConfigVersion == 2 &&
			countObservedListeners(observed.Server, repo.group.ID, 7) == 1 &&
			handleCountForPort(observed, initialListenKey) == 0 &&
			handleCountForPort(observed, recoveredListenKey) == 1
	})

	finalSession := requireObservedSession(t, finalState.Server, repo.group.ID)
	if finalSession.SessionID == firstSessionID {
		t.Fatalf("expected replacement session after old pending config disconnect, got %#v", finalSession)
	}
	if finalSession.Pending != nil {
		t.Fatalf("expected old pending config not to leak into replacement session, got %#v", finalSession)
	}
	if finalState.Server.GroupSlots[repo.group.ID] != finalSession.SessionID {
		t.Fatalf("expected group slot to move to replacement session, got %#v", finalState.Server.GroupSlots)
	}
	if issues := server.TunnelRuntimeIssues(); len(issues) != 0 {
		t.Fatalf("expected runtime issues to be clear after replacement session recovery, got %#v", issues)
	}

	assertHeartbeatStillWorks(t, secondConn)

	_ = secondConn.Close()
	select {
	case <-secondDone:
	case <-time.After(2 * time.Second):
		t.Fatal("replacement session did not exit")
	}
}

func TestServerScenarioKeepsEffectiveIPRuntimeIssueAcrossHighFrequencyEmptyAndFullRecoveryFlaps(t *testing.T) {
	var tokenID [16]byte
	copy(tokenID[:], []byte("token-id-1234567"))

	var tokenSecret [32]byte
	copy(tokenSecret[:], []byte("0123456789abcdef0123456789abcdef"))
	tokenHash := sha256.Sum256(tokenSecret[:])

	host, err := protocol.ParseHost("127.0.0.1")
	if err != nil {
		t.Fatalf("parse host: %v", err)
	}

	port := uint16(freeTCPPort(t))
	initialListenKey := ListenKey{Protocol: "tcp", IP: "127.0.0.1", Port: port}
	recoveredListenKey := ListenKey{Protocol: "tcp", IP: "127.0.0.2", Port: port}
	listenerFactory := NewScriptedListenerFactory()
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

	initialPush, err := protocol.UnmarshalConfigPush(configFrame.Body)
	if err != nil {
		t.Fatalf("unmarshal initial config.push: %v", err)
	}
	writeConfigAck(t, clientConn, configFrame.RequestID, initialPush.ConfigVersion)

	runningState := waitForObservedState(t, server, listenerFactory, func(observed sharedtestsupport.ObservedState) bool {
		session := findObservedSession(observed.Server, repo.group.ID)
		tunnel := findObservedTunnel(observed.Server, repo.group.ID, 7)
		return session != nil &&
			session.Pending == nil &&
			session.RecoveryMode == sharedtestsupport.RecoveryModeRunning &&
			session.SnapshotVersion == 1 &&
			session.LastAckedConfigVersion == 1 &&
			countObservedListeners(observed.Server, repo.group.ID, 7) == 1 &&
			handleCountForPort(observed, initialListenKey) == 1 &&
			handleCountForPort(observed, recoveredListenKey) == 0 &&
			tunnel != nil &&
			strings.TrimSpace(tunnel.RuntimeIssue) == "" &&
			tunnel.FinalStatus == "启用"
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
		t.Fatalf("unexpected empty config.push during effective_ip flap: %#v", emptyPush)
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
			session.Pending != nil &&
			session.Pending.Version == 2 &&
			session.Pending.TunnelCount == 0 &&
			session.RecoveryMode == sharedtestsupport.RecoveryModePendingEmptyConfig &&
			countObservedListeners(observed.Server, repo.group.ID, 7) == 0 &&
			handleCountForPort(observed, initialListenKey) == 0 &&
			handleCountForPort(observed, recoveredListenKey) == 0 &&
			tunnel != nil &&
			strings.Contains(tunnel.RuntimeIssue, "当前不存在于本机") &&
			tunnel.FinalStatus == "异常"
	})
	pendingEmptySession := requireObservedSession(t, pendingEmptyState.Server, repo.group.ID)
	if pendingEmptySession.Pending == nil || pendingEmptySession.Pending.EffectiveIP != "127.0.0.2" {
		t.Fatalf("expected pending empty config for recovered effective_ip, got %#v", pendingEmptySession)
	}

	network.setSnapshot(localIPv4Snapshot("127.0.0.1", "127.0.0.2"))
	if err := server.scanNonListeningTunnelRuntimeIssues(context.Background()); err != nil {
		t.Fatalf("scan runtime issues while empty config is pending: %v", err)
	}

	if frame, err := readMessageWithin(clientConn, 200*time.Millisecond); err == nil {
		t.Fatalf("expected no recovery config.push while empty config is still pending, got %s", frame.Type.String())
	} else if !isTimeoutError(err) {
		t.Fatalf("expected idle connection while empty config is pending, got %v", err)
	}

	heldEmptyState := waitForObservedState(t, server, listenerFactory, func(observed sharedtestsupport.ObservedState) bool {
		session := findObservedSession(observed.Server, repo.group.ID)
		tunnel := findObservedTunnel(observed.Server, repo.group.ID, 7)
		return session != nil &&
			session.Pending != nil &&
			session.Pending.Version == 2 &&
			session.Pending.TunnelCount == 0 &&
			session.RecoveryMode == sharedtestsupport.RecoveryModePendingEmptyConfig &&
			countObservedListeners(observed.Server, repo.group.ID, 7) == 0 &&
			handleCountForPort(observed, initialListenKey) == 0 &&
			handleCountForPort(observed, recoveredListenKey) == 0 &&
			tunnel != nil &&
			strings.Contains(tunnel.RuntimeIssue, "当前不存在于本机") &&
			tunnel.FinalStatus == "异常"
	})
	if len(heldEmptyState.Server.Listeners) != 0 {
		t.Fatalf("expected no attached listeners while empty config is pending, got %#v", heldEmptyState.Server.Listeners)
	}

	writeConfigAck(t, clientConn, emptyFrame.RequestID, emptyPush.ConfigVersion)

	emptyState := waitForObservedState(t, server, listenerFactory, func(observed sharedtestsupport.ObservedState) bool {
		session := findObservedSession(observed.Server, repo.group.ID)
		tunnel := findObservedTunnel(observed.Server, repo.group.ID, 7)
		return session != nil &&
			session.Pending == nil &&
			session.RecoveryMode == sharedtestsupport.RecoveryModeEmptyConfig &&
			session.SnapshotVersion == 2 &&
			session.SnapshotTunnelCount == 0 &&
			session.LastAckedConfigVersion == 2 &&
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

	scanDone := make(chan error, 1)
	go func() {
		scanDone <- server.scanNonListeningTunnelRuntimeIssues(context.Background())
	}()

	recoveredFrame := readMessage(t, clientConn)
	if recoveredFrame.Type != protocol.TypeConfigPush {
		t.Fatalf("expected recovered full config.push, got %s", recoveredFrame.Type.String())
	}
	recoveredPush, err := protocol.UnmarshalConfigPush(recoveredFrame.Body)
	if err != nil {
		t.Fatalf("unmarshal recovered config.push: %v", err)
	}
	if recoveredPush.ConfigVersion != 2 || len(recoveredPush.Tunnels) != 1 {
		t.Fatalf("unexpected recovered config.push after effective_ip flap: %#v", recoveredPush)
	}

	pendingFullState := waitForObservedState(t, server, listenerFactory, func(observed sharedtestsupport.ObservedState) bool {
		session := findObservedSession(observed.Server, repo.group.ID)
		tunnel := findObservedTunnel(observed.Server, repo.group.ID, 7)
		return session != nil &&
			session.Pending != nil &&
			session.Pending.Version == 2 &&
			session.Pending.TunnelCount == 1 &&
			session.RecoveryMode == sharedtestsupport.RecoveryModePendingFullConfig &&
			countObservedListeners(observed.Server, repo.group.ID, 7) == 0 &&
			handleCountForPort(observed, initialListenKey) == 0 &&
			handleCountForPort(observed, recoveredListenKey) == 0 &&
			tunnel != nil &&
			strings.Contains(tunnel.RuntimeIssue, "当前不存在于本机") &&
			tunnel.FinalStatus == "异常"
	})
	pendingFullSession := requireObservedSession(t, pendingFullState.Server, repo.group.ID)
	if pendingFullSession.Pending == nil || pendingFullSession.Pending.EffectiveIP != "127.0.0.2" {
		t.Fatalf("expected pending full config on recovered effective_ip, got %#v", pendingFullSession)
	}

	network.setSnapshot(localIPv4Snapshot("127.0.0.1"))
	if err := server.scanNonListeningTunnelRuntimeIssues(context.Background()); err != nil {
		t.Fatalf("scan runtime issues after effective_ip becomes invalid again: %v", err)
	}

	invalidFullState := waitForObservedState(t, server, listenerFactory, func(observed sharedtestsupport.ObservedState) bool {
		session := findObservedSession(observed.Server, repo.group.ID)
		tunnel := findObservedTunnel(observed.Server, repo.group.ID, 7)
		return session != nil &&
			session.Pending != nil &&
			session.Pending.Version == 2 &&
			session.Pending.TunnelCount == 1 &&
			session.RecoveryMode == sharedtestsupport.RecoveryModePendingFullConfig &&
			countObservedListeners(observed.Server, repo.group.ID, 7) == 0 &&
			handleCountForPort(observed, initialListenKey) == 0 &&
			handleCountForPort(observed, recoveredListenKey) == 0 &&
			tunnel != nil &&
			strings.Contains(tunnel.RuntimeIssue, "当前不存在于本机") &&
			tunnel.FinalStatus == "异常"
	})
	if len(invalidFullState.Server.Listeners) != 0 {
		t.Fatalf("expected no attached listeners while full recovery config is pending on invalid effective_ip, got %#v", invalidFullState.Server.Listeners)
	}

	network.setSnapshot(localIPv4Snapshot("127.0.0.1", "127.0.0.2"))
	if err := server.scanNonListeningTunnelRuntimeIssues(context.Background()); err != nil {
		t.Fatalf("scan runtime issues after effective_ip becomes valid again: %v", err)
	}

	if frame, err := readMessageWithin(clientConn, 200*time.Millisecond); err == nil {
		t.Fatalf("expected no duplicate recovery config.push while full config is pending, got %s", frame.Type.String())
	} else if !isTimeoutError(err) {
		t.Fatalf("expected idle connection while full config is pending, got %v", err)
	}

	heldFullState := waitForObservedState(t, server, listenerFactory, func(observed sharedtestsupport.ObservedState) bool {
		session := findObservedSession(observed.Server, repo.group.ID)
		tunnel := findObservedTunnel(observed.Server, repo.group.ID, 7)
		return session != nil &&
			session.Pending != nil &&
			session.Pending.Version == 2 &&
			session.Pending.TunnelCount == 1 &&
			session.RecoveryMode == sharedtestsupport.RecoveryModePendingFullConfig &&
			countObservedListeners(observed.Server, repo.group.ID, 7) == 0 &&
			handleCountForPort(observed, initialListenKey) == 0 &&
			handleCountForPort(observed, recoveredListenKey) == 0 &&
			tunnel != nil &&
			strings.Contains(tunnel.RuntimeIssue, "当前不存在于本机") &&
			tunnel.FinalStatus == "异常"
	})
	if len(heldFullState.Server.Listeners) != 0 {
		t.Fatalf("expected no attached listeners while full config remains pending, got %#v", heldFullState.Server.Listeners)
	}

	controller := testhooks.NewController()
	controller.AddBarrier("control.config_ack.before_accept", 1)
	controller.AddBarrier("control.listener.before_bind", 1)
	restoreHooks := testhooks.Install(controller)
	defer restoreHooks()

	writeConfigAck(t, clientConn, recoveredFrame.RequestID, recoveredPush.ConfigVersion)
	waitForTestHook(t, controller, "control.config_ack.before_accept", 1)

	ackHeldState := waitForObservedState(t, server, listenerFactory, func(observed sharedtestsupport.ObservedState) bool {
		session := findObservedSession(observed.Server, repo.group.ID)
		tunnel := findObservedTunnel(observed.Server, repo.group.ID, 7)
		return session != nil &&
			session.Pending != nil &&
			session.Pending.Version == 2 &&
			session.Pending.TunnelCount == 1 &&
			countObservedListeners(observed.Server, repo.group.ID, 7) == 0 &&
			handleCountForPort(observed, initialListenKey) == 0 &&
			handleCountForPort(observed, recoveredListenKey) == 0 &&
			tunnel != nil &&
			strings.Contains(tunnel.RuntimeIssue, "当前不存在于本机") &&
			tunnel.FinalStatus == "异常"
	})
	if ackHeldState.Server.GroupSlots[repo.group.ID] == 0 {
		t.Fatalf("expected group slot to stay occupied while recovery ack is held, got %#v", ackHeldState.Server.GroupSlots)
	}

	if !controller.Release("control.config_ack.before_accept", 1) {
		t.Fatal("release config_ack.before_accept barrier failed")
	}
	bindHit := waitForTestHookHit(t, controller, "control.listener.before_bind", 1)
	if got := bindHit.Fields["kind"]; got != "runtime_start" {
		t.Fatalf("expected recovery bind after full config ack, got %#v", bindHit.Fields)
	}

	beforeBindState := waitForObservedState(t, server, listenerFactory, func(observed sharedtestsupport.ObservedState) bool {
		session := findObservedSession(observed.Server, repo.group.ID)
		tunnel := findObservedTunnel(observed.Server, repo.group.ID, 7)
		return session != nil &&
			session.Pending == nil &&
			session.SnapshotVersion == 2 &&
			session.LastAckedConfigVersion == 2 &&
			countObservedListeners(observed.Server, repo.group.ID, 7) == 0 &&
			handleCountForPort(observed, initialListenKey) == 0 &&
			handleCountForPort(observed, recoveredListenKey) == 0 &&
			tunnel != nil &&
			strings.Contains(tunnel.RuntimeIssue, "当前不存在于本机") &&
			tunnel.FinalStatus == "异常"
	})
	if beforeBindState.Server.GroupSlots[repo.group.ID] == 0 {
		t.Fatalf("expected group slot to remain occupied before recovery bind completes, got %#v", beforeBindState.Server.GroupSlots)
	}

	if !controller.Release("control.listener.before_bind", 1) {
		t.Fatal("release listener.before_bind barrier failed")
	}

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
		t.Fatalf("expected exactly one recovered listener after effective_ip flap, got %#v", finalState.Server.Listeners)
	}
	if issues := server.TunnelRuntimeIssues(); len(issues) != 0 {
		t.Fatalf("expected runtime issues to clear only after full recovery completes, got %#v", issues)
	}

	assertHeartbeatStillWorks(t, clientConn)

	_ = clientConn.Close()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("server connection did not exit")
	}
}

func waitForTestHook(t *testing.T, controller *testhooks.Controller, point string, hitIndex int) {
	t.Helper()

	_ = waitForTestHookHit(t, controller, point, hitIndex)
}

func waitForTestHookHit(t *testing.T, controller *testhooks.Controller, point string, hitIndex int) testhooks.Hit {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	hit, err := controller.WaitUntilHit(ctx, point, hitIndex)
	if err != nil {
		t.Fatalf("wait for hook %s[%d]: %v", point, hitIndex, err)
	}
	return hit
}

func requireObservedSession(t *testing.T, state sharedtestsupport.ServerObservedState, groupID int64) sharedtestsupport.SessionObservedState {
	t.Helper()

	for _, session := range state.Sessions {
		if session.GroupID == groupID {
			return session
		}
	}
	t.Fatalf("observed session for group %d not found: %#v", groupID, state.Sessions)
	return sharedtestsupport.SessionObservedState{}
}

func countObservedListeners(state sharedtestsupport.ServerObservedState, groupID int64, tunnelID uint32) int {
	count := 0
	for _, listener := range state.Listeners {
		if listener.GroupID == groupID && listener.TunnelID == tunnelID {
			count++
		}
	}
	return count
}

func observedListenerBindIP(state sharedtestsupport.ServerObservedState, groupID int64, tunnelID uint32) string {
	for _, listener := range state.Listeners {
		if listener.GroupID == groupID && listener.TunnelID == tunnelID {
			return listener.BindIP
		}
	}
	return ""
}

func waitForTCPDialAddress(t *testing.T, address string) net.Conn {
	t.Helper()

	deadline := time.Now().Add(2 * time.Second)
	for {
		conn, err := net.DialTimeout("tcp", address, 100*time.Millisecond)
		if err == nil {
			return conn
		}
		if time.Now().After(deadline) {
			t.Fatalf("dial %s: %v", address, err)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
