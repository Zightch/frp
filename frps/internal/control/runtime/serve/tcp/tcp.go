package tcp

import (
	"fmt"
	"net"
	"strconv"
	"time"

	controlprotocolerrors "github.com/zightch/frp/frps/internal/control/protocol/errors"
	controlruntimestate "github.com/zightch/frp/frps/internal/control/runtime/state"
	"github.com/zightch/frp/frps/pkg/protocol"
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
}

type FrameWriter = controlprotocolerrors.FrameWriter

type Handler struct {
	Clock Clock
}

type StreamOpenOperation struct {
	Blocked   bool
	StreamID  uint32
	Stream    *Stream
	OpenFrame protocol.Frame
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
		return controlprotocolerrors.ReplyError(writer, frame.RequestID, frame.StreamID, protocol.ErrorCodeStreamNotFound, "stream %d not found", frame.StreamID)
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
