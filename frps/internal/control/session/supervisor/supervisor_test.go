package supervisor

import (
	"context"
	"net"
	"testing"
	"time"

	controlruntime "github.com/zightch/frp/frps/internal/control/runtime"
	controlsession "github.com/zightch/frp/frps/internal/control/session"
	"github.com/zightch/frp/frps/pkg/protocol"
)

func TestSupervisorRuntimeSnapshotActiveGroupsAndExclude(t *testing.T) {
	group, snapshot := testGroupRuntime()
	session := &testSession{id: 11, group: group, snapshot: snapshot}
	clientConn, serverConn := net.Pipe()
	defer clientConn.Close()
	defer serverConn.Close()

	state := testSessionState(group.ID, session.id, snapshot)
	s := New(controlsession.NoopExecutor{})
	defer s.Shutdown()

	if !s.ReserveGroupSlot(group.ID, session.id) {
		t.Fatal("expected group slot reservation to succeed")
	}
	if s.ReserveGroupSlot(group.ID, session.id+1) {
		t.Fatal("expected duplicate group slot reservation to fail")
	}
	s.ReleaseGroupSlot(group.ID, session.id)
	if !s.ReserveGroupSlot(group.ID, session.id) {
		t.Fatal("expected released group slot to be reusable")
	}
	s.ReleaseGroupSlot(group.ID, session.id)

	runtime := &testRuntime{
		groupID: group.ID,
		conn:    serverConn,
		session: session,
		group:   group,
		runtimeState: controlruntime.ObservedState{
			ActiveTunnelIDs: map[uint32]struct{}{7: {}},
		},
	}
	if agent := s.AttachSession(context.Background(), state, runtime); agent == nil {
		t.Fatal("expected supervisor to attach session")
	}

	snapshotState := s.Snapshot(nil)
	if got := snapshotState.GroupSlots[group.ID]; got != session.id {
		t.Fatalf("unexpected group slots: %#v", snapshotState.GroupSlots)
	}
	if len(snapshotState.Sessions) != 1 {
		t.Fatalf("expected one session snapshot, got %#v", snapshotState.Sessions)
	}

	active, ok := s.ActiveSession(group.ID)
	if !ok || active == nil || active.Conn == nil || active.Session == nil {
		t.Fatalf("expected active session, got ok=%v active=%#v", ok, active)
	}

	groups := s.ActiveRuntimeGroups(nil)
	if len(groups) != 1 || groups[0].Group.ID != group.ID || len(groups[0].Snapshot.Tunnels) != 1 {
		t.Fatalf("unexpected active runtime groups: %#v", groups)
	}
	if groups := s.ActiveRuntimeGroups(session); len(groups) != 0 {
		t.Fatalf("expected excluded session to be absent, got %#v", groups)
	}
}

func TestSupervisorTakeoverAndShutdown(t *testing.T) {
	group, snapshot := testGroupRuntime()
	firstSession := &testSession{id: 11, group: group, snapshot: snapshot}
	secondSession := &testSession{id: 12, group: group, snapshot: snapshot}
	firstClientConn, firstConn := net.Pipe()
	defer firstClientConn.Close()
	defer firstConn.Close()
	secondClientConn, secondConn := net.Pipe()
	defer secondClientConn.Close()
	defer secondConn.Close()

	s := New(controlsession.NoopExecutor{})
	firstAgent := s.AttachSession(context.Background(), testSessionState(group.ID, firstSession.id, snapshot), &testRuntime{
		groupID: group.ID,
		conn:    firstConn,
		session: firstSession,
		group:   group,
	})
	if firstAgent == nil {
		t.Fatal("expected first session to attach")
	}
	secondAgent := s.AttachSession(context.Background(), testSessionState(group.ID, secondSession.id, snapshot), &testRuntime{
		groupID: group.ID,
		conn:    secondConn,
		session: secondSession,
		group:   group,
	})
	if secondAgent == nil {
		t.Fatal("expected replacement session to attach")
	}

	waitFor(t, func() bool {
		return firstAgent.State().BlockReason == controlsession.BlockReasonSessionReplaced
	})
	active, ok := s.ActiveRuntime(group.ID)
	if !ok || active.Session == nil || active.Session.SessionID() != secondSession.id {
		t.Fatalf("expected replacement session to be active, got ok=%v active=%#v", ok, active)
	}

	s.Shutdown()
	waitFor(t, func() bool {
		select {
		case <-firstAgent.Done():
			return true
		default:
			return false
		}
	})
	waitFor(t, func() bool {
		select {
		case <-secondAgent.Done():
			return true
		default:
			return false
		}
	})
}

type testRuntime struct {
	groupID      int64
	conn         net.Conn
	session      *testSession
	group        controlruntime.GroupRuntime
	runtimeState controlruntime.ObservedState
}

func (r *testRuntime) RuntimeGroupID() int64 { return r.groupID }
func (r *testRuntime) RuntimeConn() net.Conn { return r.conn }
func (r *testRuntime) RuntimeSession() controlruntime.SessionStateProjectionTarget {
	return r.session
}
func (r *testRuntime) RuntimeSnapshot(state controlsession.SessionState) controlruntime.SessionSnapshot {
	configState, _ := r.session.ObserveState()
	return controlruntime.SessionSnapshot{
		GroupID:   r.groupID,
		SessionID: r.session.id,
		Conn:      r.conn,
		Config:    configState,
		State:     state,
		Runtime:   r.runtimeState,
	}
}

type testSession struct {
	id       uint64
	group    controlruntime.GroupRuntime
	snapshot controlruntime.ConfigSnapshot
}

func (s *testSession) SessionID() uint64 { return s.id }
func (s *testSession) ObserveState() (controlruntime.ObservedConfigState, controlruntime.ObservedState) {
	state := testSessionState(s.group.ID, s.id, s.snapshot)
	return controlruntime.ObservedConfigState{
		State: state,
		Group: s.group,
	}, controlruntime.ObservedState{}
}
func (s *testSession) CurrentGroupAndSnapshot() (controlruntime.GroupRuntime, controlruntime.ConfigSnapshot) {
	return s.group, s.snapshot
}

func testGroupRuntime() (controlruntime.GroupRuntime, controlruntime.ConfigSnapshot) {
	snapshot := controlruntime.ConfigSnapshot{
		Version: 3,
		Tunnels: []protocol.TunnelEntry{
			{
				TunnelID:    7,
				Protocol:    protocol.ProtocolTCP,
				TunnelFlags: protocol.TunnelFlagEnabled,
				RemoteStart: 10001,
				RemoteEnd:   10001,
			},
		},
	}
	group := controlruntime.GroupRuntime{
		ID:          1,
		Name:        "group-a",
		Enabled:     true,
		EffectiveIP: "127.0.0.1",
		Snapshot:    snapshot,
	}
	return group, snapshot
}

func testSessionState(groupID int64, sessionID uint64, snapshot controlruntime.ConfigSnapshot) controlsession.SessionState {
	state := controlsession.NewState(groupID, sessionID)
	desired := controlruntime.DesiredRuntimeFromSnapshot("127.0.0.1", snapshot)
	state.Desired = &desired
	state.Applied = &controlsession.AppliedRuntimeSnapshot{Snapshot: desired}
	return state
}

func waitFor(t *testing.T, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("condition not satisfied before deadline")
}
