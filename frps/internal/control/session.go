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

var errRuntimeIOStopped = errors.New("runtime io no longer allowed")

type sessionState struct {
	ID                     uint64
	Group                  GroupRuntime
	Snapshot               ConfigSnapshot
	LastAckedConfigVersion uint64
	pendingConfigRequestID uint32
	pendingGroup           GroupRuntime
	pendingSnapshot        ConfigSnapshot
	nextServerRequestID    atomic.Uint32
	nextStreamID           atomic.Uint32
	readTimeout            time.Duration
	configMu               sync.Mutex
	writeMu                sync.Mutex

	runtimeMu         sync.Mutex
	runtimeIOMu       sync.RWMutex
	streams           map[uint32]*publicStream
	udpSessions       map[uint32]*publicUDPSession
	udpSessionKeys    map[string]uint32
	listeners         map[uint32][]net.Listener
	udpListeners      map[uint32][]UDPListener
	listenersStarted  bool
	udpCleanupStarted bool
	runtimeFrozen     bool
	runtimeGeneration uint64
	shutdownOnce      sync.Once
	done              chan struct{}
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

func (s *sessionState) hasRuntimeListenersLocked() bool {
	for _, listeners := range s.listeners {
		if len(listeners) > 0 {
			return true
		}
	}
	for _, listeners := range s.udpListeners {
		if len(listeners) > 0 {
			return true
		}
	}
	return false
}

func (s *sessionState) activeRuntimeTunnelIDs() map[uint32]struct{} {
	s.runtimeMu.Lock()
	defer s.runtimeMu.Unlock()
	if s.runtimeFrozen {
		return nil
	}
	return s.activeRuntimeTunnelIDsLocked()
}

func (s *sessionState) activeRuntimeTunnelIDsLocked() map[uint32]struct{} {
	active := make(map[uint32]struct{})
	for tunnelID, listeners := range s.listeners {
		if len(listeners) == 0 {
			continue
		}
		active[tunnelID] = struct{}{}
	}
	for tunnelID, listeners := range s.udpListeners {
		if len(listeners) == 0 {
			continue
		}
		active[tunnelID] = struct{}{}
	}
	if len(active) == 0 {
		return nil
	}
	return active
}

func (s *sessionState) hasActiveRuntimeListeners() bool {
	s.runtimeMu.Lock()
	defer s.runtimeMu.Unlock()
	if s.runtimeFrozen {
		return false
	}
	return s.hasRuntimeListenersLocked()
}

func (s *sessionState) attachTunnelListeners(configVersion uint64, tunnelID uint32, tcpListeners []net.Listener, udpListeners []UDPListener) (bool, bool) {
	s.runtimeMu.Lock()
	defer s.runtimeMu.Unlock()
	if s.runtimeFrozen {
		return false, false
	}
	if s.runtimeGeneration != 0 && s.runtimeGeneration != configVersion {
		return false, false
	}
	if len(s.listeners[tunnelID]) > 0 || len(s.udpListeners[tunnelID]) > 0 {
		return false, false
	}

	if len(tcpListeners) > 0 {
		s.listeners[tunnelID] = append(s.listeners[tunnelID], tcpListeners...)
	}
	if len(udpListeners) > 0 {
		s.udpListeners[tunnelID] = append(s.udpListeners[tunnelID], udpListeners...)
	}

	if s.hasRuntimeListenersLocked() {
		s.listenersStarted = true
		s.runtimeGeneration = configVersion
	} else {
		s.listenersStarted = false
		s.runtimeGeneration = 0
	}

	startUDPCleanup := len(udpListeners) > 0 && !s.udpCleanupStarted
	if startUDPCleanup {
		s.udpCleanupStarted = true
	}
	return startUDPCleanup, true
}

func (s *sessionState) addPublicStream(streamID uint32, stream *publicStream, configVersion uint64) bool {
	s.runtimeMu.Lock()
	defer s.runtimeMu.Unlock()
	if s.runtimeFrozen || !s.listenersStarted || s.runtimeGeneration != configVersion {
		return false
	}
	s.streams[streamID] = stream
	return true
}

func (s *sessionState) canServeRuntimeIO(configVersion uint64) bool {
	s.runtimeMu.Lock()
	defer s.runtimeMu.Unlock()
	return !s.runtimeFrozen && s.listenersStarted && s.runtimeGeneration == configVersion
}

func (s *sessionState) lockRuntimeIOWrite(configVersion uint64) bool {
	s.writeMu.Lock()
	s.runtimeIOMu.RLock()
	if !s.canServeRuntimeIO(configVersion) {
		s.runtimeIOMu.RUnlock()
		s.writeMu.Unlock()
		return false
	}
	return true
}

func (s *sessionState) unlockRuntimeIOWrite() {
	s.runtimeIOMu.RUnlock()
	s.writeMu.Unlock()
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

func (s *sessionState) currentGroupAndSnapshot() (GroupRuntime, ConfigSnapshot) {
	s.configMu.Lock()
	defer s.configMu.Unlock()
	return s.Group, s.Snapshot
}

func (s *sessionState) currentGroup() GroupRuntime {
	s.configMu.Lock()
	defer s.configMu.Unlock()
	return s.Group
}

func (s *sessionState) replaceGroupRuntime(group GroupRuntime) {
	s.configMu.Lock()
	defer s.configMu.Unlock()
	group.Snapshot = s.Snapshot
	s.Group = group
}

func (s *sessionState) reconfigure(group GroupRuntime, snapshot ConfigSnapshot, requestID uint32) error {
	s.configMu.Lock()
	defer s.configMu.Unlock()

	if s.pendingConfigRequestID != 0 {
		return errConfigUpdateInFlight
	}

	s.pendingGroup = group
	s.pendingSnapshot = snapshot
	s.pendingConfigRequestID = requestID
	return nil
}

func (s *sessionState) configAckState() (uint32, uint64) {
	s.configMu.Lock()
	defer s.configMu.Unlock()
	if s.pendingConfigRequestID == 0 {
		return 0, s.Snapshot.Version
	}
	return s.pendingConfigRequestID, s.pendingSnapshot.Version
}

func (s *sessionState) lastAckedConfigVersion() uint64 {
	s.configMu.Lock()
	defer s.configMu.Unlock()
	return s.LastAckedConfigVersion
}

func (s *sessionState) acceptConfigAck(requestID uint32, version uint64) error {
	s.configMu.Lock()
	defer s.configMu.Unlock()

	if s.pendingConfigRequestID == 0 || requestID != s.pendingConfigRequestID {
		return errUnexpectedConfigAck
	}
	if version != s.pendingSnapshot.Version {
		return errConfigVersionMismatch
	}

	s.Group = s.pendingGroup
	s.Snapshot = s.pendingSnapshot
	s.LastAckedConfigVersion = version
	s.pendingConfigRequestID = 0
	s.pendingGroup = GroupRuntime{}
	s.pendingSnapshot = ConfigSnapshot{}
	return nil
}

func (s *sessionState) clearPendingConfigRequest(requestID uint32) {
	s.configMu.Lock()
	defer s.configMu.Unlock()
	if s.pendingConfigRequestID == requestID {
		s.pendingConfigRequestID = 0
		s.pendingGroup = GroupRuntime{}
		s.pendingSnapshot = ConfigSnapshot{}
	}
}

func (s *sessionState) hasPendingConfig() bool {
	s.configMu.Lock()
	defer s.configMu.Unlock()
	return s.pendingConfigRequestID != 0
}

func (s *sessionState) freezeTunnelRuntime() ([]net.Listener, []UDPListener, map[uint32]*publicStream, []*publicUDPSession) {
	s.runtimeIOMu.Lock()
	defer s.runtimeIOMu.Unlock()
	s.runtimeMu.Lock()
	defer s.runtimeMu.Unlock()

	s.runtimeFrozen = true
	s.listenersStarted = false
	s.runtimeGeneration = 0

	listeners := make([]net.Listener, 0, len(s.listeners))
	for tunnelID, tunnelListeners := range s.listeners {
		delete(s.listeners, tunnelID)
		listeners = append(listeners, tunnelListeners...)
	}
	udpListeners := make([]UDPListener, 0, len(s.udpListeners))
	for tunnelID, tunnelListeners := range s.udpListeners {
		delete(s.udpListeners, tunnelID)
		udpListeners = append(udpListeners, tunnelListeners...)
	}
	streams := make(map[uint32]*publicStream, len(s.streams))
	for streamID, stream := range s.streams {
		delete(s.streams, streamID)
		streams[streamID] = stream
	}
	udpSessions := make([]*publicUDPSession, 0, len(s.udpSessions))
	for sessionID, udpSession := range s.udpSessions {
		delete(s.udpSessions, sessionID)
		udpSessions = append(udpSessions, udpSession)
	}
	for key := range s.udpSessionKeys {
		delete(s.udpSessionKeys, key)
	}

	return listeners, udpListeners, streams, udpSessions
}

func (s *sessionState) allowTunnelRuntimeStart() {
	s.runtimeMu.Lock()
	defer s.runtimeMu.Unlock()
	s.runtimeFrozen = false
}

func (s *sessionState) resetTunnelRuntime() {
	listeners, udpListeners, _, _ := s.freezeTunnelRuntime()
	closeStartedTunnelListeners(listeners, udpListeners)
	s.allowTunnelRuntimeStart()
}

func (s *Server) shutdownSession(session *sessionState) {
	session.closeDone()
	listeners, udpListeners, streams, _ := session.freezeTunnelRuntime()
	closeStartedTunnelListeners(listeners, udpListeners)

	for _, stream := range streams {
		stream.signalReady(net.ErrClosed)
		stream.close()
	}
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

func (s *Server) writeRuntimeFrameWithSession(conn net.Conn, session *sessionState, configVersion uint64, frame protocol.Frame) error {
	if !session.lockRuntimeIOWrite(configVersion) {
		return errRuntimeIOStopped
	}
	defer session.unlockRuntimeIOWrite()
	return s.writeFrame(conn, frame)
}

func (s *Server) writeRuntimeFramesWithSession(conn net.Conn, session *sessionState, configVersion uint64, frames ...protocol.Frame) error {
	if !session.lockRuntimeIOWrite(configVersion) {
		return errRuntimeIOStopped
	}
	defer session.unlockRuntimeIOWrite()
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
