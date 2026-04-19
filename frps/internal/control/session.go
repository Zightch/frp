package control

import (
	"net"
	"sync"
	"sync/atomic"
	"time"
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
