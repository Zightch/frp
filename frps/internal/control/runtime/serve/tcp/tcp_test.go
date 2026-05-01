package tcp

import (
	"bytes"
	"context"
	"io"
	"net"
	"testing"
	"time"

	"github.com/zightch/frp/frps/pkg/protocol"
	"github.com/zightch/frp/frps/pkg/ratepolicy"
)

func TestPreparePublicStreamOpenBuildsFrameAndTracksStream(t *testing.T) {
	session := newFakeSession()
	publicConn := &fakeConn{remote: &net.TCPAddr{IP: net.ParseIP("198.51.100.10"), Port: 43000}}
	now := time.Date(2026, 4, 28, 12, 0, 0, 0, time.UTC)

	op, err := PreparePublicStreamOpen(session, 7, protocol.TunnelEntry{
		TunnelID: 9,
		Protocol: protocol.ProtocolTCP,
	}, 22000, publicConn, now)
	if err != nil {
		t.Fatalf("prepare stream open: %v", err)
	}
	if op.Blocked || op.StreamID == 0 || op.Stream == nil {
		t.Fatalf("unexpected operation: %#v", op)
	}
	if session.streams[op.StreamID] != op.Stream {
		t.Fatal("expected stream to be tracked")
	}
	if op.OpenFrame.Type != protocol.TypeStreamOpen || op.OpenFrame.StreamID != op.StreamID || op.OpenFrame.RequestID != op.Stream.OpenRequestID {
		t.Fatalf("unexpected open frame: %#v", op.OpenFrame)
	}
	body, err := protocol.UnmarshalStreamOpen(op.OpenFrame.Body)
	if err != nil {
		t.Fatalf("unmarshal stream.open: %v", err)
	}
	if body.TunnelID != 9 || body.RemotePort != 22000 || body.OpenedAtMs != uint64(now.UnixMilli()) {
		t.Fatalf("unexpected stream.open body: %#v", body)
	}
	if !body.ClientAddr.IP.Equal(net.ParseIP("198.51.100.10")) || body.ClientAddr.Port != 43000 {
		t.Fatalf("unexpected client addr: %#v", body.ClientAddr)
	}
}

func TestHandleStreamOpenedSignalsReady(t *testing.T) {
	session := newFakeSession()
	stream := &Stream{
		OpenRequestID: 77,
		Ready:         make(chan error, 1),
	}
	session.streams[12] = stream
	body := mustMarshalStreamOpened(t, protocol.StreamOpened{Status: protocol.StatusOK})

	if err := (Handler{}).HandleStreamOpened(&recordingWriter{}, session, protocol.Frame{
		Type:      protocol.TypeStreamOpened,
		RequestID: 77,
		StreamID:  12,
		Body:      body,
	}); err != nil {
		t.Fatalf("handle stream.opened: %v", err)
	}

	select {
	case err := <-stream.Ready:
		if err != nil {
			t.Fatalf("expected ready nil, got %v", err)
		}
	default:
		t.Fatal("expected stream ready signal")
	}
}

func TestHandleStreamDataWritesToPublicConn(t *testing.T) {
	session := newFakeSession()
	publicConn := &fakeConn{}
	session.streams[12] = &Stream{
		Conn:  publicConn,
		Ready: make(chan error, 1),
	}

	if err := (Handler{Clock: staticClock{now: time.Unix(10, 0)}}).HandleStreamData(&recordingWriter{}, session, protocol.Frame{
		Type:     protocol.TypeStreamData,
		StreamID: 12,
		Body:     []byte("payload"),
	}); err != nil {
		t.Fatalf("handle stream.data: %v", err)
	}
	if got := publicConn.writes.String(); got != "payload" {
		t.Fatalf("unexpected public write: %q", got)
	}
}

func TestCopyPublicToClientSendsDataAndClose(t *testing.T) {
	session := newFakeSession()
	stream := &Stream{
		Conn:  &fakeConn{reads: [][]byte{[]byte("payload")}, readErr: io.EOF},
		Ready: make(chan error, 1),
	}
	session.streams[12] = stream
	runtimeWriter := &recordingWriter{}
	controlWriter := &recordingWriter{}

	(Handler{Clock: staticClock{now: time.Unix(10, 0)}}).CopyPublicToClient(runtimeWriter, controlWriter, session, 12, stream)

	if len(runtimeWriter.frames) != 1 || runtimeWriter.frames[0].Type != protocol.TypeStreamData || string(runtimeWriter.frames[0].Body) != "payload" {
		t.Fatalf("unexpected runtime frames: %#v", runtimeWriter.frames)
	}
	if len(controlWriter.frames) != 1 || controlWriter.frames[0].Type != protocol.TypeStreamClose {
		t.Fatalf("unexpected control frames: %#v", controlWriter.frames)
	}
	closeBody, err := protocol.UnmarshalStreamClose(controlWriter.frames[0].Body)
	if err != nil {
		t.Fatalf("unmarshal stream.close: %v", err)
	}
	if closeBody.ReasonCode != protocol.CloseReasonEOF || closeBody.Message != "eof" {
		t.Fatalf("unexpected close body: %#v", closeBody)
	}
}

func TestCopyPublicToClientSplitsDownlinkByLimiterBurst(t *testing.T) {
	session := newFakeSession()
	stream := &Stream{
		Conn:  &fakeConn{reads: [][]byte{[]byte("payload")}, readErr: io.EOF},
		Ready: make(chan error, 1),
	}
	session.streams[12] = stream
	limiter := &fakeRateLimiter{config: ratepolicy.BucketConfig{RateBPS: 8_000, BurstBytes: 3}}
	session.rateLimits[12] = fakeStreamRateLimit{
		ctx: context.Background(),
		limiters: ratepolicy.TunnelLimiters{
			Downlink: limiter,
		},
	}
	runtimeWriter := &recordingWriter{}
	controlWriter := &recordingWriter{}

	(Handler{Clock: staticClock{now: time.Unix(10, 0)}}).CopyPublicToClient(runtimeWriter, controlWriter, session, 12, stream)

	if len(runtimeWriter.frames) != 3 {
		t.Fatalf("unexpected runtime frame count: %#v", runtimeWriter.frames)
	}
	if string(runtimeWriter.frames[0].Body) != "pay" || string(runtimeWriter.frames[1].Body) != "loa" || string(runtimeWriter.frames[2].Body) != "d" {
		t.Fatalf("unexpected runtime frame bodies: %#v", runtimeWriter.frames)
	}
	if len(limiter.waits) != 3 || limiter.waits[0] != 3 || limiter.waits[1] != 3 || limiter.waits[2] != 1 {
		t.Fatalf("unexpected limiter waits: %#v", limiter.waits)
	}
}

func TestCopyPublicToClientReturnsOnLimiterCancel(t *testing.T) {
	session := newFakeSession()
	stream := &Stream{
		Conn:  &fakeConn{reads: [][]byte{[]byte("payload")}},
		Ready: make(chan error, 1),
	}
	session.streams[12] = stream
	ctx, cancel := context.WithCancel(context.Background())
	limiter := &fakeRateLimiter{
		config: ratepolicy.BucketConfig{RateBPS: 8_000, BurstBytes: 3},
		wait: func(ctx context.Context, bytes int) error {
			cancel()
			<-ctx.Done()
			return ctx.Err()
		},
	}
	session.rateLimits[12] = fakeStreamRateLimit{
		ctx:      ctx,
		cancel:   cancel,
		limiters: ratepolicy.TunnelLimiters{Downlink: limiter},
	}
	runtimeWriter := &recordingWriter{}
	controlWriter := &recordingWriter{}

	(Handler{Clock: staticClock{now: time.Unix(10, 0)}}).CopyPublicToClient(runtimeWriter, controlWriter, session, 12, stream)

	if len(runtimeWriter.frames) != 0 {
		t.Fatalf("expected no runtime frames after limiter cancel, got %#v", runtimeWriter.frames)
	}
	if len(controlWriter.frames) != 0 {
		t.Fatalf("expected no control close after limiter cancel, got %#v", controlWriter.frames)
	}
}

func TestHandleStreamDataSplitsUplinkByLimiterBurst(t *testing.T) {
	session := newFakeSession()
	publicConn := &fakeConn{}
	session.streams[12] = &Stream{
		Conn:  publicConn,
		Ready: make(chan error, 1),
	}
	limiter := &fakeRateLimiter{config: ratepolicy.BucketConfig{RateBPS: 8_000, BurstBytes: 2}}
	session.rateLimits[12] = fakeStreamRateLimit{
		ctx:      context.Background(),
		limiters: ratepolicy.TunnelLimiters{Uplink: limiter},
	}

	if err := (Handler{Clock: staticClock{now: time.Unix(10, 0)}}).HandleStreamData(&recordingWriter{}, session, protocol.Frame{
		Type:     protocol.TypeStreamData,
		StreamID: 12,
		Body:     []byte("hello"),
	}); err != nil {
		t.Fatalf("handle stream.data: %v", err)
	}
	if got := publicConn.writes.String(); got != "hello" {
		t.Fatalf("unexpected public write: %q", got)
	}
	if len(limiter.waits) != 3 || limiter.waits[0] != 2 || limiter.waits[1] != 2 || limiter.waits[2] != 1 {
		t.Fatalf("unexpected uplink limiter waits: %#v", limiter.waits)
	}
}

func TestHandleStreamDataReturnsOnLimiterCancel(t *testing.T) {
	session := newFakeSession()
	publicConn := &fakeConn{}
	session.streams[12] = &Stream{
		Conn:  publicConn,
		Ready: make(chan error, 1),
	}
	ctx, cancel := context.WithCancel(context.Background())
	limiter := &fakeRateLimiter{
		config: ratepolicy.BucketConfig{RateBPS: 8_000, BurstBytes: 2},
		wait: func(ctx context.Context, bytes int) error {
			cancel()
			<-ctx.Done()
			return ctx.Err()
		},
	}
	session.rateLimits[12] = fakeStreamRateLimit{
		ctx:      ctx,
		cancel:   cancel,
		limiters: ratepolicy.TunnelLimiters{Uplink: limiter},
	}
	writer := &recordingWriter{}

	if err := (Handler{Clock: staticClock{now: time.Unix(10, 0)}}).HandleStreamData(writer, session, protocol.Frame{
		Type:     protocol.TypeStreamData,
		StreamID: 12,
		Body:     []byte("hello"),
	}); err != nil {
		t.Fatalf("handle stream.data: %v", err)
	}
	if got := publicConn.writes.String(); got != "" {
		t.Fatalf("expected no public writes after limiter cancel, got %q", got)
	}
	if len(writer.frames) != 0 {
		t.Fatalf("expected no control frames after limiter cancel, got %#v", writer.frames)
	}
}

func mustMarshalStreamOpened(t *testing.T, opened protocol.StreamOpened) []byte {
	t.Helper()
	body, err := protocol.MarshalStreamOpened(opened)
	if err != nil {
		t.Fatalf("marshal stream.opened: %v", err)
	}
	return body
}

type fakeSession struct {
	nextRequestID uint32
	nextStreamID  uint32
	streams       map[uint32]*Stream
	rateLimits    map[uint32]fakeStreamRateLimit
	admit         bool
}

func newFakeSession() *fakeSession {
	return &fakeSession{
		nextRequestID: 76,
		nextStreamID:  11,
		streams:       make(map[uint32]*Stream),
		rateLimits:    make(map[uint32]fakeStreamRateLimit),
		admit:         true,
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

func (s *fakeSession) AddPublicStream(streamID uint32, stream *Stream, _ uint64) bool {
	if !s.admit {
		return false
	}
	s.streams[streamID] = stream
	return true
}

func (s *fakeSession) PublicStream(streamID uint32) *Stream {
	return s.streams[streamID]
}

func (s *fakeSession) ClosePublicStream(streamID uint32) bool {
	stream, ok := s.streams[streamID]
	delete(s.streams, streamID)
	if entry, ok := s.rateLimits[streamID]; ok {
		delete(s.rateLimits, streamID)
		if entry.cancel != nil {
			entry.cancel()
		}
	}
	if ok && stream != nil {
		stream.SignalReady(net.ErrClosed)
	}
	return ok
}

func (s *fakeSession) StreamRateLimit(streamID uint32) (context.Context, ratepolicy.TunnelLimiters, bool) {
	entry, ok := s.rateLimits[streamID]
	if !ok {
		return nil, ratepolicy.TunnelLimiters{}, false
	}
	return entry.ctx, entry.limiters, true
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

type staticClock struct {
	now time.Time
}

func (c staticClock) Now() time.Time {
	return c.now
}

type fakeStreamRateLimit struct {
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

type fakeConn struct {
	reads   [][]byte
	readErr error
	writes  bytes.Buffer
	remote  net.Addr
}

func (c *fakeConn) Read(payload []byte) (int, error) {
	if len(c.reads) == 0 {
		if c.readErr != nil {
			return 0, c.readErr
		}
		return 0, io.EOF
	}
	next := c.reads[0]
	c.reads = c.reads[1:]
	copy(payload, next)
	return len(next), nil
}

func (c *fakeConn) Write(payload []byte) (int, error) {
	return c.writes.Write(payload)
}

func (c *fakeConn) Close() error {
	return nil
}

func (c *fakeConn) LocalAddr() net.Addr {
	return &net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: 7000}
}

func (c *fakeConn) RemoteAddr() net.Addr {
	if c.remote != nil {
		return c.remote
	}
	return &net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: 43000}
}

func (c *fakeConn) SetDeadline(time.Time) error {
	return nil
}

func (c *fakeConn) SetReadDeadline(time.Time) error {
	return nil
}

func (c *fakeConn) SetWriteDeadline(time.Time) error {
	return nil
}
