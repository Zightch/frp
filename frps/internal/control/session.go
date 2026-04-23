package control

import (
	"errors"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/zightch/frp/frps/pkg/protocol"
	"github.com/zightch/frp/frps/pkg/testsupport"
)

const initialServerRequestID = uint32(1 << 31)

var errRuntimeIOStopped = errors.New("runtime io no longer allowed")

type sessionAppliedConfigState struct {
	group    GroupRuntime
	snapshot ConfigSnapshot
}

type sessionPendingConfigState struct {
	requestID uint32
	group     GroupRuntime
	snapshot  ConfigSnapshot
}

type sessionAckedConfigState struct {
	version uint64
}

type sessionConfigState struct {
	current      sessionAppliedConfigState
	pending      sessionPendingConfigState
	acked        sessionAckedConfigState
	recoveryMode testsupport.RecoveryMode
}

type sessionListenerState struct {
	tcp     map[uint32][]net.Listener
	udp     map[uint32][]UDPListener
	started bool
}

type sessionUDPState struct {
	sessions       map[uint32]*publicUDPSession
	keys           map[string]uint32
	cleanupStarted bool
}

type sessionRuntimeState struct {
	done       chan struct{}
	frozen     bool
	generation uint64
	listeners  sessionListenerState
	streams    map[uint32]*publicStream
	udp        sessionUDPState
}

type sessionState struct {
	ID                  uint64
	nextServerRequestID atomic.Uint32
	nextStreamID        atomic.Uint32
	readTimeout         time.Duration

	configMu sync.Mutex
	config   sessionConfigState

	writeMu     sync.Mutex
	runtimeMu   sync.Mutex
	runtimeIOMu sync.RWMutex
	runtime     sessionRuntimeState

	shutdownOnce sync.Once
}

type observedSessionConfigState struct {
	group                GroupRuntime
	snapshot             ConfigSnapshot
	lastAckedConfigValue uint64
	pendingRequestID     uint32
	pendingGroup         GroupRuntime
	pendingSnapshot      ConfigSnapshot
	recoveryMode         testsupport.RecoveryMode
}

type observedSessionRuntimeState struct {
	frozen           bool
	listenersStarted bool
	generation       uint64
	tcpListeners     map[uint32][]net.Listener
	udpListeners     map[uint32][]UDPListener
}

func newSessionState(id uint64, group GroupRuntime, snapshot ConfigSnapshot, readTimeout time.Duration) *sessionState {
	session := &sessionState{
		ID:          id,
		readTimeout: readTimeout,
	}
	session.config.current = sessionAppliedConfigState{
		group:    group,
		snapshot: snapshot,
	}
	session.runtime.done = make(chan struct{})
	session.runtime.listeners.tcp = make(map[uint32][]net.Listener)
	session.runtime.listeners.udp = make(map[uint32][]UDPListener)
	session.runtime.streams = make(map[uint32]*publicStream)
	session.runtime.udp.sessions = make(map[uint32]*publicUDPSession)
	session.runtime.udp.keys = make(map[string]uint32)
	session.nextServerRequestID.Store(initialServerRequestID - 1)
	return session
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
	if s == nil {
		return nil
	}
	return s.runtime.done
}

func (s *sessionState) closeDone() {
	if s == nil || s.runtime.done == nil {
		return
	}
	s.shutdownOnce.Do(func() {
		close(s.runtime.done)
	})
}

func (s *sessionState) isDone() bool {
	if s == nil || s.runtime.done == nil {
		return false
	}
	select {
	case <-s.runtime.done:
		return true
	default:
		return false
	}
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
	for _, listeners := range s.runtime.listeners.tcp {
		if len(listeners) > 0 {
			return true
		}
	}
	for _, listeners := range s.runtime.listeners.udp {
		if len(listeners) > 0 {
			return true
		}
	}
	return false
}

func (s *sessionState) activeRuntimeTunnelIDs() map[uint32]struct{} {
	s.runtimeMu.Lock()
	defer s.runtimeMu.Unlock()
	if s.runtime.frozen {
		return nil
	}
	return s.activeRuntimeTunnelIDsLocked()
}

func (s *sessionState) activeRuntimeTunnelIDsLocked() map[uint32]struct{} {
	active := make(map[uint32]struct{})
	for tunnelID, listeners := range s.runtime.listeners.tcp {
		if len(listeners) == 0 {
			continue
		}
		active[tunnelID] = struct{}{}
	}
	for tunnelID, listeners := range s.runtime.listeners.udp {
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
	if s.runtime.frozen {
		return false
	}
	return s.hasRuntimeListenersLocked()
}

func (s *sessionState) canStartTunnelRuntime() bool {
	s.runtimeMu.Lock()
	defer s.runtimeMu.Unlock()
	return !s.runtime.frozen
}

func (s *sessionState) resetRuntimeGenerationIfIdle() {
	s.runtimeMu.Lock()
	defer s.runtimeMu.Unlock()
	if s.hasRuntimeListenersLocked() {
		return
	}
	s.runtime.listeners.started = false
	s.runtime.generation = 0
}

func (s *sessionState) attachTunnelListeners(configVersion uint64, tunnelID uint32, tcpListeners []net.Listener, udpListeners []UDPListener) (bool, bool) {
	s.runtimeMu.Lock()
	defer s.runtimeMu.Unlock()
	if s.runtime.frozen {
		return false, false
	}
	if s.runtime.generation != 0 && s.runtime.generation != configVersion {
		return false, false
	}
	if len(s.runtime.listeners.tcp[tunnelID]) > 0 || len(s.runtime.listeners.udp[tunnelID]) > 0 {
		return false, false
	}

	if len(tcpListeners) > 0 {
		s.runtime.listeners.tcp[tunnelID] = append(s.runtime.listeners.tcp[tunnelID], tcpListeners...)
	}
	if len(udpListeners) > 0 {
		s.runtime.listeners.udp[tunnelID] = append(s.runtime.listeners.udp[tunnelID], udpListeners...)
	}

	if s.hasRuntimeListenersLocked() {
		s.runtime.listeners.started = true
		s.runtime.generation = configVersion
	} else {
		s.runtime.listeners.started = false
		s.runtime.generation = 0
	}

	startUDPCleanup := len(udpListeners) > 0 && !s.runtime.udp.cleanupStarted
	if startUDPCleanup {
		s.runtime.udp.cleanupStarted = true
	}
	return startUDPCleanup, true
}

func (s *sessionState) addPublicStream(streamID uint32, stream *publicStream, configVersion uint64) bool {
	s.runtimeMu.Lock()
	defer s.runtimeMu.Unlock()
	if s.runtime.frozen || !s.runtime.listeners.started || s.runtime.generation != configVersion {
		return false
	}
	s.runtime.streams[streamID] = stream
	return true
}

func (s *sessionState) canServeRuntimeIO(configVersion uint64) bool {
	s.runtimeMu.Lock()
	defer s.runtimeMu.Unlock()
	return !s.runtime.frozen && s.runtime.listeners.started && s.runtime.generation == configVersion
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
	return s.runtime.streams[streamID]
}

func (s *sessionState) closePublicStream(streamID uint32) bool {
	s.runtimeMu.Lock()
	stream, ok := s.runtime.streams[streamID]
	if ok {
		delete(s.runtime.streams, streamID)
	}
	s.runtimeMu.Unlock()

	if !ok {
		return false
	}

	stream.signalReady(net.ErrClosed)
	stream.close()
	return true
}

func (s *sessionState) currentGroupID() int64 {
	s.configMu.Lock()
	defer s.configMu.Unlock()
	return s.config.current.group.ID
}

func (s *sessionState) currentGroupAndSnapshot() (GroupRuntime, ConfigSnapshot) {
	s.configMu.Lock()
	defer s.configMu.Unlock()
	return s.config.current.group, s.config.current.snapshot
}

func (s *sessionState) currentSnapshot() ConfigSnapshot {
	s.configMu.Lock()
	defer s.configMu.Unlock()
	return s.config.current.snapshot
}

func (s *sessionState) setRecoveryMode(mode testsupport.RecoveryMode) {
	s.configMu.Lock()
	defer s.configMu.Unlock()
	s.config.recoveryMode = mode
}

func (s *sessionState) recoveryModeValue() testsupport.RecoveryMode {
	s.configMu.Lock()
	defer s.configMu.Unlock()
	return s.config.recoveryMode
}

func (s *sessionState) currentGroup() GroupRuntime {
	s.configMu.Lock()
	defer s.configMu.Unlock()
	return s.config.current.group
}

func (s *sessionState) replaceGroupRuntime(group GroupRuntime) {
	s.configMu.Lock()
	defer s.configMu.Unlock()
	group.Snapshot = s.config.current.snapshot
	s.config.current.group = group
}

func (s *sessionState) reconfigure(group GroupRuntime, snapshot ConfigSnapshot, requestID uint32) error {
	s.configMu.Lock()
	defer s.configMu.Unlock()

	if s.config.pending.requestID != 0 {
		return errConfigUpdateInFlight
	}

	s.config.pending = sessionPendingConfigState{
		requestID: requestID,
		group:     group,
		snapshot:  snapshot,
	}
	return nil
}

func (s *sessionState) configAckState() (uint32, uint64) {
	s.configMu.Lock()
	defer s.configMu.Unlock()
	if s.config.pending.requestID == 0 {
		return 0, s.config.current.snapshot.Version
	}
	return s.config.pending.requestID, s.config.pending.snapshot.Version
}

func (s *sessionState) lastAckedConfigVersion() uint64 {
	s.configMu.Lock()
	defer s.configMu.Unlock()
	return s.config.acked.version
}

func (s *sessionState) acceptConfigAck(requestID uint32, version uint64) error {
	s.configMu.Lock()
	defer s.configMu.Unlock()

	if s.config.pending.requestID == 0 || requestID != s.config.pending.requestID {
		return errUnexpectedConfigAck
	}
	if version != s.config.pending.snapshot.Version {
		return errConfigVersionMismatch
	}

	s.config.current = sessionAppliedConfigState{
		group:    s.config.pending.group,
		snapshot: s.config.pending.snapshot,
	}
	s.config.acked.version = version
	s.config.pending = sessionPendingConfigState{}
	return nil
}

func (s *sessionState) clearPendingConfigRequest(requestID uint32) {
	s.configMu.Lock()
	defer s.configMu.Unlock()
	if s.config.pending.requestID == requestID {
		s.config.pending = sessionPendingConfigState{}
	}
}

func (s *sessionState) hasPendingConfig() bool {
	s.configMu.Lock()
	defer s.configMu.Unlock()
	return s.config.pending.requestID != 0
}

func (s *sessionState) refreshPendingConfig(group GroupRuntime, snapshot ConfigSnapshot) bool {
	s.configMu.Lock()
	defer s.configMu.Unlock()

	if s.config.pending.requestID == 0 {
		return false
	}
	if !samePushedConfigSnapshot(s.config.pending.snapshot, snapshot) {
		return false
	}

	s.config.pending.group = group
	s.config.pending.snapshot = snapshot
	return true
}

func (s *sessionState) freezeTunnelRuntime() ([]net.Listener, []UDPListener, map[uint32]*publicStream, []*publicUDPSession) {
	s.runtimeIOMu.Lock()
	defer s.runtimeIOMu.Unlock()
	s.runtimeMu.Lock()
	defer s.runtimeMu.Unlock()

	s.runtime.frozen = true
	s.runtime.listeners.started = false
	s.runtime.generation = 0

	listeners := make([]net.Listener, 0, len(s.runtime.listeners.tcp))
	for tunnelID, tunnelListeners := range s.runtime.listeners.tcp {
		delete(s.runtime.listeners.tcp, tunnelID)
		listeners = append(listeners, tunnelListeners...)
	}
	udpListeners := make([]UDPListener, 0, len(s.runtime.listeners.udp))
	for tunnelID, tunnelListeners := range s.runtime.listeners.udp {
		delete(s.runtime.listeners.udp, tunnelID)
		udpListeners = append(udpListeners, tunnelListeners...)
	}
	streams := make(map[uint32]*publicStream, len(s.runtime.streams))
	for streamID, stream := range s.runtime.streams {
		delete(s.runtime.streams, streamID)
		streams[streamID] = stream
	}
	udpSessions := make([]*publicUDPSession, 0, len(s.runtime.udp.sessions))
	for sessionID, udpSession := range s.runtime.udp.sessions {
		delete(s.runtime.udp.sessions, sessionID)
		udpSessions = append(udpSessions, udpSession)
	}
	for key := range s.runtime.udp.keys {
		delete(s.runtime.udp.keys, key)
	}

	return listeners, udpListeners, streams, udpSessions
}

func (s *sessionState) allowTunnelRuntimeStart() {
	s.runtimeMu.Lock()
	defer s.runtimeMu.Unlock()
	s.runtime.frozen = false
}

func (s *sessionState) resetTunnelRuntime() {
	listeners, udpListeners, _, _ := s.freezeTunnelRuntime()
	closeStartedTunnelListeners(listeners, udpListeners)
	s.allowTunnelRuntimeStart()
}

func (s *sessionState) observeState() (observedSessionConfigState, observedSessionRuntimeState) {
	s.configMu.Lock()
	configState := observedSessionConfigState{
		group:                s.config.current.group,
		snapshot:             s.config.current.snapshot,
		lastAckedConfigValue: s.config.acked.version,
		pendingRequestID:     s.config.pending.requestID,
		pendingGroup:         s.config.pending.group,
		pendingSnapshot:      s.config.pending.snapshot,
		recoveryMode:         s.config.recoveryMode,
	}
	s.configMu.Unlock()

	s.runtimeMu.Lock()
	runtimeState := observedSessionRuntimeState{
		frozen:           s.runtime.frozen,
		listenersStarted: s.runtime.listeners.started,
		generation:       s.runtime.generation,
		tcpListeners:     cloneTCPListenerMap(s.runtime.listeners.tcp),
		udpListeners:     cloneUDPListenerMap(s.runtime.listeners.udp),
	}
	s.runtimeMu.Unlock()

	return configState, runtimeState
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
	return s.writeFrameWithContext(conn, frame, s.frameContext(conn, session))
}

func (s *Server) writeFramesWithSession(conn net.Conn, session *sessionState, frames ...protocol.Frame) error {
	session.writeMu.Lock()
	defer session.writeMu.Unlock()
	for _, frame := range frames {
		if err := s.writeFrameWithContext(conn, frame, s.frameContext(conn, session)); err != nil {
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
	return s.writeFrameWithContext(conn, frame, s.frameContext(conn, session))
}

func (s *Server) writeRuntimeFramesWithSession(conn net.Conn, session *sessionState, configVersion uint64, frames ...protocol.Frame) error {
	if !session.lockRuntimeIOWrite(configVersion) {
		return errRuntimeIOStopped
	}
	defer session.unlockRuntimeIOWrite()
	for _, frame := range frames {
		if err := s.writeFrameWithContext(conn, frame, s.frameContext(conn, session)); err != nil {
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
