package wiring

import (
	"net"
	"testing"

	"github.com/zightch/frp/frps/pkg/protocol"
)

func TestSessionStateAddPublicStreamRegistersTunnelRateLimiters(t *testing.T) {
	tunnel := protocol.TunnelEntry{
		TunnelID:    7,
		Protocol:    protocol.ProtocolTCP,
		TunnelFlags: protocol.TunnelFlagEnabled,
		RemoteStart: 20000,
		RemoteEnd:   20000,
		RatePolicy: protocol.TunnelRatePolicy{
			PolicyID:    9,
			Mode:        protocol.RatePolicyModeIndependent,
			DownlinkBPS: 10_000_000,
			UplinkBPS:   5_000_000,
		},
	}
	snapshot := ConfigSnapshot{
		Version: 3,
		Tunnels: []protocol.TunnelEntry{tunnel},
	}
	session := newSessionState(11, GroupRuntime{ID: 1, Snapshot: snapshot}, snapshot, 0)
	session.RuntimeMu.Lock()
	session.Runtime.Listeners.Started = true
	session.Runtime.Generation = snapshot.Version
	session.RuntimeMu.Unlock()

	clientA, serverA := net.Pipe()
	defer clientA.Close()
	defer serverA.Close()
	clientB, serverB := net.Pipe()
	defer clientB.Close()
	defer serverB.Close()

	streamA := &publicStream{
		Conn:   serverA,
		Tunnel: tunnel,
		Ready:  make(chan error, 1),
	}
	streamB := &publicStream{
		Conn:   serverB,
		Tunnel: tunnel,
		Ready:  make(chan error, 1),
	}

	if !session.AddPublicStream(101, streamA, snapshot.Version) {
		t.Fatal("expected first stream to register")
	}
	if !session.AddPublicStream(102, streamB, snapshot.Version) {
		t.Fatal("expected second stream to register")
	}

	ctxA, limitersA, ok := session.StreamRateLimit(101)
	if !ok || ctxA == nil {
		t.Fatal("expected first stream rate limit entry")
	}
	_, limitersB, ok := session.StreamRateLimit(102)
	if !ok {
		t.Fatal("expected second stream rate limit entry")
	}
	if limitersA.Downlink == nil || limitersA.Uplink == nil {
		t.Fatalf("expected independent tunnel limiters, got %#v", limitersA)
	}
	if limitersA.Downlink != limitersB.Downlink || limitersA.Uplink != limitersB.Uplink {
		t.Fatal("streams on the same tunnel must share the same tunnel limiter pair")
	}

	session.FreezeTunnelRuntime()
	select {
	case <-ctxA.Done():
	default:
		t.Fatal("expected freeze to cancel stream rate limit context")
	}
}

func TestSessionStateAddPublicStreamRegistersSharedPolicyAcrossTunnels(t *testing.T) {
	tunnelA := protocol.TunnelEntry{
		TunnelID:    7,
		Protocol:    protocol.ProtocolTCP,
		TunnelFlags: protocol.TunnelFlagEnabled,
		RemoteStart: 20000,
		RemoteEnd:   20000,
		RatePolicy: protocol.TunnelRatePolicy{
			PolicyID:    9,
			Mode:        protocol.RatePolicyModeShared,
			DownlinkBPS: 10_000_000,
			UplinkBPS:   5_000_000,
		},
	}
	tunnelB := tunnelA
	tunnelB.TunnelID = 8
	tunnelB.RemoteStart = 20001
	tunnelB.RemoteEnd = 20001
	snapshot := ConfigSnapshot{
		Version: 4,
		Tunnels: []protocol.TunnelEntry{tunnelA, tunnelB},
	}
	session := newSessionState(11, GroupRuntime{ID: 1, Snapshot: snapshot}, snapshot, 0)
	session.RuntimeMu.Lock()
	session.Runtime.Listeners.Started = true
	session.Runtime.Generation = snapshot.Version
	session.RuntimeMu.Unlock()

	clientA, serverA := net.Pipe()
	defer clientA.Close()
	defer serverA.Close()
	clientB, serverB := net.Pipe()
	defer clientB.Close()
	defer serverB.Close()

	if !session.AddPublicStream(101, &publicStream{Conn: serverA, Tunnel: tunnelA, Ready: make(chan error, 1)}, snapshot.Version) {
		t.Fatal("expected first shared stream to register")
	}
	if !session.AddPublicStream(102, &publicStream{Conn: serverB, Tunnel: tunnelB, Ready: make(chan error, 1)}, snapshot.Version) {
		t.Fatal("expected second shared stream to register")
	}

	_, limitersA, ok := session.StreamRateLimit(101)
	if !ok {
		t.Fatal("expected first shared stream rate limit entry")
	}
	_, limitersB, ok := session.StreamRateLimit(102)
	if !ok {
		t.Fatal("expected second shared stream rate limit entry")
	}
	if limitersA.Downlink != limitersB.Downlink || limitersA.Uplink != limitersB.Uplink {
		t.Fatal("shared policy must reuse limiter pair across tunnels")
	}
}

func TestSessionStateClosePublicStreamCancelsRateLimitContext(t *testing.T) {
	tunnel := protocol.TunnelEntry{
		TunnelID:    7,
		Protocol:    protocol.ProtocolTCP,
		TunnelFlags: protocol.TunnelFlagEnabled,
		RemoteStart: 20000,
		RemoteEnd:   20000,
		RatePolicy: protocol.TunnelRatePolicy{
			PolicyID:    9,
			Mode:        protocol.RatePolicyModeIndependent,
			DownlinkBPS: 10_000_000,
			UplinkBPS:   5_000_000,
		},
	}
	snapshot := ConfigSnapshot{
		Version: 3,
		Tunnels: []protocol.TunnelEntry{tunnel},
	}
	session := newSessionState(11, GroupRuntime{ID: 1, Snapshot: snapshot}, snapshot, 0)
	session.RuntimeMu.Lock()
	session.Runtime.Listeners.Started = true
	session.Runtime.Generation = snapshot.Version
	session.RuntimeMu.Unlock()

	clientConn, serverConn := net.Pipe()
	defer clientConn.Close()
	defer serverConn.Close()

	stream := &publicStream{Conn: serverConn, Tunnel: tunnel, Ready: make(chan error, 1)}
	if !session.AddPublicStream(101, stream, snapshot.Version) {
		t.Fatal("expected stream to register")
	}

	ctx, _, ok := session.StreamRateLimit(101)
	if !ok || ctx == nil {
		t.Fatal("expected stream rate limit context")
	}

	if !session.ClosePublicStream(101) {
		t.Fatal("expected stream close to succeed")
	}
	select {
	case <-ctx.Done():
	default:
		t.Fatal("expected stream close to cancel rate limit context")
	}
}

func TestSessionStateStreamRateLimitMissingStream(t *testing.T) {
	session := newSessionState(11, GroupRuntime{ID: 1}, ConfigSnapshot{}, 0)
	ctx, limiters, ok := session.StreamRateLimit(404)
	if ok || ctx != nil || limiters.Downlink != nil || limiters.Uplink != nil {
		t.Fatalf("expected missing stream rate limit lookup to fail, got ok=%v ctx=%v limiters=%#v", ok, ctx, limiters)
	}
}
