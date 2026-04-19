package control

import (
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"sync"

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

type tcpTunnelListener struct {
	tunnel     protocol.TunnelEntry
	remotePort uint16
	listener   net.Listener
}

type udpTunnelListener struct {
	tunnel     protocol.TunnelEntry
	remotePort uint16
	listener   *net.UDPConn
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
	session.closeDone()

	session.runtimeMu.Lock()
	listeners := make([]net.Listener, 0, len(session.listeners))
	for tunnelID, tunnelListeners := range session.listeners {
		delete(session.listeners, tunnelID)
		listeners = append(listeners, tunnelListeners...)
	}
	udpListeners := make([]*net.UDPConn, 0, len(session.udpListeners))
	for tunnelID, tunnelListeners := range session.udpListeners {
		delete(session.udpListeners, tunnelID)
		udpListeners = append(udpListeners, tunnelListeners...)
	}

	streams := make([]*publicStream, 0, len(session.streams))
	for streamID, stream := range session.streams {
		delete(session.streams, streamID)
		streams = append(streams, stream)
	}
	for sessionID := range session.udpSessions {
		delete(session.udpSessions, sessionID)
	}
	for key := range session.udpSessionKeys {
		delete(session.udpSessionKeys, key)
	}
	session.listenersStarted = false
	session.runtimeMu.Unlock()

	for _, listener := range listeners {
		_ = listener.Close()
	}
	for _, listener := range udpListeners {
		_ = listener.Close()
	}
	for _, stream := range streams {
		stream.signalReady(net.ErrClosed)
		stream.close()
	}
	s.releaseGroupSlot(session.Group.ID, session.ID)
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
