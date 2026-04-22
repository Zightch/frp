package transport

import (
	"fmt"
	"net"
	"time"
)

type FrameSide string

const (
	FrameSideClient FrameSide = "client"
	FrameSideServer FrameSide = "server"
)

type FrameContext struct {
	Side      FrameSide
	ConnID    string
	GroupID   int64
	SessionID uint64
}

type FrameIO interface {
	ReadFrame(conn net.Conn, timeout time.Duration, context FrameContext) ([]byte, error)
	WriteFrame(conn net.Conn, frame []byte, timeout time.Duration, context FrameContext) error
}

type RealFrameIO struct{}

func (RealFrameIO) ReadFrame(conn net.Conn, timeout time.Duration, _ FrameContext) ([]byte, error) {
	return ReadFrame(conn, timeout)
}

func (RealFrameIO) WriteFrame(conn net.Conn, frame []byte, timeout time.Duration, _ FrameContext) error {
	return WriteFrame(conn, frame, timeout)
}

type connIDProvider interface {
	TransportConnID() string
}

func ConnectionID(conn net.Conn) string {
	if conn == nil {
		return ""
	}
	if provider, ok := conn.(connIDProvider); ok {
		return provider.TransportConnID()
	}
	return fmt.Sprintf("%T@%p", conn, conn)
}
