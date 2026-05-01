package tcp

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"time"

	controlprotocolerrors "github.com/zightch/frp/frps/internal/control/protocol/errors"
	controlruntimestate "github.com/zightch/frp/frps/internal/control/runtime/state"
	"github.com/zightch/frp/frps/pkg/protocol"
	"github.com/zightch/frp/frps/pkg/ratepolicy"
)

type Stream = controlruntimestate.Stream

type Clock interface {
	Now() time.Time
}

type Logger interface {
	Warn(msg string, args ...any)
}

type Session interface {
	NextTunnelStreamID() uint32
	NextRequestID() uint32
	AddPublicStream(streamID uint32, stream *Stream, configVersion uint64) bool
	PublicStream(streamID uint32) *Stream
	ClosePublicStream(streamID uint32) bool
	StreamRateLimit(streamID uint32) (context.Context, ratepolicy.TunnelLimiters, bool)
}

type RuntimeFrameWriter interface {
	WriteFrame(frame protocol.Frame) error
}

type FrameWriter = controlprotocolerrors.FrameWriter

type RuntimeWriteStoppedFunc func(error) bool

type Handler struct {
	Clock               Clock
	WriteTimeout        time.Duration
	RuntimeWriteStopped RuntimeWriteStoppedFunc
}

type ServeContext struct {
	Logger        Logger
	Session       Session
	RuntimeWriter RuntimeFrameWriter
	ControlWriter FrameWriter
	ConfigVersion uint64
	Tunnel        protocol.TunnelEntry
	RemotePort    uint16
}

type StreamOpenOperation struct {
	Blocked   bool
	StreamID  uint32
	Stream    *Stream
	OpenFrame protocol.Frame
}

func (h Handler) ServeTunnelListener(serve ServeContext, listener net.Listener) {
	for {
		publicConn, err := listener.Accept()
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return
			}
			if serve.Logger != nil {
				serve.Logger.Warn("tcp tunnel accept failed", "tunnel_id", serve.Tunnel.TunnelID, "error", err)
			}
			continue
		}

		go h.HandlePublicConnection(serve, publicConn)
	}
}

func (h Handler) HandlePublicConnection(serve ServeContext, publicConn net.Conn) {
	openOp, err := PreparePublicStreamOpen(serve.Session, serve.ConfigVersion, serve.Tunnel, serve.RemotePort, publicConn, h.now().UTC())
	if err != nil {
		_ = publicConn.Close()
		return
	}
	if openOp.Blocked {
		_ = publicConn.Close()
		return
	}

	if err := serve.RuntimeWriter.WriteFrame(openOp.OpenFrame); err != nil {
		serve.Session.ClosePublicStream(openOp.StreamID)
		return
	}

	select {
	case openErr := <-openOp.Stream.Ready:
		if openErr != nil {
			if serve.Logger != nil {
				serve.Logger.Warn("stream open rejected", "stream_id", openOp.StreamID, "tunnel_id", serve.Tunnel.TunnelID, "error", openErr)
			}
			serve.Session.ClosePublicStream(openOp.StreamID)
			return
		}
	case <-time.After(h.WriteTimeout):
		_ = SendStreamClose(serve.ControlWriter, openOp.StreamID, protocol.CloseReasonIdleTimeout, "stream open timeout")
		serve.Session.ClosePublicStream(openOp.StreamID)
		return
	}

	go h.CopyPublicToClient(serve.RuntimeWriter, serve.ControlWriter, serve.Session, openOp.StreamID, openOp.Stream)
}

func (h Handler) HandleStreamOpened(writer FrameWriter, session Session, frame protocol.Frame) error {
	if frame.RequestID == 0 {
		return controlprotocolerrors.ReplyError(writer, 0, frame.StreamID, protocol.ErrorCodeProtocolBadBody, "stream.opened requestId must be non-zero")
	}
	if frame.StreamID == 0 {
		return controlprotocolerrors.ReplyError(writer, frame.RequestID, 0, protocol.ErrorCodeProtocolBadBody, "stream.opened streamId must be non-zero")
	}

	opened, err := protocol.UnmarshalStreamOpened(frame.Body)
	if err != nil {
		return controlprotocolerrors.ReplyProtocolError(writer, frame, err)
	}

	stream := session.PublicStream(frame.StreamID)
	if stream == nil {
		return SendStreamClose(writer, frame.StreamID, protocol.CloseReasonProtocolError, "stream not found")
	}
	if frame.RequestID != stream.OpenRequestID {
		return controlprotocolerrors.ReplyError(writer, frame.RequestID, frame.StreamID, protocol.ErrorCodeProtocolBadBody, "unexpected stream.opened requestId %d", frame.RequestID)
	}

	switch opened.Status {
	case protocol.StatusOK:
		stream.SignalReady(nil)
	case protocol.StatusError:
		stream.SignalReady(fmt.Errorf("%s", opened.Message))
	default:
		return controlprotocolerrors.ReplyError(writer, frame.RequestID, frame.StreamID, protocol.ErrorCodeProtocolBadBody, "unsupported stream.opened status %d", opened.Status)
	}

	return nil
}

func (h Handler) HandleStreamData(writer FrameWriter, session Session, frame protocol.Frame) error {
	if frame.RequestID != 0 {
		return controlprotocolerrors.ReplyError(writer, frame.RequestID, frame.StreamID, protocol.ErrorCodeProtocolBadBody, "stream.data requestId must be zero")
	}
	if frame.StreamID == 0 {
		return controlprotocolerrors.ReplyError(writer, 0, frame.StreamID, protocol.ErrorCodeProtocolBadBody, "stream.data streamId must be non-zero")
	}

	stream := session.PublicStream(frame.StreamID)
	if stream == nil {
		return SendStreamClose(writer, frame.StreamID, protocol.CloseReasonProtocolError, "stream not found")
	}

	limitCtx, limiters, ok := session.StreamRateLimit(frame.StreamID)
	if !ok || limitCtx == nil {
		limitCtx = context.Background()
	}

	if err := ratepolicy.WritePayload(limitCtx, limiters.Uplink, protocol.MaxDataBodyLen, frame.Body, func(chunk []byte) error {
		return WriteConnFull(stream.Conn, chunk)
	}); err != nil {
		if errors.Is(err, context.Canceled) {
			return nil
		}
		if session.ClosePublicStream(frame.StreamID) {
			return SendStreamClose(writer, frame.StreamID, protocol.CloseReasonWriteError, err.Error())
		}
		return nil
	}
	stream.Touch(h.now())
	return nil
}

func (h Handler) HandleStreamClose(session Session, frame protocol.Frame) error {
	if frame.RequestID != 0 {
		return fmt.Errorf("stream.close requestId must be zero")
	}
	if frame.StreamID == 0 {
		return fmt.Errorf("stream.close streamId must be non-zero")
	}
	if _, err := protocol.UnmarshalStreamClose(frame.Body); err != nil {
		return err
	}
	session.ClosePublicStream(frame.StreamID)
	return nil
}

func (h Handler) CopyPublicToClient(runtimeWriter RuntimeFrameWriter, controlWriter FrameWriter, session Session, streamID uint32, stream *Stream) {
	buffer := make([]byte, protocol.MaxDataBodyLen)
	limitCtx, limiters, ok := session.StreamRateLimit(streamID)
	if !ok || limitCtx == nil {
		limitCtx = context.Background()
	}
	for {
		n, err := stream.Conn.Read(buffer)
		if n > 0 {
			payload := append([]byte(nil), buffer[:n]...)
			writeErr := ratepolicy.WritePayload(limitCtx, limiters.Downlink, protocol.MaxDataBodyLen, payload, func(chunk []byte) error {
				err := runtimeWriter.WriteFrame(protocol.Frame{
					Type:     protocol.TypeStreamData,
					StreamID: streamID,
					Body:     chunk,
				})
				if err == nil {
					stream.Touch(h.now())
				}
				return err
			})
			if writeErr != nil {
				if errors.Is(writeErr, context.Canceled) {
					return
				}
				if h.RuntimeWriteStopped == nil || !h.RuntimeWriteStopped(writeErr) {
					session.ClosePublicStream(streamID)
				}
				return
			}
		}

		if err == nil {
			continue
		}

		reasonCode := protocol.CloseReasonReadError
		message := err.Error()
		if errors.Is(err, io.EOF) {
			reasonCode = protocol.CloseReasonEOF
			message = "eof"
		}
		if session.ClosePublicStream(streamID) {
			_ = SendStreamClose(controlWriter, streamID, reasonCode, message)
		}
		return
	}
}

func PreparePublicStreamOpen(session Session, configVersion uint64, tunnel protocol.TunnelEntry, remotePort uint16, publicConn net.Conn, now time.Time) (StreamOpenOperation, error) {
	streamID := session.NextTunnelStreamID()
	requestID := session.NextRequestID()
	stream := NewPublicStream(configVersion, tunnel, remotePort, requestID, publicConn, now)
	if !session.AddPublicStream(streamID, stream, configVersion) {
		return StreamOpenOperation{Blocked: true}, nil
	}

	body, err := protocol.MarshalStreamOpen(protocol.StreamOpen{
		TunnelID:   tunnel.TunnelID,
		RemotePort: remotePort,
		ClientAddr: stream.ClientAddr,
		OpenedAtMs: stream.OpenedAtMs,
	})
	if err != nil {
		session.ClosePublicStream(streamID)
		return StreamOpenOperation{}, err
	}

	return StreamOpenOperation{
		StreamID: streamID,
		Stream:   stream,
		OpenFrame: protocol.Frame{
			Type:      protocol.TypeStreamOpen,
			RequestID: requestID,
			StreamID:  streamID,
			Body:      body,
		},
	}, nil
}

func SendStreamClose(writer FrameWriter, streamID uint32, reasonCode uint16, message string) error {
	body, err := protocol.MarshalStreamClose(protocol.StreamClose{
		ReasonCode: reasonCode,
		Initiator:  protocol.InitiatorFRPS,
		Message:    message,
	})
	if err != nil {
		return err
	}
	return writer.WriteFrame(protocol.Frame{
		Type:     protocol.TypeStreamClose,
		StreamID: streamID,
		Body:     body,
	})
}

func NewPublicStream(configVersion uint64, tunnel protocol.TunnelEntry, remotePort uint16, openRequestID uint32, publicConn net.Conn, now time.Time) *Stream {
	stream := &Stream{
		ConfigVersion: configVersion,
		Conn:          publicConn,
		Tunnel:        tunnel,
		RemotePort:    remotePort,
		ClientAddr:    SockAddrFromNetAddr(publicConn.RemoteAddr()),
		OpenedAtMs:    uint64(now.UTC().UnixMilli()),
		OpenRequestID: openRequestID,
		Ready:         make(chan error, 1),
	}
	stream.Touch(now)
	return stream
}

func WriteConnFull(conn net.Conn, payload []byte) error {
	for len(payload) > 0 {
		n, err := conn.Write(payload)
		if err != nil {
			return err
		}
		payload = payload[n:]
	}
	return nil
}

func SockAddrFromNetAddr(addr net.Addr) protocol.SockAddr {
	if addr == nil {
		return protocol.SockAddr{}
	}
	switch typed := addr.(type) {
	case *net.TCPAddr:
		return protocol.SockAddr{IP: append(net.IP(nil), typed.IP...), Port: uint16(typed.Port)}
	case *net.UDPAddr:
		return protocol.SockAddr{IP: append(net.IP(nil), typed.IP...), Port: uint16(typed.Port)}
	default:
		host, portText, err := net.SplitHostPort(addr.String())
		if err != nil {
			return protocol.SockAddr{}
		}
		port, err := strconv.Atoi(portText)
		if err != nil {
			return protocol.SockAddr{}
		}
		return protocol.SockAddr{IP: net.ParseIP(host), Port: uint16(port)}
	}
}

func SockAddrString(addr protocol.SockAddr) string {
	if len(addr.IP) == 0 && addr.Port == 0 {
		return ""
	}
	ip := addr.IP
	if ip4 := ip.To4(); ip4 != nil {
		ip = ip4
	} else if ip16 := ip.To16(); ip16 != nil {
		ip = ip16
	}
	return net.JoinHostPort(ip.String(), strconv.Itoa(int(addr.Port)))
}

func (h Handler) now() time.Time {
	if h.Clock == nil {
		return time.Now().UTC()
	}
	return h.Clock.Now()
}
