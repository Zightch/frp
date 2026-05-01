package udp

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/zightch/frp/frps/internal/clock"
	"github.com/zightch/frp/frps/pkg/protocol"
	"github.com/zightch/frp/frps/pkg/ratepolicy"
)

func TestPrepareDatagramForwardCreatesAndReusesSession(t *testing.T) {
	session := newFakeSession()
	listener := &fakeUDPListener{}
	clientAddr := &net.UDPAddr{IP: net.ParseIP("198.51.100.10"), Port: 53000}
	tunnel := protocol.TunnelEntry{
		TunnelID: 9,
		Protocol: protocol.ProtocolUDP,
	}

	first, err := PrepareDatagramForward(session, 30*time.Second, 7, tunnel, 22000, listener, clientAddr, []byte("hello"), time.Unix(10, 0))
	if err != nil {
		t.Fatalf("prepare first datagram: %v", err)
	}
	if first.Blocked || !first.Created || first.UDPSession == nil {
		t.Fatalf("unexpected first operation: %#v", first)
	}
	if len(first.Frames) != 2 || first.Frames[0].Type != protocol.TypeUDPOpen || first.Frames[1].Type != protocol.TypeUDPData {
		t.Fatalf("unexpected first frames: %#v", first.Frames)
	}
	open, err := protocol.UnmarshalUDPOpen(first.Frames[0].Body)
	if err != nil {
		t.Fatalf("unmarshal udp.open: %v", err)
	}
	if open.TunnelID != 9 || open.RemotePort != 22000 || open.IdleTimeoutMs != 30000 {
		t.Fatalf("unexpected udp.open body: %#v", open)
	}
	if !open.ClientAddr.IP.Equal(clientAddr.IP) || open.ClientAddr.Port != uint16(clientAddr.Port) {
		t.Fatalf("unexpected client addr: %#v", open.ClientAddr)
	}

	second, err := PrepareDatagramForward(session, 30*time.Second, 7, tunnel, 22000, listener, clientAddr, []byte("again"), time.Unix(11, 0))
	if err != nil {
		t.Fatalf("prepare second datagram: %v", err)
	}
	if second.Created || second.UDPSession.SessionID != first.UDPSession.SessionID {
		t.Fatalf("expected reused session, got %#v", second)
	}
	if len(second.Frames) != 1 || second.Frames[0].Type != protocol.TypeUDPData || string(second.Frames[0].Body) != "again" {
		t.Fatalf("unexpected second frames: %#v", second.Frames)
	}
}

func TestHandleUDPDataWritesToPublicListener(t *testing.T) {
	session := newFakeSession()
	listener := &fakeUDPListener{}
	udpSession := NewPublicSession(12, protocol.TunnelEntry{TunnelID: 9}, 22000, listener, &net.UDPAddr{IP: net.ParseIP("198.51.100.10"), Port: 53000}, 30*time.Second, time.Unix(10, 0))
	session.sessions[12] = udpSession

	if err := (Handler{Clock: staticClock{now: time.Unix(11, 0)}}).HandleUDPData(&recordingWriter{}, session, protocol.Frame{
		Type:     protocol.TypeUDPData,
		StreamID: 12,
		Body:     []byte("payload"),
	}); err != nil {
		t.Fatalf("handle udp.data: %v", err)
	}
	if string(listener.writes[0].payload) != "payload" || !listener.writes[0].addr.IP.Equal(net.ParseIP("198.51.100.10")) {
		t.Fatalf("unexpected udp write: %#v", listener.writes)
	}
}

func TestHandlePublicDatagramWaitsOnDownlinkLimiterWithoutSplitting(t *testing.T) {
	session := newFakeSession()
	listener := &fakeUDPListener{}
	clientAddr := &net.UDPAddr{IP: net.ParseIP("198.51.100.10"), Port: 53000}
	tunnel := protocol.TunnelEntry{TunnelID: 9, Protocol: protocol.ProtocolUDP}
	udpSession := NewPublicSession(12, tunnel, 22000, listener, clientAddr, 30*time.Second, time.Unix(10, 0))
	session.sessions[12] = udpSession
	session.keys[udpSession.Key()] = udpSession.SessionID
	limiter := &fakeRateLimiter{config: ratepolicy.BucketConfig{RateBPS: 8_000, BurstBytes: 2}}
	session.rateLimits[12] = fakeUDPSessionRateLimit{
		ctx:      context.Background(),
		limiters: ratepolicy.TunnelLimiters{Downlink: limiter},
	}
	runtimeWriter := &recordingWriter{}

	err := (Handler{Clock: staticClock{now: time.Unix(11, 0)}}).HandlePublicDatagram(ServeContext{
		Session:       session,
		RuntimeWriter: runtimeWriter,
		ConfigVersion: 7,
		Tunnel:        tunnel,
		RemotePort:    22000,
	}, listener, clientAddr, []byte("hello"))
	if err != nil {
		t.Fatalf("handle public datagram: %v", err)
	}
	if len(runtimeWriter.frames) != 1 || runtimeWriter.frames[0].Type != protocol.TypeUDPData || string(runtimeWriter.frames[0].Body) != "hello" {
		t.Fatalf("unexpected runtime frames: %#v", runtimeWriter.frames)
	}
	if len(limiter.waits) != 1 || limiter.waits[0] != 5 {
		t.Fatalf("unexpected limiter waits: %#v", limiter.waits)
	}
}

func TestHandlePublicDatagramReturnsOnLimiterCancel(t *testing.T) {
	session := newFakeSession()
	listener := &fakeUDPListener{}
	clientAddr := &net.UDPAddr{IP: net.ParseIP("198.51.100.10"), Port: 53000}
	tunnel := protocol.TunnelEntry{TunnelID: 9, Protocol: protocol.ProtocolUDP}
	udpSession := NewPublicSession(12, tunnel, 22000, listener, clientAddr, 30*time.Second, time.Unix(10, 0))
	session.sessions[12] = udpSession
	session.keys[udpSession.Key()] = udpSession.SessionID
	ctx, cancel := context.WithCancel(context.Background())
	limiter := &fakeRateLimiter{
		config: ratepolicy.BucketConfig{RateBPS: 8_000, BurstBytes: 2},
		wait: func(ctx context.Context, bytes int) error {
			cancel()
			<-ctx.Done()
			return ctx.Err()
		},
	}
	session.rateLimits[12] = fakeUDPSessionRateLimit{
		ctx:      ctx,
		cancel:   cancel,
		limiters: ratepolicy.TunnelLimiters{Downlink: limiter},
	}
	runtimeWriter := &recordingWriter{}

	err := (Handler{Clock: staticClock{now: time.Unix(11, 0)}}).HandlePublicDatagram(ServeContext{
		Session:       session,
		RuntimeWriter: runtimeWriter,
		ConfigVersion: 7,
		Tunnel:        tunnel,
		RemotePort:    22000,
	}, listener, clientAddr, []byte("hello"))
	if err != nil {
		t.Fatalf("handle public datagram: %v", err)
	}
	if len(runtimeWriter.frames) != 0 {
		t.Fatalf("expected no runtime frames after limiter cancel, got %#v", runtimeWriter.frames)
	}
}

func TestHandleUDPDataSendsCloseForMissingSession(t *testing.T) {
	writer := &recordingWriter{}

	if err := (Handler{}).HandleUDPData(writer, newFakeSession(), protocol.Frame{
		Type:     protocol.TypeUDPData,
		StreamID: 12,
		Body:     []byte("payload"),
	}); err != nil {
		t.Fatalf("handle missing udp session: %v", err)
	}
	if len(writer.frames) != 1 || writer.frames[0].Type != protocol.TypeUDPClose || writer.frames[0].StreamID != 12 {
		t.Fatalf("unexpected close frame: %#v", writer.frames)
	}
	closeBody, err := protocol.UnmarshalUDPClose(writer.frames[0].Body)
	if err != nil {
		t.Fatalf("unmarshal udp.close: %v", err)
	}
	if closeBody.ReasonCode != protocol.CloseReasonProtocolError {
		t.Fatalf("unexpected close body: %#v", closeBody)
	}
}

func TestHandleUDPDataWaitsOnUplinkLimiterWithoutSplitting(t *testing.T) {
	session := newFakeSession()
	listener := &fakeUDPListener{}
	udpSession := NewPublicSession(12, protocol.TunnelEntry{TunnelID: 9}, 22000, listener, &net.UDPAddr{IP: net.ParseIP("198.51.100.10"), Port: 53000}, 30*time.Second, time.Unix(10, 0))
	session.sessions[12] = udpSession
	limiter := &fakeRateLimiter{config: ratepolicy.BucketConfig{RateBPS: 8_000, BurstBytes: 2}}
	session.rateLimits[12] = fakeUDPSessionRateLimit{
		ctx:      context.Background(),
		limiters: ratepolicy.TunnelLimiters{Uplink: limiter},
	}

	if err := (Handler{Clock: staticClock{now: time.Unix(11, 0)}}).HandleUDPData(&recordingWriter{}, session, protocol.Frame{
		Type:     protocol.TypeUDPData,
		StreamID: 12,
		Body:     []byte("payload"),
	}); err != nil {
		t.Fatalf("handle udp.data: %v", err)
	}
	if len(listener.writes) != 1 || string(listener.writes[0].payload) != "payload" {
		t.Fatalf("unexpected udp writes: %#v", listener.writes)
	}
	if len(limiter.waits) != 1 || limiter.waits[0] != len("payload") {
		t.Fatalf("unexpected limiter waits: %#v", limiter.waits)
	}
}

func TestHandleUDPDataReturnsOnLimiterCancel(t *testing.T) {
	session := newFakeSession()
	listener := &fakeUDPListener{}
	udpSession := NewPublicSession(12, protocol.TunnelEntry{TunnelID: 9}, 22000, listener, &net.UDPAddr{IP: net.ParseIP("198.51.100.10"), Port: 53000}, 30*time.Second, time.Unix(10, 0))
	session.sessions[12] = udpSession
	ctx, cancel := context.WithCancel(context.Background())
	limiter := &fakeRateLimiter{
		config: ratepolicy.BucketConfig{RateBPS: 8_000, BurstBytes: 2},
		wait: func(ctx context.Context, bytes int) error {
			cancel()
			<-ctx.Done()
			return ctx.Err()
		},
	}
	session.rateLimits[12] = fakeUDPSessionRateLimit{
		ctx:      ctx,
		cancel:   cancel,
		limiters: ratepolicy.TunnelLimiters{Uplink: limiter},
	}
	writer := &recordingWriter{}

	if err := (Handler{Clock: staticClock{now: time.Unix(11, 0)}}).HandleUDPData(writer, session, protocol.Frame{
		Type:     protocol.TypeUDPData,
		StreamID: 12,
		Body:     []byte("payload"),
	}); err != nil {
		t.Fatalf("handle udp.data: %v", err)
	}
	if len(listener.writes) != 0 {
		t.Fatalf("expected no udp writes after limiter cancel, got %#v", listener.writes)
	}
	if len(writer.frames) != 0 {
		t.Fatalf("expected no control frames after limiter cancel, got %#v", writer.frames)
	}
}

func TestServeIdleCleanupUsesManualScheduler(t *testing.T) {
	manual := clock.NewManual(time.Date(2026, 4, 28, 12, 0, 0, 0, time.UTC))
	session := newFakeSession()
	session.idle = []*Session{{
		SessionID: 12,
		TunnelID:  9,
		PublicAddr: &net.UDPAddr{
			IP:   net.ParseIP("198.51.100.10"),
			Port: 53000,
		},
	}}
	writer := &recordingWriter{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan struct{})
	go func() {
		defer close(done)
		(Handler{Scheduler: manual, IdleSweep: time.Second}).ServeIdleCleanup(ctx, writer, discardLogger{}, session)
	}()

	waitForManualJob(t, manual, "control.udp_idle_cleanup")
	manual.Advance(time.Second)
	manual.WaitIdle()

	if len(writer.frames) != 1 || writer.frames[0].Type != protocol.TypeUDPClose || writer.frames[0].StreamID != 12 {
		t.Fatalf("unexpected cleanup frames: %#v", writer.frames)
	}

	close(session.done)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("idle cleanup did not stop after session done")
	}
}

type fakeSession struct {
	nextRequestID uint32
	nextStreamID  uint32
	sessions      map[uint32]*Session
	keys          map[string]uint32
	rateLimits    map[uint32]fakeUDPSessionRateLimit
	admit         bool
	idle          []*Session
	done          chan struct{}
}

func newFakeSession() *fakeSession {
	return &fakeSession{
		nextRequestID: 76,
		nextStreamID:  11,
		sessions:      make(map[uint32]*Session),
		keys:          make(map[string]uint32),
		rateLimits:    make(map[uint32]fakeUDPSessionRateLimit),
		admit:         true,
		done:          make(chan struct{}),
	}
}

func (s *fakeSession) NextTunnelStreamID() uint32 {
	s.nextStreamID++
	return s.nextStreamID
}

func (s *fakeSession) NextRequestID() uint32 {
	s.nextRequestID++
	return s.nextRequestID
}

func (s *fakeSession) BindPublicUDPSession(udpSession *Session, _ uint64) (*Session, bool) {
	if !s.admit {
		return nil, false
	}
	if sessionID, ok := s.keys[udpSession.Key()]; ok {
		return s.sessions[sessionID], false
	}
	s.sessions[udpSession.SessionID] = udpSession
	s.keys[udpSession.Key()] = udpSession.SessionID
	return udpSession, true
}

func (s *fakeSession) PublicUDPSession(sessionID uint32) *Session {
	return s.sessions[sessionID]
}

func (s *fakeSession) ClosePublicUDPSession(sessionID uint32) bool {
	udpSession, ok := s.sessions[sessionID]
	delete(s.sessions, sessionID)
	if udpSession != nil {
		delete(s.keys, udpSession.Key())
	}
	if entry, ok := s.rateLimits[sessionID]; ok {
		delete(s.rateLimits, sessionID)
		if entry.cancel != nil {
			entry.cancel()
		}
	}
	return ok
}

func (s *fakeSession) TakeIdlePublicUDPSessions(time.Time) []*Session {
	idle := s.idle
	s.idle = nil
	return idle
}

func (s *fakeSession) UDPSessionRateLimit(sessionID uint32) (context.Context, ratepolicy.TunnelLimiters, bool) {
	entry, ok := s.rateLimits[sessionID]
	if !ok {
		return nil, ratepolicy.TunnelLimiters{}, false
	}
	return entry.ctx, entry.limiters, true
}

func (s *fakeSession) DoneCh() <-chan struct{} {
	return s.done
}

type recordingWriter struct {
	frames []protocol.Frame
	err    error
}

func (w *recordingWriter) WriteFrame(frame protocol.Frame) error {
	if w.err != nil {
		return w.err
	}
	w.frames = append(w.frames, frame)
	return nil
}

func (w *recordingWriter) WriteFrames(frames ...protocol.Frame) error {
	if w.err != nil {
		return w.err
	}
	w.frames = append(w.frames, frames...)
	return nil
}

type staticClock struct {
	now time.Time
}

func (c staticClock) Now() time.Time {
	return c.now
}

type discardLogger struct{}

func (discardLogger) Info(string, ...any) {}
func (discardLogger) Warn(string, ...any) {}

type fakeUDPSessionRateLimit struct {
	ctx      context.Context
	cancel   context.CancelFunc
	limiters ratepolicy.TunnelLimiters
}

type fakeRateLimiter struct {
	config ratepolicy.BucketConfig
	waits  []int
	wait   func(context.Context, int) error
}

func (l *fakeRateLimiter) Config() ratepolicy.BucketConfig {
	return l.config
}

func (l *fakeRateLimiter) WaitN(ctx context.Context, bytes int) error {
	l.waits = append(l.waits, bytes)
	if l.wait != nil {
		return l.wait(ctx, bytes)
	}
	return nil
}

type udpWrite struct {
	payload []byte
	addr    *net.UDPAddr
}

type fakeUDPListener struct {
	writes []udpWrite
}

func (l *fakeUDPListener) Close() error { return nil }

func (l *fakeUDPListener) LocalAddr() net.Addr {
	return &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 22000}
}

func (l *fakeUDPListener) ReadFromUDP([]byte) (int, *net.UDPAddr, error) {
	return 0, nil, net.ErrClosed
}

func (l *fakeUDPListener) WriteToUDP(payload []byte, addr *net.UDPAddr) (int, error) {
	l.writes = append(l.writes, udpWrite{
		payload: append([]byte(nil), payload...),
		addr:    CloneUDPAddr(addr),
	})
	return len(payload), nil
}

func waitForManualJob(t *testing.T, manual *clock.Manual, name string) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		for _, pending := range manual.PendingJobNames() {
			if pending == name {
				return
			}
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("manual scheduler job %q was not registered", name)
}
