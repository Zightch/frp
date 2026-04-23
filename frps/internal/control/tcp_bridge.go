package control

import (
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/zightch/frp/frps/pkg/protocol"
)

type publicStream struct {
	configVersion    uint64
	conn             net.Conn
	tunnel           protocol.TunnelEntry
	remotePort       uint16
	clientAddr       protocol.SockAddr
	openedAtMs       uint64
	openRequestID    uint32
	lastActiveUnixMs atomic.Int64
	ready            chan error
	readyOnce        sync.Once
	closeOnce        sync.Once
}

func (s *Server) handlePublicConnection(serve tunnelRuntimeServeContext, publicConn net.Conn) {
	openOp, err := serve.session.preparePublicStreamOpen(serve.runtimeIO.configVersion, serve.tunnel, serve.remotePort, publicConn, s.clock.Now().UTC())
	if err != nil {
		_ = publicConn.Close()
		return
	}
	if openOp.blocked {
		_ = publicConn.Close()
		return
	}

	if err := serve.runtimeIO.writeFrame(openOp.openFrame); err != nil {
		serve.session.closePublicStream(openOp.streamID)
		return
	}

	select {
	case openErr := <-openOp.stream.ready:
		if openErr != nil {
			serve.logger.Warn("stream open rejected", "stream_id", openOp.streamID, "tunnel_id", serve.tunnel.TunnelID, "error", openErr)
			serve.session.closePublicStream(openOp.streamID)
			return
		}
	case <-time.After(s.options.WriteTimeout):
		_ = s.sendStreamClose(serve.runtimeIO.conn, serve.session, openOp.streamID, protocol.CloseReasonIdleTimeout, "stream open timeout")
		serve.session.closePublicStream(openOp.streamID)
		return
	}

	go s.copyPublicToClient(serve.runtimeIO, openOp.streamID, openOp.stream)
}

func (s *Server) handleStreamOpened(conn net.Conn, session *sessionState, frame protocol.Frame) error {
	if frame.RequestID == 0 {
		return s.replyErrorWithSession(conn, session, 0, frame.StreamID, protocol.ErrorCodeProtocolBadBody, "stream.opened requestId must be non-zero")
	}
	if frame.StreamID == 0 {
		return s.replyErrorWithSession(conn, session, frame.RequestID, 0, protocol.ErrorCodeProtocolBadBody, "stream.opened streamId must be non-zero")
	}

	opened, err := protocol.UnmarshalStreamOpened(frame.Body)
	if err != nil {
		return s.replyProtocolErrorWithSession(conn, session, frame, err)
	}

	stream := session.publicStream(frame.StreamID)
	if stream == nil {
		return s.sendStreamClose(conn, session, frame.StreamID, protocol.CloseReasonProtocolError, "stream not found")
	}
	if frame.RequestID != stream.openRequestID {
		return s.replyErrorWithSession(conn, session, frame.RequestID, frame.StreamID, protocol.ErrorCodeProtocolBadBody, "unexpected stream.opened requestId %d", frame.RequestID)
	}

	switch opened.Status {
	case protocol.StatusOK:
		stream.signalReady(nil)
	case protocol.StatusError:
		stream.signalReady(fmt.Errorf("%s", opened.Message))
	default:
		return s.replyErrorWithSession(conn, session, frame.RequestID, frame.StreamID, protocol.ErrorCodeProtocolBadBody, "unsupported stream.opened status %d", opened.Status)
	}

	return nil
}

func (s *Server) handleStreamData(conn net.Conn, session *sessionState, frame protocol.Frame) error {
	if frame.RequestID != 0 {
		return s.replyErrorWithSession(conn, session, frame.RequestID, frame.StreamID, protocol.ErrorCodeProtocolBadBody, "stream.data requestId must be zero")
	}
	if frame.StreamID == 0 {
		return s.replyErrorWithSession(conn, session, 0, frame.StreamID, protocol.ErrorCodeProtocolBadBody, "stream.data streamId must be non-zero")
	}

	stream := session.publicStream(frame.StreamID)
	if stream == nil {
		return s.sendStreamClose(conn, session, frame.StreamID, protocol.CloseReasonProtocolError, "stream not found")
	}

	if err := writeConnFull(stream.conn, frame.Body); err != nil {
		if session.closePublicStream(frame.StreamID) {
			return s.sendStreamClose(conn, session, frame.StreamID, protocol.CloseReasonWriteError, err.Error())
		}
		return nil
	}
	stream.touch(s.clock.Now())
	return nil
}

func (s *Server) handleStreamClose(session *sessionState, frame protocol.Frame) error {
	if frame.RequestID != 0 {
		return fmt.Errorf("stream.close requestId must be zero")
	}
	if frame.StreamID == 0 {
		return fmt.Errorf("stream.close streamId must be non-zero")
	}
	if _, err := protocol.UnmarshalStreamClose(frame.Body); err != nil {
		return err
	}
	session.closePublicStream(frame.StreamID)
	return nil
}

func (s *Server) sendStreamClose(conn net.Conn, session *sessionState, streamID uint32, reasonCode uint16, message string) error {
	body, err := protocol.MarshalStreamClose(protocol.StreamClose{
		ReasonCode: reasonCode,
		Initiator:  protocol.InitiatorFRPS,
		Message:    message,
	})
	if err != nil {
		return err
	}
	return s.writeFrameWithSession(conn, session, protocol.Frame{
		Type:     protocol.TypeStreamClose,
		StreamID: streamID,
		Body:     body,
	})
}

func (s *Server) copyPublicToClient(runtimeIO sessionRuntimeIOWriter, streamID uint32, stream *publicStream) {
	buffer := make([]byte, protocol.MaxDataBodyLen)
	for {
		n, err := stream.conn.Read(buffer)
		if n > 0 {
			payload := append([]byte(nil), buffer[:n]...)
			writeErr := runtimeIO.writeFrame(protocol.Frame{
				Type:     protocol.TypeStreamData,
				StreamID: streamID,
				Body:     payload,
			})
			if writeErr != nil {
				if !errors.Is(writeErr, errRuntimeIOStopped) {
					runtimeIO.session.closePublicStream(streamID)
				}
				return
			}
			stream.touch(s.clock.Now())
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
		if runtimeIO.session.closePublicStream(streamID) {
			_ = s.sendStreamClose(runtimeIO.conn, runtimeIO.session, streamID, reasonCode, message)
		}
		return
	}
}

func newPublicStream(configVersion uint64, tunnel protocol.TunnelEntry, remotePort uint16, openRequestID uint32, publicConn net.Conn, now time.Time) *publicStream {
	stream := &publicStream{
		configVersion: configVersion,
		conn:          publicConn,
		tunnel:        tunnel,
		remotePort:    remotePort,
		clientAddr:    sockAddrFromNetAddr(publicConn.RemoteAddr()),
		openedAtMs:    uint64(now.UTC().UnixMilli()),
		openRequestID: openRequestID,
		ready:         make(chan error, 1),
	}
	stream.touch(now)
	return stream
}

func writeConnFull(conn net.Conn, payload []byte) error {
	for len(payload) > 0 {
		n, err := conn.Write(payload)
		if err != nil {
			return err
		}
		payload = payload[n:]
	}
	return nil
}

func (s *publicStream) signalReady(err error) {
	s.readyOnce.Do(func() {
		s.ready <- err
	})
}

func (s *publicStream) touch(now time.Time) {
	s.lastActiveUnixMs.Store(now.UTC().UnixMilli())
}

func (s *publicStream) observedConnection(streamID uint32) observedSessionRuntimeConnection {
	return observedSessionRuntimeConnection{
		connectionID:   streamID,
		kind:           observedRuntimeConnectionKindTCPStream,
		protocol:       "tcp",
		tunnelID:       s.tunnel.TunnelID,
		remotePort:     s.remotePort,
		clientAddr:     sockAddrString(s.clientAddr),
		openedAtMs:     s.openedAtMs,
		lastActiveAtMs: nonNegativeUnixMilli(s.lastActiveUnixMs.Load()),
	}
}

func (s *publicStream) close() {
	s.closeOnce.Do(func() {
		_ = s.conn.Close()
	})
}
