package wiring

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"testing"
	"time"

	"github.com/zightch/frp/frps/pkg/ratepolicy"
)

func TestCopyTCPRawConnSplitsPayloadByLimiterBurst(t *testing.T) {
	src := &fakeRelayConn{
		reads:   [][]byte{[]byte("payload")},
		readErr: io.EOF,
	}
	dst := &fakeRelayConn{supportCloseWrite: true}
	limiter := &fakeRelayLimiter{
		config: ratepolicy.BucketConfig{
			RateBPS:    8_000,
			BurstBytes: 3,
		},
	}
	stream := &publicStream{}

	if err := copyTCPRawConn(dst, src, context.Background(), limiter, stream); err != nil {
		t.Fatalf("copy tcp raw conn: %v", err)
	}
	if got := dst.writes.String(); got != "payload" {
		t.Fatalf("unexpected dst payload: %q", got)
	}
	if len(limiter.waits) != 3 || limiter.waits[0] != 3 || limiter.waits[1] != 3 || limiter.waits[2] != 1 {
		t.Fatalf("unexpected limiter waits: %#v", limiter.waits)
	}
	if dst.closeWriteCalls != 1 {
		t.Fatalf("expected one close-write call, got %d", dst.closeWriteCalls)
	}
	if dst.readFromCalls != 0 {
		t.Fatalf("expected limiter path to avoid fast-path ReaderFrom, got %d calls", dst.readFromCalls)
	}
	if stream.LastActiveUnixMs.Load() == 0 {
		t.Fatal("expected stream last-active timestamp to be updated")
	}
}

func TestCopyTCPRawConnUsesFastPathWithoutLimiter(t *testing.T) {
	src := &fakeRelayConn{
		reads:   [][]byte{[]byte("payload")},
		readErr: io.EOF,
	}
	dst := &fakeRelayConn{supportCloseWrite: true}
	stream := &publicStream{}

	if err := copyTCPRawConn(dst, src, context.Background(), nil, stream); err != nil {
		t.Fatalf("copy tcp raw conn: %v", err)
	}
	if got := dst.writes.String(); got != "payload" {
		t.Fatalf("unexpected dst payload: %q", got)
	}
	if dst.readFromCalls != 1 {
		t.Fatalf("expected one fast-path ReaderFrom call, got %d", dst.readFromCalls)
	}
	if dst.closeWriteCalls != 1 {
		t.Fatalf("expected one close-write call, got %d", dst.closeWriteCalls)
	}
	if stream.LastActiveUnixMs.Load() == 0 {
		t.Fatal("expected stream last-active timestamp to be updated")
	}
}

func TestCopyTCPRawConnReturnsOnLimiterCancel(t *testing.T) {
	src := &fakeRelayConn{
		reads: [][]byte{[]byte("payload")},
	}
	dst := &fakeRelayConn{supportCloseWrite: true}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	limiter := &fakeRelayLimiter{
		config: ratepolicy.BucketConfig{
			RateBPS:    8_000,
			BurstBytes: 3,
		},
		wait: func(ctx context.Context, _ int) error {
			cancel()
			<-ctx.Done()
			return ctx.Err()
		},
	}

	err := copyTCPRawConn(dst, src, ctx, limiter, &publicStream{})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context canceled, got %v", err)
	}
	if got := dst.writes.String(); got != "" {
		t.Fatalf("expected no dst payload after cancel, got %q", got)
	}
	if dst.closeWriteCalls != 0 || dst.closeCalls != 0 {
		t.Fatalf("expected no half-close on limiter cancel, got closeWrite=%d close=%d", dst.closeWriteCalls, dst.closeCalls)
	}
}

func TestCopyTCPRawConnClosesConnWhenHalfCloseUnsupported(t *testing.T) {
	src := &fakeRelayConnNoCloseWrite{
		reads:   [][]byte{[]byte("ok")},
		readErr: io.EOF,
	}
	dst := &fakeRelayConnNoCloseWrite{}

	if err := copyTCPRawConn(dst, src, context.Background(), nil, &publicStream{}); err != nil {
		t.Fatalf("copy tcp raw conn: %v", err)
	}
	if dst.closeCalls != 1 {
		t.Fatalf("expected dst close fallback, got %d", dst.closeCalls)
	}
}

func TestIsTCPListenerClosedRecognizesWrappedNetErrClosed(t *testing.T) {
	err := &net.OpError{Op: "accept", Net: "tcp", Err: net.ErrClosed}
	if !isTCPListenerClosed(err) {
		t.Fatalf("expected wrapped net.ErrClosed to stop accept loop, got %v", err)
	}
}

type fakeRelayLimiter struct {
	config ratepolicy.BucketConfig
	waits  []int
	wait   func(context.Context, int) error
}

func (l *fakeRelayLimiter) Config() ratepolicy.BucketConfig {
	return l.config
}

func (l *fakeRelayLimiter) WaitN(ctx context.Context, bytes int) error {
	l.waits = append(l.waits, bytes)
	if l.wait != nil {
		return l.wait(ctx, bytes)
	}
	return nil
}

type fakeRelayConn struct {
	reads             [][]byte
	readErr           error
	writeErr          error
	writes            bytes.Buffer
	closeCalls        int
	closeWriteCalls   int
	readFromCalls     int
	supportCloseWrite bool
}

func (c *fakeRelayConn) Read(payload []byte) (int, error) {
	if len(c.reads) == 0 {
		if c.readErr != nil {
			return 0, c.readErr
		}
		return 0, io.EOF
	}
	next := c.reads[0]
	if len(next) <= len(payload) {
		c.reads = c.reads[1:]
	} else {
		c.reads[0] = next[len(payload):]
		next = next[:len(payload)]
	}
	n := copy(payload, next)
	return n, c.readErrIfDone()
}

func (c *fakeRelayConn) readErrIfDone() error {
	if len(c.reads) == 0 && c.readErr != nil {
		return c.readErr
	}
	return nil
}

func (c *fakeRelayConn) Write(payload []byte) (int, error) {
	if c.writeErr != nil {
		return 0, c.writeErr
	}
	return c.writes.Write(payload)
}

func (c *fakeRelayConn) Close() error {
	c.closeCalls++
	return nil
}

func (c *fakeRelayConn) CloseWrite() error {
	if !c.supportCloseWrite {
		return net.ErrClosed
	}
	c.closeWriteCalls++
	return nil
}

func (c *fakeRelayConn) ReadFrom(r io.Reader) (int64, error) {
	c.readFromCalls++
	return io.Copy(&c.writes, r)
}

func (c *fakeRelayConn) LocalAddr() net.Addr {
	return &net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: 7000}
}

func (c *fakeRelayConn) RemoteAddr() net.Addr {
	return &net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: 43000}
}

func (c *fakeRelayConn) SetDeadline(time.Time) error {
	return nil
}

func (c *fakeRelayConn) SetReadDeadline(time.Time) error {
	return nil
}

func (c *fakeRelayConn) SetWriteDeadline(time.Time) error {
	return nil
}

type fakeRelayConnNoCloseWrite struct {
	reads      [][]byte
	readErr    error
	writeErr   error
	writes     bytes.Buffer
	closeCalls int
}

func (c *fakeRelayConnNoCloseWrite) Read(payload []byte) (int, error) {
	if len(c.reads) == 0 {
		if c.readErr != nil {
			return 0, c.readErr
		}
		return 0, io.EOF
	}
	next := c.reads[0]
	if len(next) <= len(payload) {
		c.reads = c.reads[1:]
	} else {
		c.reads[0] = next[len(payload):]
		next = next[:len(payload)]
	}
	n := copy(payload, next)
	if len(c.reads) == 0 && c.readErr != nil {
		return n, c.readErr
	}
	return n, nil
}

func (c *fakeRelayConnNoCloseWrite) Write(payload []byte) (int, error) {
	if c.writeErr != nil {
		return 0, c.writeErr
	}
	return c.writes.Write(payload)
}

func (c *fakeRelayConnNoCloseWrite) Close() error {
	c.closeCalls++
	return nil
}

func (c *fakeRelayConnNoCloseWrite) LocalAddr() net.Addr {
	return &net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: 7000}
}

func (c *fakeRelayConnNoCloseWrite) RemoteAddr() net.Addr {
	return &net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: 43000}
}

func (c *fakeRelayConnNoCloseWrite) SetDeadline(time.Time) error {
	return nil
}

func (c *fakeRelayConnNoCloseWrite) SetReadDeadline(time.Time) error {
	return nil
}

func (c *fakeRelayConnNoCloseWrite) SetWriteDeadline(time.Time) error {
	return nil
}
