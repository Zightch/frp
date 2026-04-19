package control

import (
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
