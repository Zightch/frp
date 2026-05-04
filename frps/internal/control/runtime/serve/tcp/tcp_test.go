package tcp

import (
	"bytes"
	"net"
	"testing"
	"time"

	"github.com/zightch/frp/frps/pkg/protocol"
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

func TestHandleStreamOpenedSignalsError(t *testing.T) {
	session := newFakeSession()
	stream := &Stream{
		OpenRequestID: 91,
		Ready:         make(chan error, 1),
	}
	session.streams[12] = stream
	body := mustMarshalStreamOpened(t, protocol.StreamOpened{
		Status:    protocol.StatusError,
		ErrorCode: protocol.ErrorCodeStreamLocalDialFailed,
		Message:   "dial failed",
	})

	if err := (Handler{}).HandleStreamOpened(&recordingWriter{}, session, protocol.Frame{
		Type:      protocol.TypeStreamOpened,
		RequestID: 91,
		StreamID:  12,
		Body:      body,
	}); err != nil {
		t.Fatalf("handle stream.opened: %v", err)
	}

	select {
	case err := <-stream.Ready:
		if err == nil || err.Error() != "dial failed" {
			t.Fatalf("unexpected ready error: %v", err)
		}
	default:
		t.Fatal("expected stream ready error signal")
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
	admit         bool
}

func newFakeSession() *fakeSession {
	return &fakeSession{
		nextRequestID: 76,
		nextStreamID:  11,
		streams:       make(map[uint32]*Stream),
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
	if ok && stream != nil {
		stream.SignalReady(net.ErrClosed)
	}
	return ok
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

type fakeConn struct {
	writes bytes.Buffer
	remote net.Addr
}

func (c *fakeConn) Read([]byte) (int, error) {
	return 0, net.ErrClosed
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
