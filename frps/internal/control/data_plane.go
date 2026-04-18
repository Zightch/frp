package control

import (
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"sync"
	"time"

	"github.com/zightch/frp/frps/pkg/protocol"
)

type publicStream struct {
	conn          net.Conn
	tunnel        protocol.TunnelEntry
	openRequestID uint32
	ready         chan error
	readyOnce     sync.Once
	closeOnce     sync.Once
}

func (s *Server) ensureTunnelListeners(conn net.Conn, logger Logger, session *sessionState) error {
	session.runtimeMu.Lock()
	if session.listenersStarted {
		session.runtimeMu.Unlock()
		return nil
	}
	session.runtimeMu.Unlock()

	started := make([]net.Listener, 0)
	startedByTunnel := make(map[uint32]net.Listener)

	for _, tunnel := range session.Snapshot.Tunnels {
		if tunnel.Protocol != protocol.ProtocolTCP {
			continue
		}
		if tunnel.TunnelFlags&protocol.TunnelFlagEnabled == 0 {
			continue
		}
		if tunnel.TunnelFlags&protocol.TunnelFlagRange != 0 {
			continue
		}

		addr := net.JoinHostPort("", strconv.Itoa(int(tunnel.RemoteStart)))
		listener, err := net.Listen("tcp", addr)
		if err != nil {
			for _, startedListener := range started {
				_ = startedListener.Close()
			}
			return err
		}
		started = append(started, listener)
		startedByTunnel[tunnel.TunnelID] = listener
	}

	session.runtimeMu.Lock()
	if session.listenersStarted {
		session.runtimeMu.Unlock()
		for _, startedListener := range started {
			_ = startedListener.Close()
		}
		return nil
	}
	for tunnelID, listener := range startedByTunnel {
		session.listeners[tunnelID] = listener
	}
	session.listenersStarted = true
	session.runtimeMu.Unlock()

	for _, tunnel := range session.Snapshot.Tunnels {
		listener, ok := startedByTunnel[tunnel.TunnelID]
		if !ok {
			continue
		}
		logger.Info("tcp tunnel listener ready", "tunnel_id", tunnel.TunnelID, "addr", listener.Addr().String())
		go s.serveTunnelListener(conn, logger, session, tunnel, listener)
	}

	return nil
}

func (s *Server) serveTunnelListener(conn net.Conn, logger Logger, session *sessionState, tunnel protocol.TunnelEntry, listener net.Listener) {
	for {
		publicConn, err := listener.Accept()
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return
			}
			logger.Warn("tcp tunnel accept failed", "tunnel_id", tunnel.TunnelID, "error", err)
			continue
		}

		go s.handlePublicConnection(conn, logger, session, tunnel, publicConn)
	}
}

func (s *Server) handlePublicConnection(controlConn net.Conn, logger Logger, session *sessionState, tunnel protocol.TunnelEntry, publicConn net.Conn) {
	streamID := session.nextTunnelStreamID()
	requestID := session.nextRequestID()
	stream := &publicStream{
		conn:          publicConn,
		tunnel:        tunnel,
		openRequestID: requestID,
		ready:         make(chan error, 1),
	}

	session.addPublicStream(streamID, stream)

	body, err := protocol.MarshalStreamOpen(protocol.StreamOpen{
		TunnelID:   tunnel.TunnelID,
		RemotePort: tunnel.RemoteStart,
		ClientAddr: sockAddrFromNetAddr(publicConn.RemoteAddr()),
		OpenedAtMs: uint64(time.Now().UTC().UnixMilli()),
	})
	if err != nil {
		session.closePublicStream(streamID)
		return
	}

	if err := s.writeFrameWithSession(controlConn, session, protocol.Frame{
		Type:      protocol.TypeStreamOpen,
		RequestID: requestID,
		StreamID:  streamID,
		Body:      body,
	}); err != nil {
		session.closePublicStream(streamID)
		return
	}

	select {
	case openErr := <-stream.ready:
		if openErr != nil {
			logger.Warn("stream open rejected", "stream_id", streamID, "tunnel_id", tunnel.TunnelID, "error", openErr)
			session.closePublicStream(streamID)
			return
		}
	case <-time.After(s.options.WriteTimeout):
		_ = s.sendStreamClose(controlConn, session, streamID, protocol.CloseReasonIdleTimeout, "stream open timeout")
		session.closePublicStream(streamID)
		return
	}

	go s.copyPublicToClient(controlConn, session, streamID, stream)
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
	}
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

func (s *Server) copyPublicToClient(conn net.Conn, session *sessionState, streamID uint32, stream *publicStream) {
	buffer := make([]byte, protocol.MaxDataBodyLen)
	for {
		n, err := stream.conn.Read(buffer)
		if n > 0 {
			payload := append([]byte(nil), buffer[:n]...)
			writeErr := s.writeFrameWithSession(conn, session, protocol.Frame{
				Type:     protocol.TypeStreamData,
				StreamID: streamID,
				Body:     payload,
			})
			if writeErr != nil {
				session.closePublicStream(streamID)
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
		if session.closePublicStream(streamID) {
			_ = s.sendStreamClose(conn, session, streamID, reasonCode, message)
		}
		return
	}
}

func (s *Server) shutdownSession(session *sessionState) {
	session.runtimeMu.Lock()
	listeners := make([]net.Listener, 0, len(session.listeners))
	for tunnelID, listener := range session.listeners {
		delete(session.listeners, tunnelID)
		listeners = append(listeners, listener)
	}

	streams := make([]*publicStream, 0, len(session.streams))
	for streamID, stream := range session.streams {
		delete(session.streams, streamID)
		streams = append(streams, stream)
	}
	session.listenersStarted = false
	session.runtimeMu.Unlock()

	for _, listener := range listeners {
		_ = listener.Close()
	}
	for _, stream := range streams {
		stream.signalReady(net.ErrClosed)
		stream.close()
	}
}

func sockAddrFromNetAddr(addr net.Addr) protocol.SockAddr {
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

func (s *sessionState) nextTunnelStreamID() uint32 {
	streamID := s.nextStreamID.Add(1)
	if streamID == 0 {
		streamID = s.nextStreamID.Add(1)
	}
	return streamID
}

func (s *sessionState) addPublicStream(streamID uint32, stream *publicStream) {
	s.runtimeMu.Lock()
	defer s.runtimeMu.Unlock()
	s.streams[streamID] = stream
}

func (s *sessionState) publicStream(streamID uint32) *publicStream {
	s.runtimeMu.Lock()
	defer s.runtimeMu.Unlock()
	return s.streams[streamID]
}

func (s *sessionState) closePublicStream(streamID uint32) bool {
	s.runtimeMu.Lock()
	stream, ok := s.streams[streamID]
	if ok {
		delete(s.streams, streamID)
	}
	s.runtimeMu.Unlock()

	if !ok {
		return false
	}

	stream.signalReady(net.ErrClosed)
	stream.close()
	return true
}

func (s *publicStream) signalReady(err error) {
	s.readyOnce.Do(func() {
		s.ready <- err
	})
}

func (s *publicStream) close() {
	s.closeOnce.Do(func() {
		_ = s.conn.Close()
	})
}

type Logger interface {
	Info(msg string, args ...any)
	Warn(msg string, args ...any)
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
