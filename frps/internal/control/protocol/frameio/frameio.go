package frameio

import (
	"net"
	"time"

	"github.com/zightch/frp/frps/pkg/protocol"
	"github.com/zightch/frp/frps/pkg/transport"
)

type Context = transport.FrameContext

type SessionContext struct {
	GroupID   int64
	SessionID uint64
}

type Writer struct {
	Frames       transport.FrameIO
	Conn         net.Conn
	WriteTimeout time.Duration
	Context      Context
}

func ServerContext(conn net.Conn, session SessionContext) Context {
	return transport.FrameContext{
		Side:      transport.FrameSideServer,
		ConnID:    transport.ConnectionID(conn),
		GroupID:   session.GroupID,
		SessionID: session.SessionID,
	}
}

func ReadFrame(frames transport.FrameIO, conn net.Conn, timeout time.Duration, context Context) (protocol.Frame, error) {
	frameBytes, err := frames.ReadFrame(conn, timeout, context)
	if err != nil {
		return protocol.Frame{}, err
	}
	return protocol.ParseFrame(frameBytes)
}

func WriteFrame(frames transport.FrameIO, conn net.Conn, frame protocol.Frame, timeout time.Duration, context Context) error {
	frameBytes, err := frame.MarshalBinary()
	if err != nil {
		return err
	}
	return frames.WriteFrame(conn, frameBytes, timeout, context)
}

func WriteFrames(frames transport.FrameIO, conn net.Conn, timeout time.Duration, context Context, values ...protocol.Frame) error {
	for _, frame := range values {
		if err := WriteFrame(frames, conn, frame, timeout, context); err != nil {
			return err
		}
	}
	return nil
}

func (w Writer) WriteFrame(frame protocol.Frame) error {
	return WriteFrame(w.Frames, w.Conn, frame, w.WriteTimeout, w.Context)
}

func (w Writer) WriteFrames(frames ...protocol.Frame) error {
	return WriteFrames(w.Frames, w.Conn, w.WriteTimeout, w.Context, frames...)
}
