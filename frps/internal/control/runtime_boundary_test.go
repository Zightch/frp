package control

import (
	"context"
	"io"
	"log/slog"
	"net"
	"testing"
	"time"

	controlruntime "github.com/zightch/frp/frps/internal/control/runtime"
	controlobserve "github.com/zightch/frp/frps/internal/control/runtime/observe"
	controlsession "github.com/zightch/frp/frps/internal/control/session"
	"github.com/zightch/frp/frps/pkg/protocol"
	"github.com/zightch/frp/frps/pkg/testsupport"
)

func TestRuntimeIssueStoreKeepsNewerConfigVersion(t *testing.T) {
	store := controlruntime.NewIssueStore()

	store.RecordForConfig(7, 2, "newer issue")
	store.RecordForConfig(7, 1, "")

	issues := store.SnapshotReasons()
	if got := issues[7]; got != "newer issue" {
		t.Fatalf("expected newer config version issue to win, got %#v", issues)
	}

	store.RecordForConfig(7, 2, "")
	if issues := store.SnapshotReasons(); len(issues) != 0 {
		t.Fatalf("expected runtime issue to clear on matching config version, got %#v", issues)
	}
}

func TestSupervisorSnapshotBuildsSessionAndActiveRuntimeGroups(t *testing.T) {
	host, err := protocol.ParseHost("127.0.0.1")
	if err != nil {
		t.Fatalf("parse host: %v", err)
	}

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen tcp: %v", err)
	}
	defer controlruntime.CloseStartedTunnelListeners([]net.Listener{listener}, nil)

	group := GroupRuntime{
		ID:          1,
		Name:        "group-a",
		Enabled:     true,
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
	group.Snapshot = snapshot
	session := newSessionState(11, group, snapshot, 0)
	session.RuntimeMu.Lock()
	session.Runtime.Listeners.Started = true
	session.Runtime.Generation = snapshot.Version
	session.Runtime.Listeners.TCP[7] = []net.Listener{listener}
	session.RuntimeMu.Unlock()

	clientConn, serverConn := net.Pipe()
	defer clientConn.Close()
	defer serverConn.Close()

	state := controlsession.NewState(group.ID, session.ID)
	desired := controlruntime.DesiredRuntimeFromGroup(group)
	state.Desired = &desired
	state.Applied = &controlsession.AppliedRuntimeSnapshot{Snapshot: desired}

	supervisor := NewSupervisor(controlsession.NoopExecutor{})
	defer supervisor.Shutdown()
	runtime := &runtimeExecutor{
		groupID:      group.ID,
		conn:         serverConn,
		session:      session,
		desiredGroup: group,
	}
	if !supervisor.ReserveGroupSlot(group.ID, session.ID) {
		t.Fatal("expected group slot reservation to succeed")
	}
	supervisor.ReleaseGroupSlot(group.ID, session.ID)
	agent := supervisor.AttachSession(context.Background(), state, runtime)
	if agent == nil {
		t.Fatal("expected supervisor to attach session")
	}

	supervisorSnapshot := supervisor.Snapshot(nil)
	if got := supervisorSnapshot.groupSlots[group.ID]; got != session.ID {
		t.Fatalf("unexpected group slot snapshot: %#v", supervisorSnapshot.groupSlots)
	}
	if len(supervisorSnapshot.sessions) != 1 {
		t.Fatalf("expected one runtime session snapshot, got %#v", supervisorSnapshot.sessions)
	}
	if supervisorSnapshot.sessions[0].Runtime.Generation != snapshot.Version {
		t.Fatalf("unexpected runtime generation in snapshot: %#v", supervisorSnapshot.sessions[0].Runtime)
	}
	if _, ok := supervisorSnapshot.sessions[0].Runtime.ActiveTunnelIDs[7]; !ok {
		t.Fatalf("expected runtime snapshot to expose active tunnel ids, got %#v", supervisorSnapshot.sessions[0].Runtime.ActiveTunnelIDs)
	}
	if len(supervisorSnapshot.sessions[0].Runtime.AttachedListeners) != 1 {
		t.Fatalf("expected runtime snapshot to expose attached listeners, got %#v", supervisorSnapshot.sessions[0].Runtime.AttachedListeners)
	}
	if supervisorSnapshot.sessions[0].Runtime.AttachedListeners[0].Port != uint16(listener.Addr().(*net.TCPAddr).Port) {
		t.Fatalf("unexpected attached listener port: %#v", supervisorSnapshot.sessions[0].Runtime.AttachedListeners)
	}

	activeGroups := controlruntime.SupervisorActiveRuntimeGroups(supervisor, nil)
	if len(activeGroups) != 1 {
		t.Fatalf("expected one active runtime group, got %#v", activeGroups)
	}
	if activeGroups[0].Group.ID != group.ID || len(activeGroups[0].Snapshot.Tunnels) != 1 || activeGroups[0].Snapshot.Tunnels[0].TunnelID != 7 {
		t.Fatalf("unexpected active runtime group snapshot: %#v", activeGroups)
	}
	if groups := controlruntime.SupervisorActiveRuntimeGroups(supervisor, session); len(groups) != 0 {
		t.Fatalf("expected excluded session to be absent from runtime groups, got %#v", groups)
	}
}

func TestRuntimeSnapshotIndexBuildsSessionTunnelAndConnectionTargets(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen tcp: %v", err)
	}
	defer controlruntime.CloseStartedTunnelListeners([]net.Listener{listener}, nil)

	activePort := uint16(listener.Addr().(*net.TCPAddr).Port)
	missingPort := activePort + 1
	group := GroupRuntime{
		ID:          1,
		Name:        "group-a",
		Enabled:     true,
		EffectiveIP: "127.0.0.1",
	}
	snapshot := ConfigSnapshot{
		Version: 3,
		Tunnels: []protocol.TunnelEntry{
			{
				TunnelID:    7,
				Protocol:    protocol.ProtocolTCP,
				TunnelFlags: protocol.TunnelFlagEnabled,
				RemoteStart: activePort,
				RemoteEnd:   activePort,
			},
			{
				TunnelID:    8,
				Protocol:    protocol.ProtocolTCP,
				TunnelFlags: protocol.TunnelFlagEnabled,
				RemoteStart: missingPort,
				RemoteEnd:   missingPort,
			},
		},
	}
	group.Snapshot = snapshot
	session := newSessionState(11, group, snapshot, 0)
	session.RuntimeMu.Lock()
	session.Runtime.Listeners.Started = true
	session.Runtime.Generation = snapshot.Version
	session.Runtime.Listeners.TCP[7] = []net.Listener{listener}
	session.RuntimeMu.Unlock()

	publicClient, publicServer := net.Pipe()
	defer publicClient.Close()
	defer publicServer.Close()

	now := time.Unix(1_700_000_200, 0).UTC()
	streamConn := &connWithRemoteAddr{
		Conn:   publicServer,
		remote: &net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: 32000},
	}
	streamOpen, err := session.preparePublicStreamOpen(snapshot.Version, snapshot.Tunnels[0], activePort, streamConn, now)
	if err != nil {
		t.Fatalf("prepare public stream open: %v", err)
	}
	defer session.closePublicStream(streamOpen.streamID)

	clientConn, serverConn := net.Pipe()
	defer clientConn.Close()
	defer serverConn.Close()

	state := controlsession.NewState(group.ID, session.ID)
	desired := controlruntime.DesiredRuntimeFromGroup(group)
	state.Desired = &desired
	state.Applied = &controlsession.AppliedRuntimeSnapshot{Snapshot: desired}

	supervisor := NewSupervisor(controlsession.NoopExecutor{})
	defer supervisor.Shutdown()
	runtime := &runtimeExecutor{
		groupID:      group.ID,
		conn:         serverConn,
		session:      session,
		desiredGroup: group,
	}
	if agent := supervisor.AttachSession(context.Background(), state, runtime); agent == nil {
		t.Fatal("expected supervisor to attach session")
	}

	viewIndex := controlruntime.NewRuntimeSnapshotIndex(supervisor.Snapshot(nil).sessions)
	sessions := viewIndex.SessionsList()
	if len(sessions) != 1 {
		t.Fatalf("expected one runtime session target, got %#v", sessions)
	}

	sessionTarget := sessions[0]
	if sessionTarget.ID.GroupID != group.ID || sessionTarget.ID.SessionID != session.ID {
		t.Fatalf("unexpected session target identity: %#v", sessionTarget.ID)
	}
	if selected, ok := viewIndex.SessionByID(sessionTarget.ID); !ok || selected.ID != sessionTarget.ID {
		t.Fatalf("expected runtime view to resolve session by stable id, got ok=%v target=%#v", ok, selected)
	}
	if sessionTarget.ConnID == "" {
		t.Fatalf("expected session target conn id, got %#v", sessionTarget)
	}
	if sessionTarget.ActiveStreamCount != 1 || sessionTarget.ActiveUDPCount != 0 {
		t.Fatalf("unexpected session target connection counters: %#v", sessionTarget)
	}
	if _, ok := sessionTarget.ActiveTunnelIDs[7]; !ok {
		t.Fatalf("expected active tunnel ids to include tunnel 7, got %#v", sessionTarget.ActiveTunnelIDs)
	}
	if len(sessionTarget.ListenersByTunnel[7]) != 1 {
		t.Fatalf("expected attached listener target on tunnel 7, got %#v", sessionTarget.ListenersByTunnel)
	}
	missing := sessionTarget.MissingByTunnel[8]
	if missing.ID.TunnelID != 8 || len(missing.MissingPorts) != 1 || missing.MissingPorts[0] != missingPort {
		t.Fatalf("unexpected missing listener target: %#v", sessionTarget.MissingByTunnel)
	}
	if len(sessionTarget.Connections) != 1 {
		t.Fatalf("expected one runtime connection target, got %#v", sessionTarget.Connections)
	}
	connection := sessionTarget.Connections[0]
	if connection.ID.ConnectionID != streamOpen.streamID || connection.ID.Kind != observedRuntimeConnectionKindTCPStream {
		t.Fatalf("unexpected runtime connection target identity: %#v", connection)
	}
	if connection.TunnelID != 7 || connection.RemotePort != activePort || connection.ClientAddr != "127.0.0.1:32000" {
		t.Fatalf("unexpected runtime connection target metadata: %#v", connection)
	}

	targetTunnels := viewIndex.SelectTunnels(
		[]GroupRuntime{group},
		map[int64]string{8: "bind failed"},
		map[int64]struct{}{7: {}},
		controlobserve.TunnelStatus,
		controlobserve.RuntimeIssueKind,
	)
	if len(targetTunnels) != 2 {
		t.Fatalf("expected two runtime tunnel targets, got %#v", targetTunnels)
	}

	var activeTunnelTarget controlruntime.RuntimeTunnelTarget
	var missingTunnelTarget controlruntime.RuntimeTunnelTarget
	for _, tunnel := range targetTunnels {
		switch tunnel.ID.TunnelID {
		case 7:
			activeTunnelTarget = tunnel
		case 8:
			missingTunnelTarget = tunnel
		}
	}

	if activeTunnelTarget.ID.SessionID != session.ID || !activeTunnelTarget.StaticConflict || len(activeTunnelTarget.Listeners) != 1 {
		t.Fatalf("unexpected active tunnel target: %#v", activeTunnelTarget)
	}
	if missingTunnelTarget.ID.SessionID != session.ID || missingTunnelTarget.RuntimeIssue != "bind failed" || len(missingTunnelTarget.MissingPorts) != 1 || missingTunnelTarget.MissingPorts[0] != missingPort {
		t.Fatalf("unexpected missing tunnel target: %#v", missingTunnelTarget)
	}

	nonListening := viewIndex.SelectNonListeningEnabledTunnels(group)
	if len(nonListening) != 1 || nonListening[0].TunnelID != 8 {
		t.Fatalf("expected only non-listening tunnel 8, got %#v", nonListening)
	}

	observed := sessionTarget.ObservedState()
	if observed.GroupID != group.ID || observed.SessionID != session.ID || observed.SnapshotVersion != snapshot.Version {
		t.Fatalf("unexpected observed session state projection: %#v", observed)
	}
	if len(sessionTarget.ObservedListeners()) != 1 || len(sessionTarget.ObservedMissingListeners()) != 1 || len(sessionTarget.ObservedConnections()) != 1 {
		t.Fatalf("expected observed projections to stay aligned with runtime target, got listeners=%#v missing=%#v connections=%#v", sessionTarget.ObservedListeners(), sessionTarget.ObservedMissingListeners(), sessionTarget.ObservedConnections())
	}
}

func TestRuntimeSnapshotBuildsProjectionFromSessionState(t *testing.T) {
	group := GroupRuntime{
		ID:          1,
		Name:        "group-a",
		Enabled:     true,
		EffectiveIP: "127.0.0.1",
	}
	snapshot := ConfigSnapshot{Version: 1}
	group.Snapshot = snapshot

	state := controlsession.NewState(group.ID, 11)
	desired := controlruntime.DesiredRuntimeFromGroup(group)
	state.Desired = &desired
	applied := controlruntime.DesiredRuntimeFromGroup(group)
	applied.Version = 7
	applied.EffectiveIP = "127.0.0.2"
	state.Applied = &controlsession.AppliedRuntimeSnapshot{Snapshot: applied}

	target := controlruntime.NewRuntimeSessionTarget(controlruntime.SessionSnapshot{
		GroupID:      group.ID,
		SessionID:    11,
		DesiredGroup: group,
		RecoveryMode: testsupport.RecoveryModePendingFullConfig,
		State:        state,
	})

	if target.Snapshot.Version != 7 {
		t.Fatalf("expected runtime view to prefer applied snapshot, got %#v", target.Snapshot)
	}
	if target.EffectiveIP != "127.0.0.2" {
		t.Fatalf("expected runtime view to prefer applied effective ip, got %#v", target)
	}
	if target.LastAckedVersion != 7 {
		t.Fatalf("expected runtime view to prefer applied last acked version, got %#v", target)
	}
	if target.Pending != nil {
		t.Fatalf("expected runtime view to clear pending config when session state has none, got %#v", target.Pending)
	}
}

func TestSessionConfigApplyTracksPendingAndAppliedRecoveryModes(t *testing.T) {
	group := GroupRuntime{ID: 1, Name: "group-a"}
	snapshot := ConfigSnapshot{Version: 1}
	session := newSessionState(11, group, snapshot, 0)

	initialState := controlsession.NewState(group.ID, session.ID)
	group.Snapshot = snapshot
	desired := controlruntime.DesiredRuntimeFromGroup(group)
	initialState.Desired = &desired
	initialState.Applied = &controlsession.AppliedRuntimeSnapshot{Snapshot: desired}
	initialState.Conn = controlsession.ControlConnState{Attached: true, ConnID: "test-conn"}
	initialState.Phase = controlsession.SessionPhaseOnline
	initialState.RuntimePhase = controlsession.RuntimePhaseEmpty
	session.setControlState(initialState)

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
	if pushOp.RequestID == 0 {
		t.Fatal("expected non-zero request id")
	}
	if got := session.RecoveryModeValue(); got != testsupport.RecoveryModePendingFullConfig {
		t.Fatalf("expected pending full-config recovery mode, got %s", got)
	}

	applied, err := session.acceptConfigAck(pushOp.RequestID, fullSnapshot.Version)
	if err != nil {
		t.Fatalf("accept config ack: %v", err)
	}
	if applied.RecoveryMode != testsupport.RecoveryModeRunning {
		t.Fatalf("expected running recovery mode after ack, got %s", applied.RecoveryMode)
	}
	if got := session.RecoveryModeValue(); got != testsupport.RecoveryModeRunning {
		t.Fatalf("expected running recovery mode on session, got %s", got)
	}

	emptySnapshot := ConfigSnapshot{Version: 3}
	emptyPush, err := session.prepareConfigPush(GroupRuntime{ID: 1, Name: "group-a"}, emptySnapshot)
	if err != nil {
		t.Fatalf("prepare empty config push: %v", err)
	}
	if got := session.RecoveryModeValue(); got != testsupport.RecoveryModePendingEmptyConfig {
		t.Fatalf("expected pending empty-config recovery mode, got %s", got)
	}

	emptyApplied, err := session.acceptConfigAck(emptyPush.RequestID, emptySnapshot.Version)
	if err != nil {
		t.Fatalf("accept empty config ack: %v", err)
	}
	if emptyApplied.RecoveryMode != testsupport.RecoveryModeEmptyConfig {
		t.Fatalf("expected empty-config recovery mode after ack, got %s", emptyApplied.RecoveryMode)
	}
	if got := session.RecoveryModeValue(); got != testsupport.RecoveryModeEmptyConfig {
		t.Fatalf("expected empty-config recovery mode on session, got %s", got)
	}
}

func TestServerPlanSessionRuntimeStartSeparatesActiveAndPendingTunnels(t *testing.T) {
	activeListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen active tunnel: %v", err)
	}
	defer controlruntime.CloseStartedTunnelListeners([]net.Listener{activeListener}, nil)

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
	session.RuntimeMu.Lock()
	session.Runtime.Listeners.Started = true
	session.Runtime.Generation = 4
	session.Runtime.Listeners.TCP[7] = []net.Listener{activeListener}
	session.RuntimeMu.Unlock()

	server := NewServer(
		Options{WriteTimeout: 0},
		slog.New(slog.NewTextHandler(io.Discard, nil)),
		"test-server",
	)

	plan := controlruntime.PlanSessionRuntimeStart(server, newSessionRuntimeStartTarget(nil, slog.New(slog.NewTextHandler(io.Discard, nil)), session))
	if !plan.ActiveRuntime {
		t.Fatal("expected plan to mark active runtime")
	}
	if plan.BindIP != "127.0.0.1" {
		t.Fatalf("unexpected bind ip: %q", plan.BindIP)
	}
	if len(plan.TargetTunnels) != 1 || plan.TargetTunnels[0].TunnelID != 8 {
		t.Fatalf("unexpected start targets: %#v", plan.TargetTunnels)
	}
	if len(plan.ClearIssueTunnelIDs) != 2 {
		t.Fatalf("unexpected clear-issue tunnel ids: %#v", plan.ClearIssueTunnelIDs)
	}
	if plan.ClearIssueTunnelIDs[0] != 6 || plan.ClearIssueTunnelIDs[1] != 7 {
		t.Fatalf("unexpected clear-issue ordering: %#v", plan.ClearIssueTunnelIDs)
	}
}

func TestSessionPreparePublicStreamOpenBuildsFrameAndTracksConnectionMetadata(t *testing.T) {
	publicClient, publicServer := net.Pipe()
	defer publicClient.Close()
	defer publicServer.Close()

	session := newSessionState(11, GroupRuntime{ID: 1, Name: "group-a"}, ConfigSnapshot{Version: 2}, 0)
	session.RuntimeMu.Lock()
	session.Runtime.Listeners.Started = true
	session.Runtime.Generation = 2
	session.RuntimeMu.Unlock()

	now := time.Unix(1_700_000_000, 0).UTC()
	streamConn := &connWithRemoteAddr{
		Conn:   publicServer,
		remote: &net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: 31000},
	}
	streamOpen, err := session.preparePublicStreamOpen(2, protocol.TunnelEntry{
		TunnelID:    7,
		Protocol:    protocol.ProtocolTCP,
		TunnelFlags: protocol.TunnelFlagEnabled,
	}, 22000, streamConn, now)
	if err != nil {
		t.Fatalf("prepare public stream open: %v", err)
	}
	if streamOpen.blocked {
		t.Fatal("expected public stream open to be admitted")
	}
	if streamOpen.streamID == 0 || streamOpen.stream == nil {
		t.Fatalf("unexpected stream open operation: %#v", streamOpen)
	}
	if session.publicStream(streamOpen.streamID) != streamOpen.stream {
		t.Fatal("expected opened stream to be tracked on session")
	}
	if streamOpen.openFrame.Type != protocol.TypeStreamOpen || streamOpen.openFrame.StreamID != streamOpen.streamID || streamOpen.openFrame.RequestID == 0 {
		t.Fatalf("unexpected stream.open frame: %#v", streamOpen.openFrame)
	}

	streamOpenBody, err := protocol.UnmarshalStreamOpen(streamOpen.openFrame.Body)
	if err != nil {
		t.Fatalf("unmarshal stream.open body: %v", err)
	}
	if streamOpenBody.RemotePort != 22000 {
		t.Fatalf("unexpected stream.open remote port: %#v", streamOpenBody)
	}
	if !streamOpenBody.ClientAddr.IP.Equal(net.ParseIP("127.0.0.1").To4()) || streamOpenBody.ClientAddr.Port != 31000 {
		t.Fatalf("unexpected stream.open client addr: %#v", streamOpenBody.ClientAddr)
	}
	if streamOpenBody.OpenedAtMs != uint64(now.UnixMilli()) {
		t.Fatalf("unexpected stream.open opened_at_ms: %#v", streamOpenBody)
	}

	_, runtimeState := session.observeState()
	if runtimeState.ActiveStreamCount != 1 || runtimeState.ActiveUDPSessionCount != 0 {
		t.Fatalf("unexpected runtime connection counts: %#v", runtimeState)
	}
	if len(runtimeState.Connections) != 1 {
		t.Fatalf("expected one runtime connection, got %#v", runtimeState.Connections)
	}
	connection := runtimeState.Connections[0]
	if connection.Kind != observedRuntimeConnectionKindTCPStream || connection.Protocol != "tcp" {
		t.Fatalf("unexpected observed stream connection: %#v", connection)
	}
	if connection.ConnectionID != streamOpen.streamID || connection.TunnelID != 7 || connection.RemotePort != 22000 {
		t.Fatalf("unexpected observed stream identity: %#v", connection)
	}
	if connection.ClientAddr != "127.0.0.1:31000" || connection.OpenedAtMs != uint64(now.UnixMilli()) {
		t.Fatalf("unexpected observed stream metadata: %#v", connection)
	}

	if !session.closePublicStream(streamOpen.streamID) {
		t.Fatal("expected stream close to succeed")
	}
}

func TestSessionPreparePublicUDPDatagramForwardReusesSessionAndTracksConnectionMetadata(t *testing.T) {
	listener, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 0})
	if err != nil {
		t.Fatalf("listen udp: %v", err)
	}
	defer listener.Close()

	session := newSessionState(11, GroupRuntime{ID: 1, Name: "group-a"}, ConfigSnapshot{Version: 4}, 0)
	session.RuntimeMu.Lock()
	session.Runtime.Listeners.Started = true
	session.Runtime.Generation = 4
	session.RuntimeMu.Unlock()

	clientAddr := &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 53000}
	tunnel := protocol.TunnelEntry{
		TunnelID:    8,
		Protocol:    protocol.ProtocolUDP,
		TunnelFlags: protocol.TunnelFlagEnabled,
	}
	remotePort := uint16(listener.LocalAddr().(*net.UDPAddr).Port)
	firstNow := time.Unix(1_700_000_100, 0).UTC()
	firstForward, err := session.preparePublicUDPDatagramForward(4, tunnel, remotePort, listener, clientAddr, []byte("hello"), firstNow)
	if err != nil {
		t.Fatalf("prepare first udp forward: %v", err)
	}
	if firstForward.blocked || !firstForward.created || firstForward.udpSession == nil {
		t.Fatalf("unexpected first udp forward operation: %#v", firstForward)
	}
	if len(firstForward.frames) != 2 {
		t.Fatalf("expected udp.open + udp.data for first datagram, got %#v", firstForward.frames)
	}
	if firstForward.frames[0].Type != protocol.TypeUDPOpen || firstForward.frames[0].RequestID == 0 {
		t.Fatalf("unexpected udp.open frame: %#v", firstForward.frames[0])
	}
	if firstForward.frames[1].Type != protocol.TypeUDPData || string(firstForward.frames[1].Body) != "hello" {
		t.Fatalf("unexpected first udp.data frame: %#v", firstForward.frames[1])
	}

	secondNow := firstNow.Add(time.Second)
	secondForward, err := session.preparePublicUDPDatagramForward(4, tunnel, remotePort, listener, clientAddr, []byte("again"), secondNow)
	if err != nil {
		t.Fatalf("prepare second udp forward: %v", err)
	}
	if secondForward.blocked || secondForward.created {
		t.Fatalf("expected existing udp session reuse, got %#v", secondForward)
	}
	if secondForward.udpSession == nil || secondForward.udpSession.SessionID != firstForward.udpSession.SessionID {
		t.Fatalf("expected reused udp session id %d, got %#v", firstForward.udpSession.SessionID, secondForward.udpSession)
	}
	if len(secondForward.frames) != 1 || secondForward.frames[0].Type != protocol.TypeUDPData || string(secondForward.frames[0].Body) != "again" {
		t.Fatalf("unexpected reused udp.data frame: %#v", secondForward.frames)
	}

	_, runtimeState := session.observeState()
	if runtimeState.ActiveStreamCount != 0 || runtimeState.ActiveUDPSessionCount != 1 {
		t.Fatalf("unexpected runtime connection counts: %#v", runtimeState)
	}
	if len(runtimeState.Connections) != 1 {
		t.Fatalf("expected one observed udp connection, got %#v", runtimeState.Connections)
	}
	connection := runtimeState.Connections[0]
	if connection.Kind != observedRuntimeConnectionKindUDPSession || connection.Protocol != "udp" {
		t.Fatalf("unexpected observed udp connection: %#v", connection)
	}
	if connection.ConnectionID != firstForward.udpSession.SessionID || connection.TunnelID != tunnel.TunnelID || connection.RemotePort != remotePort {
		t.Fatalf("unexpected observed udp identity: %#v", connection)
	}
	if connection.ClientAddr != "127.0.0.1:53000" {
		t.Fatalf("unexpected observed udp client addr: %#v", connection)
	}
	if connection.OpenedAtMs != uint64(firstNow.UnixMilli()) || connection.LastActiveAtMs != uint64(secondNow.UnixMilli()) {
		t.Fatalf("unexpected observed udp timestamps: %#v", connection)
	}
	if connection.IdleTimeoutMs != uint32(defaultUDPIdleTimeout/time.Millisecond) {
		t.Fatalf("unexpected observed udp idle timeout: %#v", connection)
	}
}
