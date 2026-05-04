package client

import (
	"bytes"
	"io"
	"net"
	"testing"
	"time"
)

func TestCopyTCPRawUsesFastPath(t *testing.T) {
	src := &fakeRawRelayConn{
		reads:   [][]byte{[]byte("payload")},
		readErr: io.EOF,
	}
	dst := &fakeRawRelayConn{supportCloseWrite: true}

	if err := copyTCPRaw(dst, src); err != nil {
		t.Fatalf("copy tcp raw: %v", err)
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
}

type fakeRawRelayConn struct {
	reads             [][]byte
	readErr           error
	writeErr          error
	writes            bytes.Buffer
	closeCalls        int
	closeWriteCalls   int
	readFromCalls     int
	supportCloseWrite bool
}

func (c *fakeRawRelayConn) Read(payload []byte) (int, error) {
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

func (c *fakeRawRelayConn) Write(payload []byte) (int, error) {
	if c.writeErr != nil {
		return 0, c.writeErr
	}
	return c.writes.Write(payload)
}

func (c *fakeRawRelayConn) ReadFrom(r io.Reader) (int64, error) {
	c.readFromCalls++
	return io.Copy(&c.writes, r)
}

func (c *fakeRawRelayConn) Close() error {
	c.closeCalls++
	return nil
}

func (c *fakeRawRelayConn) CloseWrite() error {
	if !c.supportCloseWrite {
		return net.ErrClosed
	}
	c.closeWriteCalls++
	return nil
}

func (c *fakeRawRelayConn) LocalAddr() net.Addr {
	return &net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: 7000}
}

func (c *fakeRawRelayConn) RemoteAddr() net.Addr {
	return &net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: 43000}
}

func (c *fakeRawRelayConn) SetDeadline(time.Time) error {
	return nil
}

func (c *fakeRawRelayConn) SetReadDeadline(time.Time) error {
	return nil
}

func (c *fakeRawRelayConn) SetWriteDeadline(time.Time) error {
	return nil
}
