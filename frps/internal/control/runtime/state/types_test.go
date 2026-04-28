package state

import (
	"net"
	"testing"
	"time"

	controlbind "github.com/zightch/frp/frps/internal/control/bind"
	"github.com/zightch/frp/frps/pkg/protocol"
)

func TestConcreteSessionStateTracksRuntimeAndFreezeDrainsState(t *testing.T) {
	group := GroupRuntime{
		ID:          1,
		Enabled:     true,
		Snapshot:    ConfigSnapshot{Version: 7, GeneratedAtMs: 1234},
		EffectiveIP: "127.0.0.1",
	}
	session := NewConcreteSessionState(9, group, group.Snapshot, time.Second)

	tcp := fakeListener{addr: fakeAddr("127.0.0.1:2000")}
	udp := fakeUDPListener{addr: &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 2001}}
	startUDPCleanup, attached := session.AttachTunnelListeners(7, 10, []net.Listener{tcp}, []controlbind.UDPListener{udp})
	if !attached || !startUDPCleanup {
		t.Fatalf("expected listener attachment to start udp cleanup, attached=%v cleanup=%v", attached, startUDPCleanup)
	}

	stream := &Stream{
		ConfigVersion: 7,
		Tunnel:        protocol.TunnelEntry{TunnelID: 10},
		RemotePort:    2000,
		ClientAddr:    protocol.SockAddr{IP: net.ParseIP("198.51.100.10"), Port: 3000},
		OpenedAtMs:    100,
		Ready:         make(chan error, 1),
	}
	stream.Touch(time.UnixMilli(150))
	if !session.AddPublicStream(1, stream, 7) {
		t.Fatal("expected public stream to attach")
	}

	udpSession := &UDPSession{
		SessionID:   2,
		TunnelID:    10,
		RemotePort:  2001,
		ClientAddr:  protocol.SockAddr{IP: net.ParseIP("198.51.100.11"), Port: 3001},
		PublicAddr:  &net.UDPAddr{IP: net.ParseIP("198.51.100.11"), Port: 3001},
		Listener:    udp,
		OpenedAtMs:  200,
		IdleTimeout: time.Second,
	}
	udpSession.Touch(time.UnixMilli(250))
	if bound, created := session.BindPublicUDPSession(udpSession, 7); !created || bound != udpSession {
		t.Fatalf("expected udp session to bind, created=%v bound=%#v", created, bound)
	}

	_, runtimeState := session.ObserveState()
	if !runtimeState.ListenersStarted || runtimeState.Generation != 7 {
		t.Fatalf("unexpected runtime listener state: %#v", runtimeState)
	}
	if runtimeState.ActiveStreamCount != 1 || runtimeState.ActiveUDPSessionCount != 1 {
		t.Fatalf("unexpected connection counts: %#v", runtimeState)
	}
	if len(runtimeState.AttachedListeners) != 2 || len(runtimeState.Connections) != 2 {
		t.Fatalf("unexpected observed runtime details: %#v", runtimeState)
	}
	if _, ok := runtimeState.ActiveTunnelIDs[10]; !ok {
		t.Fatalf("expected active tunnel id 10, got %#v", runtimeState.ActiveTunnelIDs)
	}

	tcpListeners, udpListeners, streams, udpSessions := session.FreezeTunnelRuntime()
	if len(tcpListeners) != 1 || len(udpListeners) != 1 || len(streams) != 1 || len(udpSessions) != 1 {
		t.Fatalf("unexpected drained runtime: tcp=%d udp=%d streams=%d udpSessions=%d", len(tcpListeners), len(udpListeners), len(streams), len(udpSessions))
	}

	_, frozenState := session.ObserveState()
	if !frozenState.Frozen || frozenState.ListenersStarted || frozenState.ActiveStreamCount != 0 || frozenState.ActiveUDPSessionCount != 0 {
		t.Fatalf("unexpected frozen runtime state: %#v", frozenState)
	}
}

type fakeListener struct {
	addr net.Addr
}

func (l fakeListener) Accept() (net.Conn, error) { return nil, net.ErrClosed }
func (l fakeListener) Close() error              { return nil }
func (l fakeListener) Addr() net.Addr            { return l.addr }

type fakeUDPListener struct {
	addr net.Addr
}

func (l fakeUDPListener) Close() error                                  { return nil }
func (l fakeUDPListener) LocalAddr() net.Addr                           { return l.addr }
func (l fakeUDPListener) ReadFromUDP([]byte) (int, *net.UDPAddr, error) { return 0, nil, net.ErrClosed }
func (l fakeUDPListener) WriteToUDP(payload []byte, _ *net.UDPAddr) (int, error) {
	return len(payload), nil
}

type fakeAddr string

func (a fakeAddr) Network() string { return "tcp" }
func (a fakeAddr) String() string  { return string(a) }
