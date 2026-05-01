package wiring

import (
	"net"
	"testing"
	"time"

	"github.com/zightch/frp/frps/pkg/protocol"
	"github.com/zightch/frp/frps/pkg/ratepolicy"
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

func TestSessionStateFreezeTunnelRuntimeRebuildsStreamRateLimitersAfterReload(t *testing.T) {
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

	if !session.AddPublicStream(101, &publicStream{Conn: serverA, Tunnel: tunnel, Ready: make(chan error, 1)}, snapshot.Version) {
		t.Fatal("expected initial stream to register")
	}

	ctxA, limitersA, ok := session.StreamRateLimit(101)
	if !ok || ctxA == nil {
		t.Fatal("expected initial stream rate limit context")
	}

	session.FreezeTunnelRuntime()
	select {
	case <-ctxA.Done():
	default:
		t.Fatal("expected freeze to cancel initial stream rate limit context")
	}

	session.AllowTunnelRuntimeStart()
	session.RuntimeMu.Lock()
	session.Runtime.Listeners.Started = true
	session.Runtime.Generation = 4
	session.RuntimeMu.Unlock()

	reloadedTunnel := tunnel
	reloadedTunnel.RatePolicy.DownlinkBPS = 20_000_000
	reloadedTunnel.RatePolicy.UplinkBPS = 8_000_000

	clientB, serverB := net.Pipe()
	defer clientB.Close()
	defer serverB.Close()

	if !session.AddPublicStream(202, &publicStream{Conn: serverB, Tunnel: reloadedTunnel, Ready: make(chan error, 1)}, 4) {
		t.Fatal("expected reloaded stream to register")
	}

	_, limitersB, ok := session.StreamRateLimit(202)
	if !ok {
		t.Fatal("expected reloaded stream rate limit entry")
	}
	if limitersA.Downlink == limitersB.Downlink || limitersA.Uplink == limitersB.Uplink {
		t.Fatal("expected reload to rebuild tunnel limiter instances")
	}
	if got := limitersB.Downlink.Config(); got != (ratepolicy.BucketConfig{RateBPS: 20_000_000, BurstBytes: 2_500_000}) {
		t.Fatalf("unexpected reloaded downlink config: %#v", got)
	}
	if got := limitersB.Uplink.Config(); got != (ratepolicy.BucketConfig{RateBPS: 8_000_000, BurstBytes: 1_000_000}) {
		t.Fatalf("unexpected reloaded uplink config: %#v", got)
	}
}

func TestSessionStateStreamRateLimitMissingStream(t *testing.T) {
	session := newSessionState(11, GroupRuntime{ID: 1}, ConfigSnapshot{}, 0)
	ctx, limiters, ok := session.StreamRateLimit(404)
	if ok || ctx != nil || limiters.Downlink != nil || limiters.Uplink != nil {
		t.Fatalf("expected missing stream rate limit lookup to fail, got ok=%v ctx=%v limiters=%#v", ok, ctx, limiters)
	}
}

func TestSessionStateBindPublicUDPSessionRegistersTunnelRateLimiters(t *testing.T) {
	tunnel := protocol.TunnelEntry{
		TunnelID:    7,
		Protocol:    protocol.ProtocolUDP,
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

	listener := &rateLimitTestUDPListener{}
	sessionA, created := session.BindPublicUDPSession(newPublicUDPSession(201, tunnel, 20000, listener, &net.UDPAddr{IP: net.ParseIP("198.51.100.10"), Port: 53000}, time.Now().UTC()), snapshot.Version)
	if sessionA == nil || !created {
		t.Fatal("expected first udp session to register")
	}
	sessionB, created := session.BindPublicUDPSession(newPublicUDPSession(202, tunnel, 20000, listener, &net.UDPAddr{IP: net.ParseIP("198.51.100.11"), Port: 53001}, time.Now().UTC()), snapshot.Version)
	if sessionB == nil || !created {
		t.Fatal("expected second udp session to register")
	}

	ctxA, limitersA, ok := session.UDPSessionRateLimit(sessionA.SessionID)
	if !ok || ctxA == nil {
		t.Fatal("expected first udp session rate limit entry")
	}
	_, limitersB, ok := session.UDPSessionRateLimit(sessionB.SessionID)
	if !ok {
		t.Fatal("expected second udp session rate limit entry")
	}
	if limitersA.Downlink == nil || limitersA.Uplink == nil {
		t.Fatalf("expected independent udp tunnel limiters, got %#v", limitersA)
	}
	if limitersA.Downlink != limitersB.Downlink || limitersA.Uplink != limitersB.Uplink {
		t.Fatal("udp sessions on the same tunnel must share the same limiter pair")
	}
}

func TestSessionStateBindPublicUDPSessionRegistersSharedPolicyAcrossTunnels(t *testing.T) {
	tunnelA := protocol.TunnelEntry{
		TunnelID:    7,
		Protocol:    protocol.ProtocolUDP,
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

	listener := &rateLimitTestUDPListener{}
	sessionA, created := session.BindPublicUDPSession(newPublicUDPSession(201, tunnelA, 20000, listener, &net.UDPAddr{IP: net.ParseIP("198.51.100.10"), Port: 53000}, time.Now().UTC()), snapshot.Version)
	if sessionA == nil || !created {
		t.Fatal("expected first shared udp session to register")
	}
	sessionB, created := session.BindPublicUDPSession(newPublicUDPSession(202, tunnelB, 20001, listener, &net.UDPAddr{IP: net.ParseIP("198.51.100.11"), Port: 53001}, time.Now().UTC()), snapshot.Version)
	if sessionB == nil || !created {
		t.Fatal("expected second shared udp session to register")
	}

	_, limitersA, ok := session.UDPSessionRateLimit(sessionA.SessionID)
	if !ok {
		t.Fatal("expected first shared udp session rate limit entry")
	}
	_, limitersB, ok := session.UDPSessionRateLimit(sessionB.SessionID)
	if !ok {
		t.Fatal("expected second shared udp session rate limit entry")
	}
	if limitersA.Downlink != limitersB.Downlink || limitersA.Uplink != limitersB.Uplink {
		t.Fatal("shared policy must reuse udp limiter pair across tunnels")
	}
}

func TestSessionStateClosePublicUDPSessionCancelsRateLimitContext(t *testing.T) {
	tunnel := protocol.TunnelEntry{
		TunnelID:    7,
		Protocol:    protocol.ProtocolUDP,
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

	listener := &rateLimitTestUDPListener{}
	udpSession, created := session.BindPublicUDPSession(newPublicUDPSession(201, tunnel, 20000, listener, &net.UDPAddr{IP: net.ParseIP("198.51.100.10"), Port: 53000}, time.Now().UTC()), snapshot.Version)
	if udpSession == nil || !created {
		t.Fatal("expected udp session to register")
	}

	ctx, _, ok := session.UDPSessionRateLimit(udpSession.SessionID)
	if !ok || ctx == nil {
		t.Fatal("expected udp session rate limit context")
	}

	if !session.ClosePublicUDPSession(udpSession.SessionID) {
		t.Fatal("expected udp session close to succeed")
	}
	select {
	case <-ctx.Done():
	default:
		t.Fatal("expected udp session close to cancel rate limit context")
	}
}

func TestSessionStateFreezeTunnelRuntimeCancelsUDPRateLimitContext(t *testing.T) {
	tunnel := protocol.TunnelEntry{
		TunnelID:    7,
		Protocol:    protocol.ProtocolUDP,
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

	listener := &rateLimitTestUDPListener{}
	udpSession, created := session.BindPublicUDPSession(newPublicUDPSession(201, tunnel, 20000, listener, &net.UDPAddr{IP: net.ParseIP("198.51.100.10"), Port: 53000}, time.Now().UTC()), snapshot.Version)
	if udpSession == nil || !created {
		t.Fatal("expected udp session to register")
	}

	ctx, _, ok := session.UDPSessionRateLimit(udpSession.SessionID)
	if !ok || ctx == nil {
		t.Fatal("expected udp session rate limit context")
	}

	session.FreezeTunnelRuntime()
	select {
	case <-ctx.Done():
	default:
		t.Fatal("expected freeze to cancel udp session rate limit context")
	}
}

func TestSessionStateTakeIdlePublicUDPSessionsSkipsActiveTransfer(t *testing.T) {
	tunnel := protocol.TunnelEntry{
		TunnelID:    7,
		Protocol:    protocol.ProtocolUDP,
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

	listener := &rateLimitTestUDPListener{}
	udpSession, created := session.BindPublicUDPSession(newPublicUDPSession(201, tunnel, 20000, listener, &net.UDPAddr{IP: net.ParseIP("198.51.100.10"), Port: 53000}, time.Now().UTC()), snapshot.Version)
	if udpSession == nil || !created {
		t.Fatal("expected udp session to register")
	}
	ctx, _, ok := session.UDPSessionRateLimit(udpSession.SessionID)
	if !ok || ctx == nil {
		t.Fatal("expected udp session rate limit context")
	}

	udpSession.Touch(time.Now().Add(-defaultUDPIdleTimeout - time.Second))
	release := udpSession.BeginTransfer()
	if idle := session.TakeIdlePublicUDPSessions(time.Now()); len(idle) != 0 {
		t.Fatalf("expected active transfer to block idle cleanup, got %#v", idle)
	}
	if session.PublicUDPSession(udpSession.SessionID) == nil {
		t.Fatal("expected udp session to remain active while transfer is in flight")
	}

	release()
	idle := session.TakeIdlePublicUDPSessions(time.Now())
	if len(idle) != 1 || idle[0].SessionID != udpSession.SessionID {
		t.Fatalf("expected idle cleanup to take udp session after transfer release, got %#v", idle)
	}
	select {
	case <-ctx.Done():
	default:
		t.Fatal("expected idle cleanup to cancel udp session rate limit context")
	}
}

func TestSessionStateUDPSessionRateLimitMissingSession(t *testing.T) {
	session := newSessionState(11, GroupRuntime{ID: 1}, ConfigSnapshot{}, 0)
	ctx, limiters, ok := session.UDPSessionRateLimit(404)
	if ok || ctx != nil || limiters.Downlink != nil || limiters.Uplink != nil {
		t.Fatalf("expected missing udp session rate limit lookup to fail, got ok=%v ctx=%v limiters=%#v", ok, ctx, limiters)
	}
}

type rateLimitTestUDPListener struct{}

func (rateLimitTestUDPListener) Close() error { return nil }

func (rateLimitTestUDPListener) LocalAddr() net.Addr {
	return &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 22000}
}

func (rateLimitTestUDPListener) ReadFromUDP([]byte) (int, *net.UDPAddr, error) {
	return 0, nil, net.ErrClosed
}

func (rateLimitTestUDPListener) WriteToUDP(payload []byte, addr *net.UDPAddr) (int, error) {
	return len(payload), nil
}
