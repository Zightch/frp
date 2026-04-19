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
