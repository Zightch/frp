package control

import (
	"errors"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/zightch/frp/frps/pkg/protocol"
)

const initialServerRequestID = uint32(1 << 31)

type sessionState struct {
	ID                     uint64
	Group                  GroupRuntime
	Snapshot               ConfigSnapshot
	LastAckedConfigVersion uint64
	pendingConfigRequestID uint32
	nextServerRequestID    atomic.Uint32
	nextStreamID           atomic.Uint32
	readTimeout            time.Duration
	writeMu                sync.Mutex

	runtimeMu        sync.Mutex
	streams          map[uint32]*publicStream
	udpSessions      map[uint32]*publicUDPSession
	udpSessionKeys   map[string]uint32
	listeners        map[uint32][]net.Listener
	udpListeners     map[uint32][]*net.UDPConn
	listenersStarted bool
	shutdownOnce     sync.Once
	done             chan struct{}
}

func (s *sessionState) nextRequestID() uint32 {
	requestID := s.nextServerRequestID.Add(1)
	if requestID == 0 {
		s.nextServerRequestID.Store(initialServerRequestID - 1)
		requestID = s.nextServerRequestID.Add(1)
	}
	return requestID
}

func (s *sessionState) doneCh() <-chan struct{} {
	return s.done
}

func (s *sessionState) closeDone() {
	if s.done == nil {
		return
	}
	s.shutdownOnce.Do(func() {
		close(s.done)
	})
}

func sessionReadTimeout(heartbeatInterval, minimum time.Duration) time.Duration {
	timeout := heartbeatInterval * 3
	if timeout < minimum {
		return minimum
	}
	return timeout
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

func (s *Server) writeFrameWithSession(conn net.Conn, session *sessionState, frame protocol.Frame) error {
	session.writeMu.Lock()
	defer session.writeMu.Unlock()
	return s.writeFrame(conn, frame)
}

func (s *Server) writeFramesWithSession(conn net.Conn, session *sessionState, frames ...protocol.Frame) error {
	session.writeMu.Lock()
	defer session.writeMu.Unlock()
	for _, frame := range frames {
		if err := s.writeFrame(conn, frame); err != nil {
			return err
		}
	}
	return nil
}

func (s *Server) replyProtocolErrorWithSession(conn net.Conn, session *sessionState, frame protocol.Frame, err error) error {
	protocolErr := protocol.AsProtocolError(err)
	if protocolErr == nil {
		return err
	}
	if writeErr := s.writeErrorWithSession(conn, session, frame.RequestID, frame.StreamID, protocolErr.Code, false, protocolErr.Message); writeErr != nil {
		return errors.Join(err, writeErr)
	}
	return err
}

func (s *Server) replyErrorWithSession(conn net.Conn, session *sessionState, requestID, streamID uint32, code uint16, format string, args ...any) error {
	err := protocol.NewError(code, format, args...)
	if writeErr := s.writeErrorWithSession(conn, session, requestID, streamID, code, false, err.Message); writeErr != nil {
		return errors.Join(err, writeErr)
	}
	return err
}

func (s *Server) writeErrorWithSession(conn net.Conn, session *sessionState, requestID, streamID uint32, code uint16, retryable bool, message string) error {
	body, err := protocol.MarshalErrorBody(protocol.ErrorBody{
		ErrorCode: code,
		Retryable: retryable,
		Message:   message,
	})
	if err != nil {
		return err
	}
	return s.writeFrameWithSession(conn, session, protocol.Frame{
		Type:      protocol.TypeError,
		RequestID: requestID,
		StreamID:  streamID,
		Body:      body,
	})
}
