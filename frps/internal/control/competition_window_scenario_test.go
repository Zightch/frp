//go:build testhooks

package control

import (
	"context"
	"io"
	"log/slog"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/zightch/frp/frps/internal/testhooks"
	"github.com/zightch/frp/frps/pkg/protocol"
	sharedtestsupport "github.com/zightch/frp/frps/pkg/testsupport"
	"github.com/zightch/frp/frps/pkg/transport"
)

func TestServerScenarioLateDelayedAckFromClosedSessionDoesNotPolluteReplacementSession(t *testing.T) {
	tokenID, tokenHash := fixedTestToken()
	host := mustParseTestHost(t, "127.0.0.1")

	initialKey := ListenKey{Protocol: "tcp", IP: "127.0.0.1", Port: uint16(freeTCPPort(t))}
	replacementKey := ListenKey{Protocol: "tcp", IP: "127.0.0.1", Port: uint16(freeTCPPortExcept(t, int(initialKey.Port)))}
	listenerFactory := NewScriptedListenerFactory()
	frameIO := transport.NewScriptedFrameIO()
	frameIO.AddRule(transport.ScriptedRule{
		ConnID:     "old-session",
		Direction:  sharedtestsupport.FrameDirectionClientToServer,
		FrameType:  protocol.TypeConfigAck,
		Occurrence: 1,
		Action:     sharedtestsupport.FrameActionDelay,
	})

	repo := &scriptedRuntimeRepository{
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
						RemoteStart: initialKey.Port,
						RemoteEnd:   initialKey.Port,
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
			FrameIO:         frameIO,
		},
		slog.New(slog.NewTextHandler(io.Discard, nil)),
		"test-server",
	)

	oldClient, oldDone, initialFrame := authenticateScriptedServerSession(t, server, frameIO, "old-session", tokenID, tokenHash)
	defer oldClient.Close()

	initialPush, err := protocol.UnmarshalConfigPush(initialFrame.Body)
	if err != nil {
		t.Fatalf("unmarshal initial config.push: %v", err)
	}
	oldClient.writeConfigAck(t, initialFrame.RequestID, initialPush.ConfigVersion)

	pendingState := waitForObservedState(t, server, listenerFactory, func(observed sharedtestsupport.ObservedState) bool {
		session := findObservedSession(observed.Server, repo.group.ID)
		return session != nil &&
			session.Pending != nil &&
			session.Pending.Version == 1 &&
			session.Pending.TunnelCount == 1 &&
			countObservedListeners(observed.Server, repo.group.ID, 7) == 0 &&
			handleCountForPort(observed, initialKey) == 0 &&
			handleCountForPort(observed, replacementKey) == 0
	})
	if pendingState.Server.GroupSlots[repo.group.ID] == 0 {
		t.Fatalf("expected pending old session to occupy group slot, got %#v", pendingState.Server.GroupSlots)
	}

	repo.SetGroup(GroupRuntime{
		ID:          1,
		Name:        "group-a",
		Enabled:     true,
		EffectiveIP: "127.0.0.1",
		TokenHash:   tokenHash,
		Snapshot: ConfigSnapshot{
			Version:       2,
			GeneratedAtMs: 200,
			Tunnels: []protocol.TunnelEntry{
				{
					TunnelID:    7,
					Protocol:    protocol.ProtocolTCP,
					TunnelFlags: protocol.TunnelFlagEnabled,
					RemoteStart: replacementKey.Port,
					RemoteEnd:   replacementKey.Port,
					LocalHost:   host,
					LocalStart:  2200,
					LocalEnd:    2200,
				},
			},
		},
	})
	server.RefreshGroup(repo.group.ID)

	select {
	case <-oldDone:
	case <-time.After(2 * time.Second):
		t.Fatal("old pending session did not exit after refresh forced replacement")
	}

	stoppedState := waitForObservedState(t, server, listenerFactory, func(observed sharedtestsupport.ObservedState) bool {
		return len(observed.Server.Sessions) == 0 &&
			len(observed.Server.GroupSlots) == 0 &&
			countObservedListeners(observed.Server, repo.group.ID, 7) == 0 &&
			handleCountForPort(observed, initialKey) == 0 &&
			handleCountForPort(observed, replacementKey) == 0
	})
	if len(stoppedState.Server.Listeners) != 0 {
		t.Fatalf("expected no listeners after old pending session closes, got %#v", stoppedState.Server.Listeners)
	}

	newClient, newDone, replacementFrame := authenticateScriptedServerSession(t, server, frameIO, "new-session", tokenID, tokenHash)
	defer newClient.Close()

	replacementPush, err := protocol.UnmarshalConfigPush(replacementFrame.Body)
	if err != nil {
		t.Fatalf("unmarshal replacement config.push: %v", err)
	}
	if replacementPush.ConfigVersion != 2 || len(replacementPush.Tunnels) != 1 || replacementPush.Tunnels[0].RemoteStart != replacementKey.Port {
		t.Fatalf("unexpected replacement config.push: %#v", replacementPush)
	}
	newClient.writeConfigAck(t, replacementFrame.RequestID, replacementPush.ConfigVersion)

	runningState := waitForObservedState(t, server, listenerFactory, func(observed sharedtestsupport.ObservedState) bool {
		session := findObservedSession(observed.Server, repo.group.ID)
		return session != nil &&
			session.Pending == nil &&
			session.SnapshotVersion == 2 &&
			session.LastAckedConfigVersion == 2 &&
			session.RecoveryMode == sharedtestsupport.RecoveryModeRunning &&
			countObservedListeners(observed.Server, repo.group.ID, 7) == 1 &&
			handleCountForPort(observed, initialKey) == 0 &&
			handleCountForPort(observed, replacementKey) == 1
	})
	runningSession := requireObservedSession(t, runningState.Server, repo.group.ID)
	if runningState.Server.GroupSlots[repo.group.ID] != runningSession.SessionID {
		t.Fatalf("expected replacement session to own group slot, got %#v", runningState.Server.GroupSlots)
	}

	if released := frameIO.ReleaseDelayed(transport.ReleaseFilter{
		ConnID:    "old-session",
		Direction: sharedtestsupport.FrameDirectionClientToServer,
		FrameType: protocol.TypeConfigAck.String(),
	}); released != 0 {
		t.Fatalf("expected delayed old config.ack to stay undeliverable after replacement, got %d", released)
	}
	assertNoExtraScriptedControlFrame(t, newClient, "expected no old-session config.ack to leak into replacement session")
	assertScriptedHeartbeatStillWorks(t, newClient)

	_ = newClient.Close()
	select {
	case <-newDone:
	case <-time.After(2 * time.Second):
		t.Fatal("replacement session did not exit")
	}
}

func TestServerScenarioRejectsOutOfOrderRecoveryAckWithoutLeavingPendingOrListeners(t *testing.T) {
	tokenID, tokenHash := fixedTestToken()
	host := mustParseTestHost(t, "127.0.0.1")

	listenKey := ListenKey{Protocol: "tcp", IP: "127.0.0.1", Port: uint16(freeTCPPort(t))}
	listenerFactory := NewScriptedListenerFactory()
	repo := &scriptedRuntimeRepository{
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
		ID:          1,
		Name:        "group-a",
		Enabled:     true,
		EffectiveIP: "127.0.0.1",
		TokenHash:   tokenHash,
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

	refreshDone := make(chan struct{})
	go func() {
		defer close(refreshDone)
		server.RefreshGroup(repo.group.ID)
	}()

	recoveryFrame := readMessage(t, clientConn)
	if recoveryFrame.Type != protocol.TypeConfigPush {
		t.Fatalf("expected recovery config.push, got %s", recoveryFrame.Type.String())
	}
	recoveryPush, err := protocol.UnmarshalConfigPush(recoveryFrame.Body)
	if err != nil {
		t.Fatalf("unmarshal recovery config.push: %v", err)
	}
	if recoveryPush.ConfigVersion != 2 || len(recoveryPush.Tunnels) != 1 {
		t.Fatalf("unexpected recovery config.push: %#v", recoveryPush)
	}

	writeMessage(t, clientConn, buildConfigAckFrame(t, recoveryFrame.RequestID, 0, 1, protocol.StatusOK, ""))

	errorFrame := readMessage(t, clientConn)
	if errorFrame.Type != protocol.TypeError {
		t.Fatalf("expected error frame for out-of-order ack, got %s", errorFrame.Type.String())
	}
	errorBody, err := protocol.UnmarshalErrorBody(errorFrame.Body)
	if err != nil {
		t.Fatalf("unmarshal error frame: %v", err)
	}
	if errorBody.ErrorCode != protocol.ErrorCodeProtocolBadBody || !strings.Contains(errorBody.Message, "config.ack version mismatch") {
		t.Fatalf("unexpected out-of-order ack error: %#v", errorBody)
	}

	select {
	case <-refreshDone:
	case <-time.After(time.Second):
		t.Fatal("refresh did not complete before stale ack shutdown")
	}
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("session did not exit after out-of-order ack")
	}

	stoppedState := waitForObservedState(t, server, listenerFactory, func(observed sharedtestsupport.ObservedState) bool {
		return len(observed.Server.Sessions) == 0 &&
			len(observed.Server.GroupSlots) == 0 &&
			countObservedListeners(observed.Server, repo.group.ID, 7) == 0 &&
			handleCountForPort(observed, listenKey) == 0
	})
	if len(stoppedState.Server.Listeners) != 0 {
		t.Fatalf("expected no listeners after out-of-order ack closes session, got %#v", stoppedState.Server.Listeners)
	}

	replacementConn, replacementDone, replacementFrame := authenticateServerSession(t, server, tokenID, tokenHash)
	defer replacementConn.Close()

	replacementPush, err := protocol.UnmarshalConfigPush(replacementFrame.Body)
	if err != nil {
		t.Fatalf("unmarshal replacement config.push: %v", err)
	}
	if replacementPush.ConfigVersion != 2 || len(replacementPush.Tunnels) != 1 {
		t.Fatalf("unexpected replacement config.push: %#v", replacementPush)
	}
	writeConfigAck(t, replacementConn, replacementFrame.RequestID, replacementPush.ConfigVersion)

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
		t.Fatalf("expected replacement session to own group slot after stale ack shutdown, got %#v", finalState.Server.GroupSlots)
	}

	assertHeartbeatStillWorks(t, replacementConn)

	_ = replacementConn.Close()
	select {
	case <-replacementDone:
	case <-time.After(2 * time.Second):
		t.Fatal("replacement session did not exit")
	}
}

func TestServerScenarioRejectsDuplicateRefreshAckWithoutLeavingListenersOrPending(t *testing.T) {
	tokenID, tokenHash := fixedTestToken()
	host := mustParseTestHost(t, "127.0.0.1")

	listenKey := ListenKey{Protocol: "tcp", IP: "127.0.0.1", Port: uint16(freeTCPPort(t))}
	listenerFactory := NewScriptedListenerFactory()
	repo := &scriptedRuntimeRepository{
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

	clientConn, done, initialFrame := authenticateServerSession(t, server, tokenID, tokenHash)
	defer clientConn.Close()

	initialPush, err := protocol.UnmarshalConfigPush(initialFrame.Body)
	if err != nil {
		t.Fatalf("unmarshal initial config.push: %v", err)
	}
	writeConfigAck(t, clientConn, initialFrame.RequestID, initialPush.ConfigVersion)
	waitForActiveGroupSession(t, server, repo.group.ID)

	active, ok := server.activeSession(repo.group.ID)
	if !ok || active == nil || active.session == nil {
		t.Fatalf("active session for group %d not found", repo.group.ID)
	}
	waitForIdleConfig(t, active.session)

	repo.SetGroup(GroupRuntime{
		ID:          1,
		Name:        "group-a",
		Enabled:     true,
		EffectiveIP: "127.0.0.1",
		TokenHash:   tokenHash,
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

	refreshDone := make(chan struct{})
	go func() {
		defer close(refreshDone)
		server.RefreshGroup(repo.group.ID)
	}()

	refreshFrame := readMessage(t, clientConn)
	if refreshFrame.Type != protocol.TypeConfigPush {
		t.Fatalf("expected refresh config.push, got %s", refreshFrame.Type.String())
	}
	refreshPush, err := protocol.UnmarshalConfigPush(refreshFrame.Body)
	if err != nil {
		t.Fatalf("unmarshal refresh config.push: %v", err)
	}
	if refreshPush.ConfigVersion != 2 || len(refreshPush.Tunnels) != 1 {
		t.Fatalf("unexpected refresh config.push: %#v", refreshPush)
	}
	writeConfigAck(t, clientConn, refreshFrame.RequestID, refreshPush.ConfigVersion)

	select {
	case <-refreshDone:
	case <-time.After(time.Second):
		t.Fatal("refresh did not complete")
	}

	runningState := waitForObservedState(t, server, listenerFactory, func(observed sharedtestsupport.ObservedState) bool {
		session := findObservedSession(observed.Server, repo.group.ID)
		return session != nil &&
			session.Pending == nil &&
			session.SnapshotVersion == 2 &&
			session.LastAckedConfigVersion == 2 &&
			session.RecoveryMode == sharedtestsupport.RecoveryModeRunning &&
			countObservedListeners(observed.Server, repo.group.ID, 7) == 1 &&
			handleCountForPort(observed, listenKey) == 1
	})
	runningSession := requireObservedSession(t, runningState.Server, repo.group.ID)
	if runningState.Server.GroupSlots[repo.group.ID] != runningSession.SessionID {
		t.Fatalf("expected running refresh session to keep group slot, got %#v", runningState.Server.GroupSlots)
	}

	writeConfigAck(t, clientConn, refreshFrame.RequestID, refreshPush.ConfigVersion)

	errorFrame := readMessage(t, clientConn)
	if errorFrame.Type != protocol.TypeError {
		t.Fatalf("expected duplicate refresh ack to yield error frame, got %s", errorFrame.Type.String())
	}
	errorBody, err := protocol.UnmarshalErrorBody(errorFrame.Body)
	if err != nil {
		t.Fatalf("unmarshal duplicate refresh ack error: %v", err)
	}
	if errorBody.ErrorCode != protocol.ErrorCodeProtocolBadBody || !strings.Contains(errorBody.Message, "unexpected config.ack requestId") {
		t.Fatalf("unexpected duplicate refresh ack error: %#v", errorBody)
	}

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("session did not exit after duplicate refresh ack")
	}

	stoppedState := waitForObservedState(t, server, listenerFactory, func(observed sharedtestsupport.ObservedState) bool {
		return len(observed.Server.Sessions) == 0 &&
			len(observed.Server.GroupSlots) == 0 &&
			countObservedListeners(observed.Server, repo.group.ID, 7) == 0 &&
			handleCountForPort(observed, listenKey) == 0
	})
	if len(stoppedState.Server.Listeners) != 0 {
		t.Fatalf("expected no listeners after duplicate refresh ack closes session, got %#v", stoppedState.Server.Listeners)
	}
}

func TestServerScenarioDelayedOldHeartbeatAndErrorFramesDoNotAffectReplacementSession(t *testing.T) {
	tokenID, tokenHash := fixedTestToken()
	host := mustParseTestHost(t, "127.0.0.1")

	listenKey := ListenKey{Protocol: "tcp", IP: "127.0.0.1", Port: uint16(freeTCPPort(t))}
	listenerFactory := NewScriptedListenerFactory()
	frameIO := transport.NewScriptedFrameIO()
	frameIO.AddRule(transport.ScriptedRule{
		ConnID:     "old-session",
		Direction:  sharedtestsupport.FrameDirectionClientToServer,
		FrameType:  protocol.TypeHeartbeatPing,
		Occurrence: 1,
		Action:     sharedtestsupport.FrameActionDelay,
	})
	frameIO.AddRule(transport.ScriptedRule{
		ConnID:     "old-session",
		Direction:  sharedtestsupport.FrameDirectionServerToClient,
		FrameType:  protocol.TypeError,
		Occurrence: 1,
		Action:     sharedtestsupport.FrameActionDelay,
	})

	repo := &scriptedRuntimeRepository{
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
			FrameIO:         frameIO,
		},
		slog.New(slog.NewTextHandler(io.Discard, nil)),
		"test-server",
	)

	oldClient, oldDone, initialFrame := authenticateScriptedServerSession(t, server, frameIO, "old-session", tokenID, tokenHash)
	defer oldClient.Close()

	initialPush, err := protocol.UnmarshalConfigPush(initialFrame.Body)
	if err != nil {
		t.Fatalf("unmarshal initial config.push: %v", err)
	}
	oldClient.writeConfigAck(t, initialFrame.RequestID, initialPush.ConfigVersion)

	runningState := waitForObservedState(t, server, listenerFactory, func(observed sharedtestsupport.ObservedState) bool {
		session := findObservedSession(observed.Server, repo.group.ID)
		return session != nil &&
			session.Pending == nil &&
			session.SnapshotVersion == 1 &&
			session.LastAckedConfigVersion == 1 &&
			session.RecoveryMode == sharedtestsupport.RecoveryModeRunning &&
			countObservedListeners(observed.Server, repo.group.ID, 7) == 1 &&
			handleCountForPort(observed, listenKey) == 1
	})
	oldSession := requireObservedSession(t, runningState.Server, repo.group.ID)
	if runningState.Server.GroupSlots[repo.group.ID] != oldSession.SessionID {
		t.Fatalf("expected old session to own group slot before reconnect, got %#v", runningState.Server.GroupSlots)
	}

	oldClient.writeHeartbeatPing(t, 11)
	oldClient.writeConfigAck(t, initialFrame.RequestID, initialPush.ConfigVersion)

	select {
	case <-oldDone:
	case <-time.After(2 * time.Second):
		t.Fatal("old session did not exit after delayed heartbeat plus duplicate ack")
	}

	stoppedState := waitForObservedState(t, server, listenerFactory, func(observed sharedtestsupport.ObservedState) bool {
		return len(observed.Server.Sessions) == 0 &&
			len(observed.Server.GroupSlots) == 0 &&
			countObservedListeners(observed.Server, repo.group.ID, 7) == 0 &&
			handleCountForPort(observed, listenKey) == 0
	})
	if len(stoppedState.Server.Listeners) != 0 {
		t.Fatalf("expected no listeners after old session exits, got %#v", stoppedState.Server.Listeners)
	}

	newClient, newDone, replacementFrame := authenticateScriptedServerSession(t, server, frameIO, "new-session", tokenID, tokenHash)
	defer newClient.Close()

	replacementPush, err := protocol.UnmarshalConfigPush(replacementFrame.Body)
	if err != nil {
		t.Fatalf("unmarshal replacement config.push: %v", err)
	}
	if replacementPush.ConfigVersion != 1 || len(replacementPush.Tunnels) != 1 {
		t.Fatalf("unexpected replacement config.push: %#v", replacementPush)
	}
	newClient.writeConfigAck(t, replacementFrame.RequestID, replacementPush.ConfigVersion)

	replacementState := waitForObservedState(t, server, listenerFactory, func(observed sharedtestsupport.ObservedState) bool {
		session := findObservedSession(observed.Server, repo.group.ID)
		return session != nil &&
			session.Pending == nil &&
			session.SnapshotVersion == 1 &&
			session.LastAckedConfigVersion == 1 &&
			session.RecoveryMode == sharedtestsupport.RecoveryModeRunning &&
			countObservedListeners(observed.Server, repo.group.ID, 7) == 1 &&
			handleCountForPort(observed, listenKey) == 1
	})
	replacementSession := requireObservedSession(t, replacementState.Server, repo.group.ID)
	if replacementState.Server.GroupSlots[repo.group.ID] != replacementSession.SessionID {
		t.Fatalf("expected replacement session to own group slot, got %#v", replacementState.Server.GroupSlots)
	}

	if released := frameIO.ReleaseDelayed(transport.ReleaseFilter{
		ConnID:    "old-session",
		Direction: sharedtestsupport.FrameDirectionClientToServer,
		FrameType: protocol.TypeHeartbeatPing.String(),
	}); released != 0 {
		t.Fatalf("expected delayed old heartbeat to stay undeliverable after replacement, got %d", released)
	}
	if released := frameIO.ReleaseDelayed(transport.ReleaseFilter{
		ConnID:    "old-session",
		Direction: sharedtestsupport.FrameDirectionServerToClient,
		FrameType: protocol.TypeError.String(),
	}); released != 0 {
		t.Fatalf("expected delayed old error frame to stay on closed old connection, got %d", released)
	}

	assertNoExtraScriptedControlFrame(t, newClient, "expected no delayed old-session frame to leak into replacement session")
	assertScriptedHeartbeatStillWorks(t, newClient)

	_ = newClient.Close()
	select {
	case <-newDone:
	case <-time.After(2 * time.Second):
		t.Fatal("replacement session did not exit")
	}
}

func TestServerUnregisterOldSessionKeepsReplacementSlotAndSession(t *testing.T) {
	server := NewServer(
		Options{},
		slog.New(slog.NewTextHandler(io.Discard, nil)),
		"test-server",
	)

	group := GroupRuntime{ID: 1, Name: "group-a"}
	oldSession := newTestSessionState(group, ConfigSnapshot{Version: 1})
	oldSession.ID = 1
	newSession := newTestSessionState(group, ConfigSnapshot{Version: 2})
	newSession.ID = 2

	oldClient, oldServer := net.Pipe()
	defer oldClient.Close()
	defer oldServer.Close()
	newClient, newServer := net.Pipe()
	defer newClient.Close()
	defer newServer.Close()

	server.registerActiveSession(oldServer, oldSession)
	server.mu.Lock()
	server.groupSlots[group.ID] = oldSession.ID
	server.mu.Unlock()

	server.registerActiveSession(newServer, newSession)
	server.mu.Lock()
	server.groupSlots[group.ID] = newSession.ID
	server.mu.Unlock()

	server.unregisterActiveSession(oldSession)

	state := server.ObserveState()
	if got := state.GroupSlots[group.ID]; got != newSession.ID {
		t.Fatalf("expected replacement session to keep group slot, got %#v", state.GroupSlots)
	}
	if len(state.Sessions) != 1 || state.Sessions[0].SessionID != newSession.ID {
		t.Fatalf("expected replacement session to remain active, got %#v", state.Sessions)
	}
}

func TestServerScenarioReleasingBlockedStartupBindAfterClientDisconnectLeavesNoListenerLeak(t *testing.T) {
	controller := testhooks.NewController()
	controller.AddBarrier("control.listener.before_bind", 1)
	restoreHooks := testhooks.Install(controller)
	defer restoreHooks()

	tokenID, tokenHash := fixedTestToken()
	host := mustParseTestHost(t, "127.0.0.1")

	listenKey := ListenKey{Protocol: "tcp", IP: "127.0.0.1", Port: uint16(freeTCPPort(t))}
	listenerFactory := NewScriptedListenerFactory()
	repo := &scriptedRuntimeRepository{
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
		t.Fatalf("expected blocked bind to come from runtime_start, got %#v", bindHit.Fields)
	}

	_ = clientConn.Close()
	if !controller.Release("control.listener.before_bind", 1) {
		t.Fatal("release startup bind barrier failed")
	}

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("session did not exit after client disconnect raced with blocked bind")
	}

	finalState := waitForObservedState(t, server, listenerFactory, func(observed sharedtestsupport.ObservedState) bool {
		return len(observed.Server.Sessions) == 0 &&
			len(observed.Server.GroupSlots) == 0 &&
			countObservedListeners(observed.Server, repo.group.ID, 7) == 0 &&
			handleCountForPort(observed, listenKey) == 0
	})
	if len(finalState.Server.Listeners) != 0 {
		t.Fatalf("expected no listener leak after client disconnect race, got %#v", finalState.Server.Listeners)
	}
	assertNoListenerHandles(t, finalState, listenKey)
}

func TestServerScenarioReleasingBlockedStartupBindAfterDisableRefreshDoesNotAttachStaleListener(t *testing.T) {
	controller := testhooks.NewController()
	controller.AddBarrier("control.config_ack.before_accept", 2)
	controller.AddBarrier("control.listener.before_bind", 1)
	restoreHooks := testhooks.Install(controller)
	defer restoreHooks()

	tokenID, tokenHash := fixedTestToken()
	host := mustParseTestHost(t, "127.0.0.1")

	listenKey := ListenKey{Protocol: "tcp", IP: "127.0.0.1", Port: uint16(freeTCPPort(t))}
	listenerFactory := NewScriptedListenerFactory()
	repo := &scriptedRuntimeRepository{
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
		t.Fatalf("expected blocked bind to come from runtime_start, got %#v", bindHit.Fields)
	}

	repo.SetGroup(GroupRuntime{
		ID:          1,
		Name:        "group-a",
		Enabled:     false,
		EffectiveIP: "127.0.0.1",
		TokenHash:   tokenHash,
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

	refreshDone := make(chan struct{})
	go func() {
		defer close(refreshDone)
		server.RefreshGroup(repo.group.ID)
	}()

	emptyFrame := readMessage(t, clientConn)
	if emptyFrame.Type != protocol.TypeConfigPush {
		t.Fatalf("expected disable refresh to push empty config, got %s", emptyFrame.Type.String())
	}
	emptyPush, err := protocol.UnmarshalConfigPush(emptyFrame.Body)
	if err != nil {
		t.Fatalf("unmarshal disable empty config.push: %v", err)
	}
	if emptyPush.ConfigVersion != 2 || len(emptyPush.Tunnels) != 0 {
		t.Fatalf("unexpected disable empty config.push: %#v", emptyPush)
	}

	pendingState := waitForObservedState(t, server, listenerFactory, func(observed sharedtestsupport.ObservedState) bool {
		session := findObservedSession(observed.Server, repo.group.ID)
		return session != nil &&
			session.Pending != nil &&
			session.Pending.Version == 2 &&
			session.Pending.TunnelCount == 0 &&
			session.RecoveryMode == sharedtestsupport.RecoveryModePendingEmptyConfig &&
			countObservedListeners(observed.Server, repo.group.ID, 7) == 0 &&
			handleCountForPort(observed, listenKey) == 0
	})
	if pendingState.Server.GroupSlots[repo.group.ID] == 0 {
		t.Fatalf("expected disable refresh to keep group slot while empty config is pending, got %#v", pendingState.Server.GroupSlots)
	}

	if !controller.Release("control.listener.before_bind", 1) {
		t.Fatal("release blocked startup bind after disable refresh failed")
	}
	writeConfigAck(t, clientConn, emptyFrame.RequestID, emptyPush.ConfigVersion)
	waitForTestHook(t, controller, "control.config_ack.before_accept", 2)
	if !controller.Release("control.config_ack.before_accept", 2) {
		t.Fatal("release disable refresh config_ack.before_accept barrier failed")
	}

	select {
	case <-refreshDone:
	case <-time.After(time.Second):
		t.Fatal("disable refresh did not complete")
	}

	finalState := waitForObservedState(t, server, listenerFactory, func(observed sharedtestsupport.ObservedState) bool {
		session := findObservedSession(observed.Server, repo.group.ID)
		return session != nil &&
			session.Pending == nil &&
			session.SnapshotVersion == 2 &&
			session.SnapshotTunnelCount == 0 &&
			countObservedListeners(observed.Server, repo.group.ID, 7) == 0 &&
			handleCountForPort(observed, listenKey) == 0
	})
	finalSession := requireObservedSession(t, finalState.Server, repo.group.ID)
	if finalState.Server.GroupSlots[repo.group.ID] != finalSession.SessionID {
		t.Fatalf("expected disabled session to keep group slot, got %#v", finalState.Server.GroupSlots)
	}
	assertNoListenerHandles(t, finalState, listenKey)
	assertHeartbeatStillWorks(t, clientConn)

	_ = clientConn.Close()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("disabled session did not exit")
	}
}

func TestServerScenarioReleasingBlockedStartupBindAfterGroupDeletionLeavesNoListenerLeak(t *testing.T) {
	controller := testhooks.NewController()
	controller.AddBarrier("control.listener.before_bind", 1)
	restoreHooks := testhooks.Install(controller)
	defer restoreHooks()

	tokenID, tokenHash := fixedTestToken()
	host := mustParseTestHost(t, "127.0.0.1")

	listenKey := ListenKey{Protocol: "tcp", IP: "127.0.0.1", Port: uint16(freeTCPPort(t))}
	listenerFactory := NewScriptedListenerFactory()
	repo := &deletableRuntimeRepository{
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
		t.Fatalf("expected blocked bind to come from runtime_start, got %#v", bindHit.Fields)
	}

	repo.Delete()
	server.RefreshGroup(repo.group.ID)

	if !controller.Release("control.listener.before_bind", 1) {
		t.Fatal("release blocked startup bind after group deletion failed")
	}

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("session did not exit after group deletion raced with blocked bind")
	}

	finalState := waitForObservedState(t, server, listenerFactory, func(observed sharedtestsupport.ObservedState) bool {
		return len(observed.Server.Sessions) == 0 &&
			len(observed.Server.GroupSlots) == 0 &&
			countObservedListeners(observed.Server, repo.group.ID, 7) == 0 &&
			handleCountForPort(observed, listenKey) == 0
	})
	if len(finalState.Server.Listeners) != 0 {
		t.Fatalf("expected no listeners after group deletion race, got %#v", finalState.Server.Listeners)
	}
	assertNoListenerHandles(t, finalState, listenKey)
}

type scriptedControlClient struct {
	conn      net.Conn
	frames    *transport.ScriptedFrameIO
	connID    string
	sessionID uint64
}

func authenticateScriptedServerSession(
	t *testing.T,
	server *Server,
	frames *transport.ScriptedFrameIO,
	connID string,
	tokenID [16]byte,
	tokenHash [32]byte,
) (*scriptedControlClient, chan struct{}, protocol.Frame) {
	t.Helper()

	clientConn, serverConn := frames.OpenConnPair(connID)
	client := &scriptedControlClient{
		conn:   clientConn,
		frames: frames,
		connID: connID,
	}

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
	client.writeMessage(t, protocol.Frame{
		Type:      protocol.TypeAuthBegin,
		RequestID: 1,
		Body:      authBeginBody,
	})

	challengeFrame := client.readMessage(t)
	if challengeFrame.Type != protocol.TypeAuthChallenge {
		t.Fatalf("expected auth.challenge, got %s", challengeFrame.Type.String())
	}
	challenge, err := protocol.UnmarshalAuthChallenge(challengeFrame.Body)
	if err != nil {
		t.Fatalf("unmarshal auth.challenge: %v", err)
	}

	authFinishBody, err := protocol.MarshalAuthFinish(protocol.AuthFinish{
		ChallengeID: challenge.ChallengeID,
		Response:    protocol.ChallengeResponse(tokenHash, challenge.Nonce),
	})
	if err != nil {
		t.Fatalf("marshal auth.finish: %v", err)
	}
	client.writeMessage(t, protocol.Frame{
		Type:      protocol.TypeAuthFinish,
		RequestID: 2,
		Body:      authFinishBody,
	})

	helloFrame := client.readMessage(t)
	if helloFrame.Type != protocol.TypeServerHello {
		t.Fatalf("expected server.hello, got %s", helloFrame.Type.String())
	}
	hello, err := protocol.UnmarshalServerHello(helloFrame.Body)
	if err != nil {
		t.Fatalf("unmarshal server.hello: %v", err)
	}
	client.sessionID = hello.SessionID

	configFrame := client.readMessage(t)
	if configFrame.Type != protocol.TypeConfigPush {
		t.Fatalf("expected config.push, got %s", configFrame.Type.String())
	}

	return client, done, configFrame
}

func (c *scriptedControlClient) Close() error {
	if c == nil || c.conn == nil {
		return nil
	}
	return c.conn.Close()
}

func (c *scriptedControlClient) writeConfigAck(t *testing.T, requestID uint32, version uint64) {
	t.Helper()

	body, err := protocol.MarshalConfigAck(protocol.ConfigAck{
		ConfigVersion: version,
		AppliedAtMs:   uint64(time.Now().UTC().UnixMilli()),
		Status:        protocol.StatusOK,
	})
	if err != nil {
		t.Fatalf("marshal config.ack: %v", err)
	}
	c.writeMessage(t, protocol.Frame{
		Type:      protocol.TypeConfigAck,
		RequestID: requestID,
		Body:      body,
	})
}

func (c *scriptedControlClient) writeHeartbeatPing(t *testing.T, requestID uint32) {
	t.Helper()

	body, err := protocol.MarshalHeartbeatPing(protocol.HeartbeatPing{
		ClientUnixMs: uint64(time.Now().UTC().UnixMilli()),
	})
	if err != nil {
		t.Fatalf("marshal heartbeat.ping: %v", err)
	}
	c.writeMessage(t, protocol.Frame{
		Type:      protocol.TypeHeartbeatPing,
		RequestID: requestID,
		Body:      body,
	})
}

func (c *scriptedControlClient) writeMessage(t *testing.T, frame protocol.Frame) {
	t.Helper()

	if c == nil || c.frames == nil {
		t.Fatal("scripted control client is not initialized")
	}
	frameBytes, err := frame.MarshalBinary()
	if err != nil {
		t.Fatalf("marshal frame: %v", err)
	}
	if err := c.frames.WriteFrame(c.conn, frameBytes, time.Second, transport.FrameContext{
		Side:      transport.FrameSideClient,
		ConnID:    c.connID,
		SessionID: c.sessionID,
	}); err != nil {
		t.Fatalf("write scripted frame: %v", err)
	}
}

func (c *scriptedControlClient) readMessage(t *testing.T) protocol.Frame {
	t.Helper()

	frame, err := c.readMessageWithin(time.Second)
	if err != nil {
		t.Fatalf("read scripted frame: %v", err)
	}
	return frame
}

func (c *scriptedControlClient) readMessageWithin(timeout time.Duration) (protocol.Frame, error) {
	if c == nil || c.frames == nil {
		return protocol.Frame{}, net.ErrClosed
	}
	frameBytes, err := c.frames.ReadFrame(c.conn, timeout, transport.FrameContext{
		Side:      transport.FrameSideClient,
		ConnID:    c.connID,
		SessionID: c.sessionID,
	})
	if err != nil {
		return protocol.Frame{}, err
	}
	return protocol.ParseFrame(frameBytes)
}

func assertNoExtraScriptedControlFrame(t *testing.T, client *scriptedControlClient, message string) {
	t.Helper()

	if frame, err := client.readMessageWithin(200 * time.Millisecond); err == nil {
		t.Fatalf("%s, got %s", message, frame.Type.String())
	} else if !isTimeoutError(err) {
		t.Fatalf("expected scripted control connection to stay idle, got %v", err)
	}
}

func assertScriptedHeartbeatStillWorks(t *testing.T, client *scriptedControlClient) {
	t.Helper()

	client.writeHeartbeatPing(t, 91)
	pongFrame := client.readMessage(t)
	if pongFrame.Type != protocol.TypeHeartbeatPong || pongFrame.RequestID != 91 {
		t.Fatalf("unexpected heartbeat.pong on scripted control connection: %#v", pongFrame)
	}
}

type deletableRuntimeRepository struct {
	mu      sync.RWMutex
	group   GroupRuntime
	deleted bool
}

func (r *deletableRuntimeRepository) LoadGroupRuntime(_ context.Context, _ [16]byte) (GroupRuntime, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if r.deleted {
		return GroupRuntime{}, ErrGroupNotFound
	}
	return cloneGroupRuntimeForTest(r.group), nil
}

func (r *deletableRuntimeRepository) LoadGroupRuntimeByID(_ context.Context, _ int64) (GroupRuntime, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if r.deleted {
		return GroupRuntime{}, ErrGroupNotFound
	}
	return cloneGroupRuntimeForTest(r.group), nil
}

func (r *deletableRuntimeRepository) ListGroupRuntimes(_ context.Context) ([]GroupRuntime, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if r.deleted {
		return nil, nil
	}
	return []GroupRuntime{cloneGroupRuntimeForTest(r.group)}, nil
}

func (r *deletableRuntimeRepository) Delete() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.deleted = true
}
