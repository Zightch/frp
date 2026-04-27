package control

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	controlbind "github.com/zightch/frp/frps/internal/control/bind"
	controlrepo "github.com/zightch/frp/frps/internal/control/repo"
	controlsession "github.com/zightch/frp/frps/internal/control/session"
	"github.com/zightch/frp/frps/internal/ports"
	"github.com/zightch/frp/frps/internal/storage"
	"github.com/zightch/frp/frps/internal/system"
	"github.com/zightch/frp/frps/internal/testhooks"
	"github.com/zightch/frp/frps/pkg/protocol"
	"github.com/zightch/frp/frps/pkg/testsupport"
	"github.com/zightch/frp/frps/pkg/transport"
)

const initialServerRequestID = uint32(1 << 31)

var errRuntimeIOStopped = errors.New("runtime io no longer allowed")

type Repository = controlrepo.Repository
type SQLRepository = controlrepo.SQLRepository
type GroupRuntime = controlrepo.GroupRuntime
type ConfigSnapshot = controlrepo.ConfigSnapshot

type UDPListener = controlbind.UDPListener
type ListenKey = controlbind.ListenKey
type BindKind = controlbind.BindKind
type ListenerBind = controlbind.ListenerBind
type ListenerFactory = controlbind.ListenerFactory
type ScriptedListenerFactory = controlbind.ScriptedListenerFactory
type ListenerCall = controlbind.ListenerCall
type ScriptedListenerFailure = controlbind.ScriptedListenerFailure

var ErrGroupNotFound = controlrepo.ErrGroupNotFound

const (
	BindKindRuntimeProbe = controlbind.BindKindRuntimeProbe
	BindKindRuntimeStart = controlbind.BindKindRuntimeStart
)

func NewRepository(store *storage.SQL) *SQLRepository {
	return controlrepo.NewSQLRepository(store)
}

func NewNetListenerFactory() ListenerFactory {
	return controlbind.NewNetListenerFactory()
}

func NewScriptedListenerFactory() *ScriptedListenerFactory {
	return controlbind.NewScriptedListenerFactory()
}

func (s *Server) listenTCP(ctx context.Context, bind ListenerBind) (net.Listener, error) {
	testhooks.Point(
		"control.listener.before_bind",
		testhooks.F("protocol", bind.Key.Protocol),
		testhooks.F("kind", string(bind.Kind)),
		testhooks.F("group_id", bind.GroupID),
		testhooks.F("tunnel_id", bind.TunnelID),
		testhooks.F("port", bind.Key.Port),
	)
	listener, err := s.listeners.ListenTCP(ctx, bind)
	testhooks.Point(
		"control.listener.after_bind",
		testhooks.F("protocol", bind.Key.Protocol),
		testhooks.F("kind", string(bind.Kind)),
		testhooks.F("group_id", bind.GroupID),
		testhooks.F("tunnel_id", bind.TunnelID),
		testhooks.F("port", bind.Key.Port),
		testhooks.F("ok", err == nil),
	)
	return listener, err
}

func (s *Server) resolveUDPAddr(ctx context.Context, bind ListenerBind) (*net.UDPAddr, error) {
	testhooks.Point(
		"control.listener.before_resolve_udp",
		testhooks.F("kind", string(bind.Kind)),
		testhooks.F("group_id", bind.GroupID),
		testhooks.F("tunnel_id", bind.TunnelID),
		testhooks.F("port", bind.Key.Port),
	)
	addr, err := s.listeners.ResolveUDP(ctx, bind)
	testhooks.Point(
		"control.listener.after_resolve_udp",
		testhooks.F("kind", string(bind.Kind)),
		testhooks.F("group_id", bind.GroupID),
		testhooks.F("tunnel_id", bind.TunnelID),
		testhooks.F("port", bind.Key.Port),
		testhooks.F("ok", err == nil),
	)
	return addr, err
}

func (s *Server) listenUDP(ctx context.Context, bind ListenerBind, addr *net.UDPAddr) (UDPListener, error) {
	testhooks.Point(
		"control.listener.before_bind",
		testhooks.F("protocol", bind.Key.Protocol),
		testhooks.F("kind", string(bind.Kind)),
		testhooks.F("group_id", bind.GroupID),
		testhooks.F("tunnel_id", bind.TunnelID),
		testhooks.F("port", bind.Key.Port),
	)
	listener, err := s.listeners.ListenUDP(ctx, bind, addr)
	testhooks.Point(
		"control.listener.after_bind",
		testhooks.F("protocol", bind.Key.Protocol),
		testhooks.F("kind", string(bind.Kind)),
		testhooks.F("group_id", bind.GroupID),
		testhooks.F("tunnel_id", bind.TunnelID),
		testhooks.F("port", bind.Key.Port),
		testhooks.F("ok", err == nil),
	)
	return listener, err
}

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

type sessionConfigPushOperation struct {
	requestID    uint32
	group        GroupRuntime
	snapshot     ConfigSnapshot
	recoveryMode testsupport.RecoveryMode
}

type sessionConfigApplyResult struct {
	group        GroupRuntime
	snapshot     ConfigSnapshot
	recoveryMode testsupport.RecoveryMode
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
	frozen                bool
	listenersStarted      bool
	generation            uint64
	activeTunnelIDs       map[uint32]struct{}
	attachedListeners     []observedSessionRuntimeListener
	activeStreamCount     uint32
	activeUDPSessionCount uint32
	connections           []observedSessionRuntimeConnection
}

type observedSessionRuntimeListener struct {
	tunnelID uint32
	protocol string
	bindIP   string
	port     uint16
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

func pendingRecoveryModeForSnapshot(snapshot ConfigSnapshot) testsupport.RecoveryMode {
	if len(snapshot.Tunnels) == 0 {
		return testsupport.RecoveryModePendingEmptyConfig
	}
	return testsupport.RecoveryModePendingFullConfig
}

func appliedRecoveryModeForSnapshot(snapshot ConfigSnapshot) testsupport.RecoveryMode {
	if len(snapshot.Tunnels) == 0 {
		return testsupport.RecoveryModeEmptyConfig
	}
	return testsupport.RecoveryModeRunning
}

func (s *sessionState) prepareConfigPush(group GroupRuntime, snapshot ConfigSnapshot) (sessionConfigPushOperation, error) {
	requestID := s.nextRequestID()
	recoveryMode := pendingRecoveryModeForSnapshot(snapshot)

	s.configMu.Lock()
	defer s.configMu.Unlock()

	if s.config.pending.requestID != 0 {
		return sessionConfigPushOperation{}, errConfigUpdateInFlight
	}

	s.config.pending = sessionPendingConfigState{
		requestID: requestID,
		group:     group,
		snapshot:  snapshot,
	}
	s.config.recoveryMode = recoveryMode
	return sessionConfigPushOperation{
		requestID:    requestID,
		group:        group,
		snapshot:     snapshot,
		recoveryMode: recoveryMode,
	}, nil
}

func (s *sessionState) reconfigure(group GroupRuntime, snapshot ConfigSnapshot, requestID uint32) error {
	recoveryMode := pendingRecoveryModeForSnapshot(snapshot)

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
	s.config.recoveryMode = recoveryMode
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

func (s *sessionState) acceptConfigAck(requestID uint32, version uint64) (sessionConfigApplyResult, error) {
	s.configMu.Lock()
	defer s.configMu.Unlock()

	if s.config.pending.requestID == 0 || requestID != s.config.pending.requestID {
		return sessionConfigApplyResult{}, errUnexpectedConfigAck
	}
	if version != s.config.pending.snapshot.Version {
		return sessionConfigApplyResult{}, errConfigVersionMismatch
	}

	s.config.current = sessionAppliedConfigState{
		group:    s.config.pending.group,
		snapshot: s.config.pending.snapshot,
	}
	s.config.acked.version = version
	s.config.recoveryMode = appliedRecoveryModeForSnapshot(s.config.current.snapshot)
	s.config.pending = sessionPendingConfigState{}
	return sessionConfigApplyResult{
		group:        s.config.current.group,
		snapshot:     s.config.current.snapshot,
		recoveryMode: s.config.recoveryMode,
	}, nil
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
	runtimeConnections := observeRuntimeConnections(s.runtime.streams, s.runtime.udp.sessions)
	runtimeState := observedSessionRuntimeState{
		frozen:                s.runtime.frozen,
		listenersStarted:      s.runtime.listeners.started,
		generation:            s.runtime.generation,
		activeTunnelIDs:       s.activeRuntimeTunnelIDsLocked(),
		attachedListeners:     observeRuntimeListeners(s.runtime.listeners.tcp, s.runtime.listeners.udp),
		activeStreamCount:     uint32(len(s.runtime.streams)),
		activeUDPSessionCount: uint32(len(s.runtime.udp.sessions)),
		connections:           runtimeConnections,
	}
	s.runtimeMu.Unlock()

	return configState, runtimeState
}

func observeRuntimeListeners(tcp map[uint32][]net.Listener, udp map[uint32][]UDPListener) []observedSessionRuntimeListener {
	listeners := make([]observedSessionRuntimeListener, 0, len(tcp)+len(udp))
	for tunnelID, tunnelListeners := range tcp {
		for _, listener := range tunnelListeners {
			bindIP, port := listenerAddr(listener.Addr())
			listeners = append(listeners, observedSessionRuntimeListener{
				tunnelID: tunnelID,
				protocol: "tcp",
				bindIP:   bindIP,
				port:     port,
			})
		}
	}
	for tunnelID, tunnelListeners := range udp {
		for _, listener := range tunnelListeners {
			bindIP, port := listenerAddr(listener.LocalAddr())
			listeners = append(listeners, observedSessionRuntimeListener{
				tunnelID: tunnelID,
				protocol: "udp",
				bindIP:   bindIP,
				port:     port,
			})
		}
	}
	return listeners
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

var (
	errConfigUpdateInFlight  = errors.New("config update already in flight")
	errUnexpectedConfigAck   = errors.New("unexpected config ack")
	errConfigVersionMismatch = errors.New("config version mismatch")
)

type sessionRuntimeStartTarget struct {
	conn     net.Conn
	logger   Logger
	session  *sessionState
	group    GroupRuntime
	snapshot ConfigSnapshot
}

type sessionRuntimeStartPlan struct {
	group               GroupRuntime
	snapshot            ConfigSnapshot
	blocked             bool
	activeRuntime       bool
	clearIssueTunnelIDs []uint32
	targetTunnels       []protocol.TunnelEntry
	bindIP              string
	bindErr             error
	conflictIssues      map[uint32]string
}

type tunnelListenerOperationContext struct {
	groupID       int64
	configVersion uint64
	tunnel        protocol.TunnelEntry
	bindIP        string
	kind          BindKind
}

type tunnelRuntimeServeContext struct {
	logger     Logger
	session    *sessionState
	runtimeIO  sessionRuntimeIOWriter
	tunnel     protocol.TunnelEntry
	remotePort uint16
}

type runtimeGroupSnapshot struct {
	group    GroupRuntime
	snapshot ConfigSnapshot
}

func (s *Server) handleConfigAck(conn net.Conn, logger *slog.Logger, session *sessionState, agent *controlsession.Agent, frame protocol.Frame) error {
	if frame.RequestID == 0 {
		return s.replyErrorWithSession(conn, session, 0, frame.StreamID, protocol.ErrorCodeProtocolBadBody, "config.ack requestId must be non-zero")
	}
	if frame.StreamID != 0 {
		return s.replyErrorWithSession(conn, session, frame.RequestID, frame.StreamID, protocol.ErrorCodeProtocolBadBody, "config.ack streamId must be zero")
	}

	ack, err := protocol.UnmarshalConfigAck(frame.Body)
	if err != nil {
		return s.replyProtocolErrorWithSession(conn, session, frame, err)
	}
	pendingRequestID, expectedVersion := session.configAckState()
	currentGroup := session.currentGroup()
	isInitialStartup := session.lastAckedConfigVersion() == 0
	if pendingRequestID == 0 || frame.RequestID != pendingRequestID {
		return s.replyErrorWithSession(conn, session, frame.RequestID, frame.StreamID, protocol.ErrorCodeProtocolBadBody, "unexpected config.ack requestId %d", frame.RequestID)
	}
	if ack.ConfigVersion != expectedVersion {
		return s.replyErrorWithSession(
			conn,
			session,
			frame.RequestID,
			0,
			protocol.ErrorCodeProtocolBadBody,
			"config.ack version mismatch: got %d want %d",
			ack.ConfigVersion,
			expectedVersion,
		)
	}
	if ack.Status == protocol.StatusError {
		return s.replyErrorWithSession(
			conn,
			session,
			frame.RequestID,
			0,
			protocol.ErrorCodeConfigApplyFailed,
			"client rejected config version %d: %s",
			ack.ConfigVersion,
			strings.TrimSpace(ack.Message),
		)
	}
	if ack.Status != protocol.StatusOK {
		return s.replyErrorWithSession(conn, session, frame.RequestID, 0, protocol.ErrorCodeProtocolBadBody, "unsupported config.ack status %d", ack.Status)
	}

	testhooks.Point(
		"control.config_ack.before_accept",
		testhooks.F("group_id", currentGroup.ID),
		testhooks.F("session_id", session.ID),
		testhooks.F("request_id", frame.RequestID),
		testhooks.F("config_version", ack.ConfigVersion),
	)
	appliedConfig, err := session.acceptConfigAck(frame.RequestID, ack.ConfigVersion)
	if err != nil {
		switch {
		case errors.Is(err, errUnexpectedConfigAck):
			return s.replyErrorWithSession(conn, session, frame.RequestID, frame.StreamID, protocol.ErrorCodeProtocolBadBody, "unexpected config.ack requestId %d", frame.RequestID)
		case errors.Is(err, errConfigVersionMismatch):
			return s.replyErrorWithSession(
				conn,
				session,
				frame.RequestID,
				0,
				protocol.ErrorCodeProtocolBadBody,
				"config.ack version mismatch: got %d want %d",
				ack.ConfigVersion,
				expectedVersion,
			)
		default:
			return err
		}
	}
	if isInitialStartup {
		if _, err := s.resolveGroupEffectiveIP(appliedConfig.group); err != nil {
			reason := buildGroupEffectiveIPRuntimeReason(appliedConfig.group, err)
			for _, tunnel := range enabledTunnels(appliedConfig.snapshot) {
				s.recordTunnelRuntimeIssueForConfig(tunnel.TunnelID, appliedConfig.snapshot.Version, reason)
			}
			if reason, ok := buildInitialStartupRejectedReason(appliedConfig.group, err); ok {
				return s.replyErrorWithSession(conn, session, frame.RequestID, 0, protocol.ErrorCodeConfigApplyFailed, "%s", reason)
			}
			return err
		}
	}
	testhooks.Point(
		"control.config_ack.after_accept",
		testhooks.F("group_id", appliedConfig.group.ID),
		testhooks.F("session_id", session.ID),
		testhooks.F("request_id", frame.RequestID),
		testhooks.F("config_version", ack.ConfigVersion),
	)
	logger.Info("config acknowledged", "config_version", ack.ConfigVersion, "applied_at_ms", ack.AppliedAtMs)
	if agent == nil || !agent.Enqueue(controlsession.ConfigAckReceived{
		RequestID:     frame.RequestID,
		ConfigVersion: ack.ConfigVersion,
	}) {
		return net.ErrClosed
	}
	return nil
}

func (s *Server) pushConfig(conn net.Conn, session *sessionState) error {
	group, snapshot := session.currentGroupAndSnapshot()
	return s.pushReloadConfig(conn, session, group, snapshot)
}

func (s *Server) loadGroupRuntimeByClientID(clientID [16]byte) (GroupRuntime, error) {
	ctx, cancel := context.WithTimeout(context.Background(), s.options.ReadTimeout)
	defer cancel()
	return s.repo.LoadGroupRuntimeByClientID(ctx, clientID)
}

func (s *Server) loadGroupRuntimeByID(groupID int64) (GroupRuntime, error) {
	ctx, cancel := context.WithTimeout(context.Background(), s.options.ReadTimeout)
	defer cancel()
	return s.repo.LoadGroupRuntimeByID(ctx, groupID)
}

func runtimeSnapshotForGroup(group GroupRuntime) ConfigSnapshot {
	snapshot := group.Snapshot
	if group.Enabled {
		return snapshot
	}
	snapshot.Tunnels = nil
	return snapshot
}

func (s *Server) pushReloadConfig(conn net.Conn, session *sessionState, group GroupRuntime, snapshot ConfigSnapshot) error {
	body, err := protocol.MarshalConfigPush(protocol.ConfigPush{
		ConfigVersion: snapshot.Version,
		GeneratedAtMs: snapshot.GeneratedAtMs,
		Tunnels:       snapshot.Tunnels,
	})
	if err != nil {
		return err
	}
	pushOp, err := session.prepareConfigPush(group, snapshot)
	if err != nil {
		return err
	}
	testhooks.Point(
		"control.config_push.before_write",
		testhooks.F("group_id", pushOp.group.ID),
		testhooks.F("session_id", session.ID),
		testhooks.F("request_id", pushOp.requestID),
		testhooks.F("config_version", pushOp.snapshot.Version),
		testhooks.F("tunnel_count", len(pushOp.snapshot.Tunnels)),
	)
	if err := s.writeFrameWithSession(conn, session, protocol.Frame{
		Type:      protocol.TypeConfigPush,
		RequestID: pushOp.requestID,
		Body:      body,
	}); err != nil {
		session.clearPendingConfigRequest(pushOp.requestID)
		return err
	}
	testhooks.Point(
		"control.config_push.after_write",
		testhooks.F("group_id", pushOp.group.ID),
		testhooks.F("session_id", session.ID),
		testhooks.F("request_id", pushOp.requestID),
		testhooks.F("config_version", pushOp.snapshot.Version),
		testhooks.F("tunnel_count", len(pushOp.snapshot.Tunnels)),
	)
	return nil
}

func (s *Server) applyAcceptedConfig(conn net.Conn, logger Logger, session *sessionState, _ sessionConfigApplyResult) error {
	session.allowTunnelRuntimeStart()
	return s.ensureTunnelListeners(conn, logger, session)
}

func (s *Server) RefreshGroup(groupID int64) {
	if s == nil || groupID <= 0 {
		return
	}

	active, ok := s.activeSession(groupID)
	if !ok || active == nil || active.session == nil {
		return
	}

	group, err := s.loadGroupRuntimeByID(groupID)
	if err != nil {
		switch {
		case errors.Is(err, ErrGroupNotFound):
			s.logger.Info("closing active session for deleted proxy group", "group_id", groupID, "session_id", active.session.ID)
			_ = active.conn.Close()
		default:
			s.logger.Warn("refresh proxy group runtime failed", "group_id", groupID, "session_id", active.session.ID, "error", err)
		}
		return
	}

	snapshot, _, _ := s.runtimeRefreshSnapshot(group, runtimeSnapshotForGroup(group))
	group.Snapshot = snapshot
	currentGroup, currentSnapshot := active.session.currentGroupAndSnapshot()
	if currentGroup.EffectiveIP == group.EffectiveIP && samePushedConfigSnapshot(currentSnapshot, group.Snapshot) {
		active.session.replaceGroupRuntime(group)
	}
	if runtime := s.runtimeExecutor(active.session.ID); runtime != nil {
		runtime.setDesiredGroup(group)
	}
	s.supervisor.UpdateDesiredRuntime(groupID, desiredRuntimeFromGroup(group))
}

func (s *Server) freezeGroupRuntime(conn net.Conn, session *sessionState) error {
	listeners, udpListeners, streams, udpSessions := session.freezeTunnelRuntime()
	closeStartedTunnelListeners(listeners, udpListeners)

	var freezeErr error
	for streamID, stream := range streams {
		if err := s.sendStreamClose(conn, session, streamID, protocol.CloseReasonAdminTerminated, "reload in progress"); err != nil && freezeErr == nil {
			freezeErr = err
		}
		stream.signalReady(net.ErrClosed)
		stream.close()
	}
	for _, udpSession := range udpSessions {
		if err := s.sendUDPClose(conn, session, udpSession.sessionID, protocol.CloseReasonAdminTerminated, "reload in progress"); err != nil && freezeErr == nil {
			freezeErr = err
		}
	}
	return freezeErr
}

func (s *Server) rebindGroupRuntime(conn net.Conn, session *sessionState, group GroupRuntime) error {
	if err := s.freezeGroupRuntime(conn, session); err != nil {
		return err
	}
	session.replaceGroupRuntime(group)
	session.allowTunnelRuntimeStart()
	logger := s.logger.With(
		"session_id", session.ID,
		"group_id", group.ID,
		"group_name", group.Name,
	)
	return s.ensureTunnelListeners(conn, logger, session)
}

func (s *Server) runtimeRefreshSnapshot(group GroupRuntime, snapshot ConfigSnapshot) (ConfigSnapshot, error, bool) {
	enabled := enabledTunnels(snapshot)
	if len(enabled) == 0 {
		return snapshot, nil, false
	}

	if _, err := s.resolveGroupEffectiveIP(group); err != nil {
		s.clearTunnelRuntimeIssues(group.Snapshot.Tunnels)
		reason := buildGroupEffectiveIPRuntimeReason(group, err)
		for _, tunnel := range enabled {
			s.recordTunnelRuntimeIssueForConfig(tunnel.TunnelID, snapshot.Version, reason)
		}
		return emptyConfigSnapshot(snapshot), err, true
	}

	return snapshot, nil, false
}

func emptyConfigSnapshot(snapshot ConfigSnapshot) ConfigSnapshot {
	snapshot.Tunnels = nil
	return snapshot
}

func samePushedConfigSnapshot(current, next ConfigSnapshot) bool {
	return current.Version == next.Version &&
		current.GeneratedAtMs == next.GeneratedAtMs &&
		sameRuntimeSnapshot(current, next)
}

func sameRuntimeSnapshot(current, next ConfigSnapshot) bool {
	return sameTunnelEntries(current.Tunnels, next.Tunnels)
}

func sameTunnelEntries(left, right []protocol.TunnelEntry) bool {
	if len(left) == 0 && len(right) == 0 {
		return true
	}
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if !sameTunnelEntry(left[index], right[index]) {
			return false
		}
	}
	return true
}

func sameTunnelEntry(left, right protocol.TunnelEntry) bool {
	return left.TunnelID == right.TunnelID &&
		left.Protocol == right.Protocol &&
		left.TunnelFlags == right.TunnelFlags &&
		left.RemoteStart == right.RemoteStart &&
		left.RemoteEnd == right.RemoteEnd &&
		sameHost(left.LocalHost, right.LocalHost) &&
		left.LocalStart == right.LocalStart &&
		left.LocalEnd == right.LocalEnd &&
		left.Revision == right.Revision &&
		left.BackendTLSMode == right.BackendTLSMode &&
		left.BackendTLSLoadSystemCA == right.BackendTLSLoadSystemCA &&
		left.BackendTLSInsecureSkipVerify == right.BackendTLSInsecureSkipVerify &&
		left.BackendTLSServerName == right.BackendTLSServerName &&
		left.BackendTLSCAPEM == right.BackendTLSCAPEM &&
		left.BackendTLSClientCertPEM == right.BackendTLSClientCertPEM &&
		left.BackendTLSClientKeyPEM == right.BackendTLSClientKeyPEM
}

func sameHost(left, right protocol.Host) bool {
	if left.Type != right.Type || left.Name != right.Name {
		return false
	}
	switch left.Type {
	case protocol.HostTypeIPv4, protocol.HostTypeIPv6:
		return left.IP.Equal(right.IP)
	default:
		return true
	}
}

func newSessionRuntimeStartTarget(conn net.Conn, logger Logger, session *sessionState) sessionRuntimeStartTarget {
	group, snapshot := session.currentGroupAndSnapshot()
	return sessionRuntimeStartTarget{
		conn:     conn,
		logger:   logger,
		session:  session,
		group:    group,
		snapshot: snapshot,
	}
}

func newTunnelRuntimeStartContext(groupID int64, configVersion uint64, tunnel protocol.TunnelEntry, bindIP string) tunnelListenerOperationContext {
	return tunnelListenerOperationContext{
		groupID:       groupID,
		configVersion: configVersion,
		tunnel:        tunnel,
		bindIP:        bindIP,
		kind:          BindKindRuntimeStart,
	}
}

func newTunnelRuntimeProbeContext(groupID int64, tunnel protocol.TunnelEntry, bindIP string) tunnelListenerOperationContext {
	return tunnelListenerOperationContext{
		groupID: groupID,
		tunnel:  tunnel,
		bindIP:  bindIP,
		kind:    BindKindRuntimeProbe,
	}
}

func (c tunnelListenerOperationContext) remotePortCount() int {
	return int(c.tunnel.RemoteEnd-c.tunnel.RemoteStart) + 1
}

func (c tunnelListenerOperationContext) listenerBind(remotePort uint16) ListenerBind {
	return ListenerBind{
		GroupID:       c.groupID,
		TunnelID:      c.tunnel.TunnelID,
		ConfigVersion: c.configVersion,
		Kind:          c.kind,
		Key: ListenKey{
			Protocol: protocolName(c.tunnel.Protocol),
			IP:       c.bindIP,
			Port:     remotePort,
		},
	}
}

func (c tunnelListenerOperationContext) listenerStartError(remotePort uint16, err error) error {
	return &tunnelListenerStartError{
		TunnelID:    c.tunnel.TunnelID,
		Protocol:    c.tunnel.Protocol,
		EffectiveIP: c.bindIP,
		RemotePort:  remotePort,
		Cause:       err,
	}
}

func (c tunnelListenerOperationContext) serveContext(server *Server, conn net.Conn, logger Logger, session *sessionState, remotePort uint16) tunnelRuntimeServeContext {
	return tunnelRuntimeServeContext{
		logger:     logger,
		session:    session,
		runtimeIO:  newSessionRuntimeIOWriter(server, conn, session, c.configVersion),
		tunnel:     c.tunnel,
		remotePort: remotePort,
	}
}

func (s runtimeSessionSnapshot) activeRuntimeGroup() (runtimeGroupSnapshot, bool) {
	activeTunnelIDs := s.activeRuntimeTunnelIDs()
	if len(activeTunnelIDs) == 0 {
		return runtimeGroupSnapshot{}, false
	}

	group := s.desiredGroup
	config := buildRuntimeObservedConfig(s.state, group)
	snapshot := config.snapshot
	snapshot.Tunnels = filterTunnelsByID(snapshot.Tunnels, activeTunnelIDs)
	if len(snapshot.Tunnels) == 0 {
		return runtimeGroupSnapshot{}, false
	}

	group.EffectiveIP = config.effectiveIP
	group.Snapshot = snapshot
	return runtimeGroupSnapshot{
		group:    group,
		snapshot: snapshot,
	}, true
}

func (s runtimeSessionSnapshot) activeRuntimeTunnelIDs() map[uint32]struct{} {
	if s.runtime.frozen {
		return nil
	}
	return s.runtime.activeTunnelIDs
}

func (s *Server) reserveGroupSlot(groupID int64, sessionID uint64) bool {
	if s == nil || s.supervisor == nil {
		return false
	}
	return s.supervisor.ReserveGroupSlot(groupID, sessionID)
}

func (s *Server) releaseGroupSlot(groupID int64, sessionID uint64) {
	if s == nil || s.supervisor == nil {
		return
	}
	s.supervisor.ReleaseGroupSlot(groupID, sessionID)
}

func (s *Server) activeSession(groupID int64) (*activeSession, bool) {
	if s == nil || s.supervisor == nil {
		return nil, false
	}
	return s.supervisor.ActiveSession(groupID)
}

func (s *Server) activeRuntimeGroups(exclude *sessionState) []runtimeGroupSnapshot {
	if s == nil || s.supervisor == nil {
		return nil
	}

	snapshot := s.supervisor.Snapshot(exclude)
	if len(snapshot.sessions) == 0 {
		return nil
	}

	result := make([]runtimeGroupSnapshot, 0, len(snapshot.sessions))
	for _, session := range snapshot.sessions {
		group, ok := session.activeRuntimeGroup()
		if !ok {
			continue
		}
		result = append(result, group)
	}
	return result
}

func (s *Server) registerActiveSession(conn net.Conn, session *sessionState) {
	if s == nil || s.supervisor == nil || conn == nil || session == nil {
		return
	}
	_ = attachProjectedRuntimeSession(context.Background(), s.supervisor, s.logger, conn, session)
}

func (s *Server) unregisterActiveSession(session *sessionState) {
	if s == nil || s.supervisor == nil || session == nil {
		return
	}
	s.supervisor.DetachRuntime(session.ID)
	_ = s.supervisor.DispatchBySessionID(session.ID, controlsession.ControlConnClosed{Reason: "runtime unregistered"})
}

func projectedSessionState(session *sessionState, conn net.Conn) controlsession.SessionState {
	if session == nil {
		return controlsession.SessionState{}
	}

	configState, runtimeState := session.observeState()
	state := controlsession.NewState(configState.group.ID, session.ID)
	state.Conn = controlsession.ControlConnState{
		Attached: conn != nil,
		ConnID:   transport.ConnectionID(conn),
	}
	state.Phase = controlsession.SessionPhaseOnline

	applied := desiredRuntimeFromObservedSnapshot(configState.group.EffectiveIP, configState.snapshot)
	if configState.lastAckedConfigValue != 0 || configState.snapshot.Version != 0 || len(configState.snapshot.Tunnels) != 0 {
		state.Applied = &controlsession.AppliedRuntimeSnapshot{Snapshot: applied}
	}

	desired := applied
	if configState.pendingRequestID != 0 {
		pending := desiredRuntimeFromObservedSnapshot(configState.pendingGroup.EffectiveIP, configState.pendingSnapshot)
		state.Pending = &controlsession.PendingConfigPush{
			RequestID: configState.pendingRequestID,
			Snapshot:  pending,
		}
		desired = pending
		state.Phase = controlsession.SessionPhaseSyncingConfig
	}
	state.Desired = &desired
	state.Bindings = projectSessionBindings(desired, runtimeState.activeTunnelIDs)
	state.RuntimePhase = projectedRuntimePhase(desired, state.Bindings, runtimeState)

	return state
}

func projectSessionBindings(snapshot controlsession.DesiredRuntimeSnapshot, activeTunnelIDs map[uint32]struct{}) map[controlsession.BindingKey]controlsession.BindingState {
	bindings := make(map[controlsession.BindingKey]controlsession.BindingState)
	for _, tunnel := range snapshot.Tunnels {
		if !tunnel.Enabled {
			continue
		}

		phase := controlsession.BindingPhaseClosed
		if _, ok := activeTunnelIDs[tunnel.TunnelID]; ok {
			phase = controlsession.BindingPhaseActive
		}

		for port := tunnel.RemoteStart; port <= tunnel.RemoteEnd; port++ {
			key := controlsession.BindingKey{
				Protocol:    tunnel.Protocol,
				EffectiveIP: snapshot.EffectiveIP,
				Port:        port,
			}
			bindings[key] = controlsession.BindingState{
				Key:   key,
				Phase: phase,
			}
			if port == tunnel.RemoteEnd {
				break
			}
		}
	}
	return bindings
}

func projectedRuntimePhase(snapshot controlsession.DesiredRuntimeSnapshot, bindings map[controlsession.BindingKey]controlsession.BindingState, runtime observedSessionRuntimeState) controlsession.RuntimePhase {
	if !desiredSnapshotHasEnabledTunnels(snapshot) {
		return controlsession.RuntimePhaseEmpty
	}
	if runtime.frozen {
		return controlsession.RuntimePhaseBlocked
	}
	if len(bindings) == 0 {
		return controlsession.RuntimePhaseBinding
	}
	for _, binding := range bindings {
		if binding.Phase != controlsession.BindingPhaseActive {
			if runtime.listenersStarted {
				return controlsession.RuntimePhaseRecovering
			}
			return controlsession.RuntimePhaseBinding
		}
	}
	return controlsession.RuntimePhaseActive
}

func desiredSnapshotHasEnabledTunnels(snapshot controlsession.DesiredRuntimeSnapshot) bool {
	for _, tunnel := range snapshot.Tunnels {
		if tunnel.Enabled {
			return true
		}
	}
	return false
}

func desiredRuntimeFromObservedSnapshot(effectiveIP string, snapshot ConfigSnapshot) controlsession.DesiredRuntimeSnapshot {
	return controlsession.DesiredRuntimeSnapshot{
		Version:       snapshot.Version,
		GeneratedAtMs: snapshot.GeneratedAtMs,
		EffectiveIP:   effectiveIP,
		Tunnels:       desiredTunnelsFromConfig(snapshot.Tunnels),
	}
}

func attachProjectedRuntimeSession(parent context.Context, supervisor *Supervisor, baseLogger Logger, conn net.Conn, session *sessionState) *controlsession.Agent {
	if supervisor == nil || conn == nil || session == nil {
		return nil
	}

	group, snapshot := session.currentGroupAndSnapshot()
	group.Snapshot = snapshot
	runtime := &runtimeExecutor{
		groupID:      group.ID,
		conn:         conn,
		logger:       scopedRuntimeLogger(baseLogger, session, group),
		session:      session,
		desiredGroup: group,
	}
	return supervisor.AttachSession(parent, projectedSessionState(session, conn), runtime)
}

func scopedRuntimeLogger(base Logger, session *sessionState, group GroupRuntime) *slog.Logger {
	if base == nil || session == nil {
		return nil
	}
	if logger, ok := base.(*slog.Logger); ok {
		return logger.With("session_id", session.ID, "group_id", group.ID, "group_name", group.Name)
	}
	return nil
}

type tunnelRuntimeIssue struct {
	Reason        string
	ConfigVersion uint64
}

type runtimeIssueStore struct {
	mu      sync.RWMutex
	tunnels map[int64]tunnelRuntimeIssue
}

type tcpTunnelListener struct {
	configVersion uint64
	tunnel        protocol.TunnelEntry
	remotePort    uint16
	listener      net.Listener
}

type udpTunnelListener struct {
	configVersion uint64
	tunnel        protocol.TunnelEntry
	remotePort    uint16
	listener      UDPListener
}

type tunnelListenerStartError struct {
	TunnelID    uint32
	Protocol    uint8
	EffectiveIP string
	RemotePort  uint16
	Cause       error
}

type groupEffectiveIPStartErrorKind uint8

const (
	groupEffectiveIPStartErrorInvalid groupEffectiveIPStartErrorKind = iota + 1
	groupEffectiveIPStartErrorNotLocal
)

type groupEffectiveIPStartError struct {
	EffectiveIP string
	Kind        groupEffectiveIPStartErrorKind
	Cause       error
}

type runtimeClaimOwner struct {
	GroupID     int64
	GroupName   string
	TunnelID    uint32
	EffectiveIP string
}

type tunnelListenerBatch struct {
	tcpListeners []net.Listener
	tcpRuntimes  []tcpTunnelListener
	udpListeners []UDPListener
	udpRuntimes  []udpTunnelListener
}

func newRuntimeIssueStore() *runtimeIssueStore {
	return &runtimeIssueStore{
		tunnels: make(map[int64]tunnelRuntimeIssue),
	}
}

func (s *runtimeIssueStore) snapshotReasons() map[int64]string {
	if s == nil {
		return nil
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	if len(s.tunnels) == 0 {
		return nil
	}

	issues := make(map[int64]string, len(s.tunnels))
	for tunnelID, issue := range s.tunnels {
		issues[tunnelID] = issue.Reason
	}
	return issues
}

func (s *runtimeIssueStore) clearTunnels(tunnels []protocol.TunnelEntry) {
	if s == nil || len(tunnels) == 0 {
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	for _, tunnel := range tunnels {
		delete(s.tunnels, int64(tunnel.TunnelID))
	}
}

func (s *runtimeIssueStore) record(tunnelID uint32, reason string) {
	s.recordForConfig(tunnelID, 0, reason)
}

func (s *runtimeIssueStore) recordForConfig(tunnelID uint32, configVersion uint64, reason string) {
	if s == nil || tunnelID == 0 {
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	current, ok := s.tunnels[int64(tunnelID)]
	if ok && configVersion != 0 && current.ConfigVersion > configVersion {
		return
	}

	trimmedReason := strings.TrimSpace(reason)
	if trimmedReason == "" {
		delete(s.tunnels, int64(tunnelID))
		return
	}

	s.tunnels[int64(tunnelID)] = tunnelRuntimeIssue{
		Reason:        trimmedReason,
		ConfigVersion: configVersion,
	}
}

func (s *runtimeIssueStore) clearUnknown(knownTunnelIDs map[int64]struct{}) {
	if s == nil {
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	for tunnelID := range s.tunnels {
		if _, ok := knownTunnelIDs[tunnelID]; ok {
			continue
		}
		delete(s.tunnels, tunnelID)
	}
}

func (s *runtimeIssueStore) applyScanResult(snapshot ConfigSnapshot, staticConflictIDs map[int64]struct{}, issues map[uint32]string, preserved map[uint32]struct{}) {
	if s == nil {
		return
	}

	for _, tunnel := range snapshot.Tunnels {
		if tunnel.TunnelFlags&protocol.TunnelFlagEnabled == 0 {
			s.recordForConfig(tunnel.TunnelID, snapshot.Version, "")
			continue
		}
		if _, conflicted := staticConflictIDs[int64(tunnel.TunnelID)]; conflicted {
			s.recordForConfig(tunnel.TunnelID, snapshot.Version, "")
			continue
		}
		if _, keep := preserved[tunnel.TunnelID]; keep {
			continue
		}
		s.recordForConfig(tunnel.TunnelID, snapshot.Version, issues[tunnel.TunnelID])
	}
}

func (s *Server) TunnelRuntimeIssues() map[int64]string {
	if s == nil || s.runtimeIssues == nil {
		return nil
	}
	return s.runtimeIssues.snapshotReasons()
}

func (s *Server) clearTunnelRuntimeIssues(tunnels []protocol.TunnelEntry) {
	if s == nil || s.runtimeIssues == nil {
		return
	}
	s.runtimeIssues.clearTunnels(tunnels)
}

func (s *Server) recordTunnelRuntimeIssue(tunnelID uint32, reason string) {
	if s == nil || s.runtimeIssues == nil {
		return
	}
	s.runtimeIssues.record(tunnelID, reason)
}

func (s *Server) recordTunnelRuntimeIssueForConfig(tunnelID uint32, configVersion uint64, reason string) {
	if s == nil || s.runtimeIssues == nil {
		return
	}
	s.runtimeIssues.recordForConfig(tunnelID, configVersion, reason)
}

func (s *Server) clearUnknownTunnelRuntimeIssues(knownTunnelIDs map[int64]struct{}) {
	if s == nil || s.runtimeIssues == nil {
		return
	}
	s.runtimeIssues.clearUnknown(knownTunnelIDs)
}

func (s *Server) applyScannedTunnelRuntimeIssues(snapshot ConfigSnapshot, staticConflictIDs map[int64]struct{}, issues map[uint32]string, preserved map[uint32]struct{}) {
	if s == nil || s.runtimeIssues == nil {
		return
	}
	s.runtimeIssues.applyScanResult(snapshot, staticConflictIDs, issues, preserved)
}

func (e *tunnelListenerStartError) Error() string {
	if e == nil {
		return ""
	}
	return buildTunnelListenerStartReason(e.Protocol, e.EffectiveIP, e.RemotePort, e.Cause)
}

func (e *tunnelListenerStartError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Cause
}

func (e *groupEffectiveIPStartError) Error() string {
	if e == nil {
		return ""
	}
	switch e.Kind {
	case groupEffectiveIPStartErrorInvalid:
		return fmt.Sprintf("group effective_ip %q is invalid: %v", e.EffectiveIP, e.Cause)
	case groupEffectiveIPStartErrorNotLocal:
		return fmt.Sprintf("group effective_ip %q is not a current local IP", e.EffectiveIP)
	default:
		if e.Cause == nil {
			return fmt.Sprintf("group effective_ip %q failed", e.EffectiveIP)
		}
		return fmt.Sprintf("group effective_ip %q failed: %v", e.EffectiveIP, e.Cause)
	}
}

func (e *groupEffectiveIPStartError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Cause
}

func (s *Server) ensureTunnelListeners(conn net.Conn, logger Logger, session *sessionState) error {
	target := newSessionRuntimeStartTarget(conn, logger, session)
	plan := s.planSessionRuntimeStart(target)
	return s.applySessionRuntimeStartPlan(target, plan)
}

func (s *Server) planSessionRuntimeStart(target sessionRuntimeStartTarget) sessionRuntimeStartPlan {
	plan := sessionRuntimeStartPlan{
		group:    target.group,
		snapshot: target.snapshot,
	}
	if s.isShuttingDown() || target.session.isDone() || !target.session.canStartTunnelRuntime() {
		plan.blocked = true
		return plan
	}
	if len(target.snapshot.Tunnels) == 0 {
		return plan
	}

	activeTunnelIDs := target.session.activeRuntimeTunnelIDs()
	plan.activeRuntime = len(activeTunnelIDs) != 0
	plan.targetTunnels = selectNonListeningEnabledTunnels(target.snapshot.Tunnels, activeTunnelIDs)
	plan.clearIssueTunnelIDs = collectRuntimeIssueClearTunnelIDs(target.snapshot.Tunnels, activeTunnelIDs)
	if len(plan.targetTunnels) == 0 {
		return plan
	}

	bindIP, err := s.resolveGroupEffectiveIP(target.group)
	if err != nil {
		plan.bindErr = err
		return plan
	}
	plan.bindIP = bindIP
	plan.conflictIssues = s.detectRuntimePortConflictIssues(target.group, bindIP, plan.targetTunnels)
	return plan
}

func (s *Server) applySessionRuntimeStartPlan(target sessionRuntimeStartTarget, plan sessionRuntimeStartPlan) error {
	if plan.blocked {
		return nil
	}
	if len(plan.snapshot.Tunnels) == 0 {
		target.session.resetRuntimeGenerationIfIdle()
		target.session.setRecoveryMode(testsupport.RecoveryModeEmptyConfig)
		return nil
	}
	for _, tunnelID := range plan.clearIssueTunnelIDs {
		s.recordTunnelRuntimeIssueForConfig(tunnelID, plan.snapshot.Version, "")
	}
	if plan.bindErr != nil {
		reason := buildGroupEffectiveIPRuntimeReason(plan.group, plan.bindErr)
		for _, tunnel := range enabledTunnels(plan.snapshot) {
			s.recordTunnelRuntimeIssueForConfig(tunnel.TunnelID, plan.snapshot.Version, reason)
		}
		return fmt.Errorf("%s: %w", reason, plan.bindErr)
	}
	for tunnelID, reason := range plan.conflictIssues {
		s.recordTunnelRuntimeIssueForConfig(tunnelID, plan.snapshot.Version, reason)
	}
	if len(plan.targetTunnels) == 0 {
		if plan.activeRuntime {
			target.session.setRecoveryMode(testsupport.RecoveryModeRunning)
		}
		return nil
	}

	for _, tunnel := range plan.targetTunnels {
		if reason := strings.TrimSpace(plan.conflictIssues[tunnel.TunnelID]); reason != "" {
			target.logger.Warn(
				"skip tunnel listener start because runtime conflict was detected",
				"tunnel_id", tunnel.TunnelID,
				"group_id", plan.group.ID,
				"reason", reason,
			)
			continue
		}
		opCtx := newTunnelRuntimeStartContext(plan.group.ID, plan.snapshot.Version, tunnel, plan.bindIP)
		started, startErr := s.startTunnelListeners(opCtx)
		if startErr != nil {
			s.recordTunnelRuntimeIssueForConfig(tunnel.TunnelID, plan.snapshot.Version, startErr.Error())
			target.logger.Warn(
				"tunnel listener start failed",
				"tunnel_id", tunnel.TunnelID,
				"group_id", plan.group.ID,
				"error", startErr,
			)
			continue
		}
		s.recordTunnelRuntimeIssueForConfig(tunnel.TunnelID, plan.snapshot.Version, "")
		startUDPCleanup, attached := target.session.attachTunnelListeners(plan.snapshot.Version, tunnel.TunnelID, started.tcpListeners, started.udpListeners)
		if !attached {
			closeStartedTunnelListeners(started.tcpListeners, started.udpListeners)
			return nil
		}
		if s.isShuttingDown() || target.session.isDone() {
			s.shutdownSession(target.session)
			return nil
		}
		if startUDPCleanup {
			go s.serveUDPIdleCleanup(target.conn, target.logger, target.session)
		}
		for _, runtime := range started.tcpRuntimes {
			serve := opCtx.serveContext(s, target.conn, target.logger, target.session, runtime.remotePort)
			target.logger.Info(
				"tcp tunnel listener ready",
				"tunnel_id", runtime.tunnel.TunnelID,
				"remote_port", runtime.remotePort,
				"addr", runtime.listener.Addr().String(),
			)
			go s.serveTunnelListener(serve, runtime.listener)
		}
		for _, runtime := range started.udpRuntimes {
			serve := opCtx.serveContext(s, target.conn, target.logger, target.session, runtime.remotePort)
			target.logger.Info(
				"udp tunnel listener ready",
				"tunnel_id", runtime.tunnel.TunnelID,
				"remote_port", runtime.remotePort,
				"addr", runtime.listener.LocalAddr().String(),
			)
			go s.serveUDPTunnelListener(serve, runtime.listener)
		}
	}
	for tunnelID := range target.session.activeRuntimeTunnelIDs() {
		s.recordTunnelRuntimeIssueForConfig(tunnelID, plan.snapshot.Version, "")
	}
	if target.session.hasActiveRuntimeListeners() {
		target.session.setRecoveryMode(testsupport.RecoveryModeRunning)
	}
	return nil
}

func collectRuntimeIssueClearTunnelIDs(tunnels []protocol.TunnelEntry, activeTunnelIDs map[uint32]struct{}) []uint32 {
	clearIDs := make([]uint32, 0, len(tunnels))
	for _, tunnel := range tunnels {
		if tunnel.TunnelFlags&protocol.TunnelFlagEnabled == 0 {
			clearIDs = append(clearIDs, tunnel.TunnelID)
			continue
		}
		if _, active := activeTunnelIDs[tunnel.TunnelID]; active {
			clearIDs = append(clearIDs, tunnel.TunnelID)
		}
	}
	return clearIDs
}

func (s *Server) detectRuntimePortConflictIssues(group GroupRuntime, bindIP string, tunnels []protocol.TunnelEntry) map[uint32]string {
	claims, owners, targetOrder := buildRuntimeClaims(group, tunnels, bindIP)
	if len(targetOrder) == 0 {
		return nil
	}

	for _, active := range s.activeRuntimeGroups(nil) {
		otherBindIP, ok := normalizeRuntimeListenIP(active.group.EffectiveIP)
		if !ok {
			continue
		}
		otherClaims, otherOwners, _ := buildRuntimeClaims(active.group, active.snapshot.Tunnels, otherBindIP)
		claims = append(claims, otherClaims...)
		for ownerID, owner := range otherOwners {
			owners[ownerID] = owner
		}
	}

	conflicts := ports.DetectConflicts(claims)
	if len(conflicts) == 0 {
		return nil
	}

	issues := make(map[uint32]string)
	for _, tunnelID := range targetOrder {
		conflict, ok := conflicts[int64(tunnelID)]
		if !ok {
			continue
		}
		target, ok := owners[int64(tunnelID)]
		if !ok {
			continue
		}
		other, ok := owners[conflict.OtherOwnerID]
		if !ok {
			continue
		}
		reason := buildRuntimeConflictReason(target, other, conflict)
		issues[tunnelID] = reason
	}
	return issues
}

func buildRuntimeClaims(group GroupRuntime, tunnels []protocol.TunnelEntry, bindIP string) ([]ports.Claim, map[int64]runtimeClaimOwner, []uint32) {
	claims := make([]ports.Claim, 0, len(tunnels))
	owners := make(map[int64]runtimeClaimOwner)
	targetOrder := make([]uint32, 0, len(tunnels))
	for _, tunnel := range tunnels {
		if tunnel.TunnelFlags&protocol.TunnelFlagEnabled == 0 {
			continue
		}
		ownerID := int64(tunnel.TunnelID)
		claims = append(claims, ports.Claim{
			OwnerID:     ownerID,
			Protocol:    protocolName(tunnel.Protocol),
			EffectiveIP: bindIP,
			PortStart:   int64(tunnel.RemoteStart),
			PortEnd:     int64(tunnel.RemoteEnd),
		})
		owners[ownerID] = runtimeClaimOwner{
			GroupID:     group.ID,
			GroupName:   group.Name,
			TunnelID:    tunnel.TunnelID,
			EffectiveIP: bindIP,
		}
		targetOrder = append(targetOrder, tunnel.TunnelID)
	}
	return claims, owners, targetOrder
}

func (s *Server) startTunnelListeners(opCtx tunnelListenerOperationContext) (tunnelListenerBatch, error) {
	started := tunnelListenerBatch{}
	switch opCtx.tunnel.Protocol {
	case protocol.ProtocolTCP:
		tlsConfig, err := controlbind.LoadTunnelListenerTLSConfig(context.Background(), s.options.Store, opCtx.tunnel.TunnelID)
		if err != nil {
			return tunnelListenerBatch{}, opCtx.listenerStartError(opCtx.tunnel.RemoteStart, err)
		}
		started.tcpListeners = make([]net.Listener, 0, opCtx.remotePortCount())
		started.tcpRuntimes = make([]tcpTunnelListener, 0, opCtx.remotePortCount())
		for remotePort := int(opCtx.tunnel.RemoteStart); remotePort <= int(opCtx.tunnel.RemoteEnd); remotePort++ {
			bind := opCtx.listenerBind(uint16(remotePort))
			listener, err := s.listenTCP(context.Background(), bind)
			if err != nil {
				closeStartedTunnelListeners(started.tcpListeners, started.udpListeners)
				return tunnelListenerBatch{}, opCtx.listenerStartError(uint16(remotePort), err)
			}
			if tlsConfig != nil {
				listener = tls.NewListener(listener, tlsConfig.Clone())
			}
			started.tcpListeners = append(started.tcpListeners, listener)
			started.tcpRuntimes = append(started.tcpRuntimes, tcpTunnelListener{
				configVersion: opCtx.configVersion,
				tunnel:        opCtx.tunnel,
				remotePort:    uint16(remotePort),
				listener:      listener,
			})
		}
	case protocol.ProtocolUDP:
		started.udpListeners = make([]UDPListener, 0, opCtx.remotePortCount())
		started.udpRuntimes = make([]udpTunnelListener, 0, opCtx.remotePortCount())
		for remotePort := int(opCtx.tunnel.RemoteStart); remotePort <= int(opCtx.tunnel.RemoteEnd); remotePort++ {
			bind := opCtx.listenerBind(uint16(remotePort))
			udpAddr, err := s.resolveUDPAddr(context.Background(), bind)
			if err != nil {
				closeStartedTunnelListeners(started.tcpListeners, started.udpListeners)
				return tunnelListenerBatch{}, opCtx.listenerStartError(uint16(remotePort), err)
			}
			listener, err := s.listenUDP(context.Background(), bind, udpAddr)
			if err != nil {
				closeStartedTunnelListeners(started.tcpListeners, started.udpListeners)
				return tunnelListenerBatch{}, opCtx.listenerStartError(uint16(remotePort), err)
			}
			started.udpListeners = append(started.udpListeners, listener)
			started.udpRuntimes = append(started.udpRuntimes, udpTunnelListener{
				configVersion: opCtx.configVersion,
				tunnel:        opCtx.tunnel,
				remotePort:    uint16(remotePort),
				listener:      listener,
			})
		}
	}
	return started, nil
}

func selectNonListeningEnabledTunnels(tunnels []protocol.TunnelEntry, activeTunnelIDs map[uint32]struct{}) []protocol.TunnelEntry {
	selected := make([]protocol.TunnelEntry, 0, len(tunnels))
	for _, tunnel := range tunnels {
		if tunnel.TunnelFlags&protocol.TunnelFlagEnabled == 0 {
			continue
		}
		if _, active := activeTunnelIDs[tunnel.TunnelID]; active {
			continue
		}
		selected = append(selected, tunnel)
	}
	return selected
}

func filterTunnelsByID(tunnels []protocol.TunnelEntry, activeTunnelIDs map[uint32]struct{}) []protocol.TunnelEntry {
	if len(activeTunnelIDs) == 0 {
		return nil
	}
	filtered := make([]protocol.TunnelEntry, 0, len(tunnels))
	for _, tunnel := range tunnels {
		if _, ok := activeTunnelIDs[tunnel.TunnelID]; !ok {
			continue
		}
		filtered = append(filtered, tunnel)
	}
	return filtered
}

func (s *Server) resolveGroupEffectiveIP(group GroupRuntime) (string, error) {
	effectiveIP, err := system.NormalizeListenIP(group.EffectiveIP)
	if err != nil {
		return "", &groupEffectiveIPStartError{
			EffectiveIP: group.EffectiveIP,
			Kind:        groupEffectiveIPStartErrorInvalid,
			Cause:       err,
		}
	}
	if system.IsSpecialListenIP(effectiveIP) {
		return effectiveIP, nil
	}
	if s.network != nil && !s.network.Current().HasIP(effectiveIP) {
		return "", &groupEffectiveIPStartError{
			EffectiveIP: effectiveIP,
			Kind:        groupEffectiveIPStartErrorNotLocal,
		}
	}
	return effectiveIP, nil
}

func closeStartedTunnelListeners(tcpListeners []net.Listener, udpListeners []UDPListener) {
	for _, listener := range tcpListeners {
		testhooks.Point(
			"control.listener.before_close",
			testhooks.F("protocol", "tcp"),
			testhooks.F("addr", listener.Addr().String()),
		)
		_ = listener.Close()
		testhooks.Point(
			"control.listener.after_close",
			testhooks.F("protocol", "tcp"),
			testhooks.F("addr", listener.Addr().String()),
		)
	}
	for _, listener := range udpListeners {
		testhooks.Point(
			"control.listener.before_close",
			testhooks.F("protocol", "udp"),
			testhooks.F("addr", listener.LocalAddr().String()),
		)
		_ = listener.Close()
		testhooks.Point(
			"control.listener.after_close",
			testhooks.F("protocol", "udp"),
			testhooks.F("addr", listener.LocalAddr().String()),
		)
	}
}

func protocolName(value uint8) string {
	switch value {
	case protocol.ProtocolTCP:
		return "tcp"
	case protocol.ProtocolUDP:
		return "udp"
	default:
		return fmt.Sprintf("protocol(%d)", value)
	}
}

func normalizeRuntimeListenIP(raw string) (string, bool) {
	normalized, err := system.NormalizeListenIP(raw)
	if err != nil {
		return "", false
	}
	return normalized, true
}

func buildRuntimeConflictReason(target, other runtimeClaimOwner, conflict ports.Conflict) string {
	return fmt.Sprintf(
		`与分组"%s"的 tunnel_id=%d 在 %s (%s) %s 上冲突，无法启动监听`,
		other.GroupName,
		other.TunnelID,
		strings.ToUpper(conflict.Protocol),
		formatConflictEffectiveIPs(conflict.OwnerEffectiveIP, conflict.OtherEffectiveIP),
		formatConflictPortRange(conflict.ConflictStart, conflict.ConflictEnd),
	)
}

func buildTunnelListenerStartReason(protocolValue uint8, effectiveIP string, remotePort uint16, cause error) string {
	addr := net.JoinHostPort(effectiveIP, strconv.Itoa(int(remotePort)))
	if isListenPortConflictError(cause) {
		return fmt.Sprintf("%s 监听 %s 端口冲突，无法启动", strings.ToUpper(protocolName(protocolValue)), addr)
	}
	return fmt.Sprintf("%s 监听 %s 启动失败: %v", strings.ToUpper(protocolName(protocolValue)), addr, cause)
}

func isListenPortConflictError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, syscall.EADDRINUSE) {
		return true
	}
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "address already in use") ||
		strings.Contains(message, "only one usage of each socket address") ||
		strings.Contains(message, "10048")
}

func formatConflictEffectiveIPs(ownerIP, otherIP string) string {
	if strings.TrimSpace(ownerIP) == "" {
		return strings.TrimSpace(otherIP)
	}
	if strings.TrimSpace(otherIP) == "" || ownerIP == otherIP {
		return ownerIP
	}
	return ownerIP + " <-> " + otherIP
}

func formatConflictPortRange(start, end int64) string {
	if start == end {
		return strconv.FormatInt(start, 10)
	}
	return fmt.Sprintf("%d-%d", start, end)
}

func (s *Server) serveTunnelListener(serve tunnelRuntimeServeContext, listener net.Listener) {
	for {
		publicConn, err := listener.Accept()
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return
			}
			serve.logger.Warn("tcp tunnel accept failed", "tunnel_id", serve.tunnel.TunnelID, "error", err)
			continue
		}

		go s.handlePublicConnection(serve, publicConn)
	}
}

func (s *Server) serveUDPTunnelListener(serve tunnelRuntimeServeContext, listener UDPListener) {
	buffer := make([]byte, protocol.MaxDataBodyLen)
	for {
		n, clientAddr, err := listener.ReadFromUDP(buffer)
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return
			}
			serve.logger.Warn("udp tunnel read failed", "tunnel_id", serve.tunnel.TunnelID, "error", err)
			continue
		}
		payload := append([]byte(nil), buffer[:n]...)
		if err := s.handlePublicUDPDatagram(serve, listener, clientAddr, payload); err != nil {
			serve.logger.Warn(
				"udp tunnel forward failed",
				"tunnel_id", serve.tunnel.TunnelID,
				"remote_port", serve.remotePort,
				"client_addr", clientAddr.String(),
				"error", err,
			)
		}
	}
}

type runtimeSnapshotIndex struct {
	sessions        []runtimeSessionTarget
	sessionsByID    map[runtimeSessionTargetID]runtimeSessionTarget
	sessionsByGroup map[int64]runtimeSessionTarget
}

type runtimeSessionTargetID struct {
	GroupID   int64
	SessionID uint64
}

type runtimeTunnelTargetID struct {
	GroupID   int64
	SessionID uint64
	TunnelID  uint32
}

type runtimeConnectionTargetID struct {
	GroupID      int64
	SessionID    uint64
	ConnectionID uint32
	Kind         string
}

type runtimePendingConfigTarget struct {
	requestID   uint32
	snapshot    ConfigSnapshot
	effectiveIP string
}

type runtimeSessionTarget struct {
	id                     runtimeSessionTargetID
	conn                   net.Conn
	connID                 string
	effectiveIP            string
	snapshot               ConfigSnapshot
	lastAckedConfigVersion uint64
	pending                *runtimePendingConfigTarget
	recoveryMode           testsupport.RecoveryMode
	runtimeFrozen          bool
	listenersStarted       bool
	runtimeGeneration      uint64
	activeStreamCount      uint32
	activeUDPSessionCount  uint32
	activeTunnelIDs        map[uint32]struct{}
	listeners              []runtimeListenerTarget
	listenersByTunnel      map[uint32][]runtimeListenerTarget
	missingListeners       []runtimeMissingListenerTarget
	missingByTunnel        map[uint32]runtimeMissingListenerTarget
	connections            []runtimeConnectionTarget
}

type runtimeListenerTarget struct {
	id            runtimeTunnelTargetID
	protocol      string
	bindIP        string
	port          uint16
	configVersion uint64
	kind          string
}

type runtimeMissingListenerTarget struct {
	id           runtimeTunnelTargetID
	protocol     string
	missingPorts []uint16
}

type runtimeConnectionTarget struct {
	id             runtimeConnectionTargetID
	protocol       string
	tunnelID       uint32
	remotePort     uint16
	clientAddr     string
	openedAtMs     uint64
	lastActiveAtMs uint64
	idleTimeoutMs  uint32
}

type runtimeTunnelTarget struct {
	id             runtimeTunnelTargetID
	tunnel         protocol.TunnelEntry
	staticConflict bool
	runtimeIssue   string
	runtimeKind    string
	finalStatus    string
	finalReason    string
	listeners      []runtimeListenerTarget
	missingPorts   []uint16
}

type runtimeObservedConfig struct {
	effectiveIP            string
	snapshot               ConfigSnapshot
	lastAckedConfigVersion uint64
	pending                *runtimePendingConfigTarget
}

func newRuntimeSnapshotIndex(snapshot supervisorSnapshot) runtimeSnapshotIndex {
	index := runtimeSnapshotIndex{
		sessions:        make([]runtimeSessionTarget, 0, len(snapshot.sessions)),
		sessionsByID:    make(map[runtimeSessionTargetID]runtimeSessionTarget, len(snapshot.sessions)),
		sessionsByGroup: make(map[int64]runtimeSessionTarget, len(snapshot.sessions)),
	}
	for _, sessionSnapshot := range snapshot.sessions {
		target := newRuntimeSessionTarget(sessionSnapshot)
		if target.id.GroupID == 0 || target.id.SessionID == 0 {
			continue
		}
		index.sessions = append(index.sessions, target)
		index.sessionsByID[target.id] = target
		index.sessionsByGroup[target.id.GroupID] = target
	}
	return index
}

func newRuntimeSessionTarget(snapshot runtimeSessionSnapshot) runtimeSessionTarget {
	config := buildRuntimeObservedConfig(snapshot.state, snapshot.desiredGroup)
	target := runtimeSessionTarget{
		id: runtimeSessionTargetID{
			GroupID:   snapshot.groupID,
			SessionID: snapshot.sessionID,
		},
		conn:                   snapshot.conn,
		connID:                 transport.ConnectionID(snapshot.conn),
		effectiveIP:            config.effectiveIP,
		snapshot:               config.snapshot,
		lastAckedConfigVersion: config.lastAckedConfigVersion,
		pending:                config.pending,
		recoveryMode:           snapshot.recoveryMode,
		runtimeFrozen:          snapshot.runtime.frozen,
		listenersStarted:       snapshot.runtime.listenersStarted,
		runtimeGeneration:      snapshot.runtime.generation,
		activeStreamCount:      snapshot.runtime.activeStreamCount,
		activeUDPSessionCount:  snapshot.runtime.activeUDPSessionCount,
		activeTunnelIDs:        snapshot.runtime.activeTunnelIDs,
		listenersByTunnel:      make(map[uint32][]runtimeListenerTarget),
		missingByTunnel:        make(map[uint32]runtimeMissingListenerTarget),
		connections:            make([]runtimeConnectionTarget, 0, len(snapshot.runtime.connections)),
	}

	for _, attached := range snapshot.runtime.attachedListeners {
		listener := runtimeListenerTarget{
			id: runtimeTunnelTargetID{
				GroupID:   target.id.GroupID,
				SessionID: snapshot.sessionID,
				TunnelID:  attached.tunnelID,
			},
			protocol:      attached.protocol,
			bindIP:        attached.bindIP,
			port:          attached.port,
			configVersion: snapshot.runtime.generation,
			kind:          attached.protocol,
		}
		target.listeners = append(target.listeners, listener)
		target.listenersByTunnel[attached.tunnelID] = append(target.listenersByTunnel[attached.tunnelID], listener)
	}

	for _, connection := range snapshot.runtime.connections {
		target.connections = append(target.connections, runtimeConnectionTarget{
			id: runtimeConnectionTargetID{
				GroupID:      target.id.GroupID,
				SessionID:    snapshot.sessionID,
				ConnectionID: connection.connectionID,
				Kind:         connection.kind,
			},
			protocol:       connection.protocol,
			tunnelID:       connection.tunnelID,
			remotePort:     connection.remotePort,
			clientAddr:     connection.clientAddr,
			openedAtMs:     connection.openedAtMs,
			lastActiveAtMs: connection.lastActiveAtMs,
			idleTimeoutMs:  connection.idleTimeoutMs,
		})
	}

	target.missingListeners = buildRuntimeMissingListenerTargets(target.id, target.snapshot.Tunnels, target.listenersByTunnel)
	for _, missing := range target.missingListeners {
		target.missingByTunnel[missing.id.TunnelID] = missing
	}

	return target
}

func buildRuntimeObservedConfig(state controlsession.SessionState, desiredGroup GroupRuntime) runtimeObservedConfig {
	config := runtimeObservedConfig{
		effectiveIP: desiredGroup.EffectiveIP,
		snapshot:    desiredGroup.Snapshot,
	}

	if state.Applied != nil {
		config.snapshot = configSnapshotFromDesired(state.Applied.Snapshot)
		config.effectiveIP = state.Applied.Snapshot.EffectiveIP
		config.lastAckedConfigVersion = state.Applied.Snapshot.Version
	} else if state.Desired != nil {
		config.snapshot = configSnapshotFromDesired(*state.Desired)
		config.effectiveIP = state.Desired.EffectiveIP
	}

	if state.Pending != nil {
		config.pending = &runtimePendingConfigTarget{
			requestID:   state.Pending.RequestID,
			snapshot:    configSnapshotFromDesired(state.Pending.Snapshot),
			effectiveIP: state.Pending.Snapshot.EffectiveIP,
		}
	}

	return config
}

func buildRuntimeMissingListenerTargets(id runtimeSessionTargetID, tunnels []protocol.TunnelEntry, listenersByTunnel map[uint32][]runtimeListenerTarget) []runtimeMissingListenerTarget {
	missing := make([]runtimeMissingListenerTarget, 0)
	for _, tunnel := range tunnels {
		if tunnel.TunnelFlags&protocol.TunnelFlagEnabled == 0 {
			continue
		}

		actualPorts := make(map[uint16]struct{}, len(listenersByTunnel[tunnel.TunnelID]))
		for _, listener := range listenersByTunnel[tunnel.TunnelID] {
			actualPorts[listener.port] = struct{}{}
		}

		missingPorts := make([]uint16, 0)
		for port := tunnel.RemoteStart; port <= tunnel.RemoteEnd; port++ {
			if _, ok := actualPorts[port]; ok {
				if port == tunnel.RemoteEnd {
					break
				}
				continue
			}
			missingPorts = append(missingPorts, port)
			if port == tunnel.RemoteEnd {
				break
			}
		}
		if len(missingPorts) == 0 {
			continue
		}

		missing = append(missing, runtimeMissingListenerTarget{
			id: runtimeTunnelTargetID{
				GroupID:   id.GroupID,
				SessionID: id.SessionID,
				TunnelID:  tunnel.TunnelID,
			},
			protocol:     protocolName(tunnel.Protocol),
			missingPorts: missingPorts,
		})
	}
	return missing
}

func (s *Server) runtimeSnapshotIndex() runtimeSnapshotIndex {
	if s == nil || s.supervisor == nil {
		return runtimeSnapshotIndex{}
	}
	return newRuntimeSnapshotIndex(s.supervisor.Snapshot(nil))
}

func (s runtimeSnapshotIndex) sessionsList() []runtimeSessionTarget {
	return s.sessions
}

func (s runtimeSnapshotIndex) session(groupID int64) (runtimeSessionTarget, bool) {
	target, ok := s.sessionsByGroup[groupID]
	return target, ok
}

func (s runtimeSnapshotIndex) sessionByID(id runtimeSessionTargetID) (runtimeSessionTarget, bool) {
	target, ok := s.sessionsByID[id]
	return target, ok
}

func (s runtimeSnapshotIndex) selectNonListeningEnabledTunnels(group GroupRuntime) []protocol.TunnelEntry {
	if !group.Enabled {
		return nil
	}

	session, ok := s.session(group.ID)
	if !ok {
		return enabledTunnels(group.Snapshot)
	}
	return selectNonListeningEnabledTunnels(group.Snapshot.Tunnels, session.activeTunnelIDs)
}

func (s runtimeSnapshotIndex) selectTunnels(groups []GroupRuntime, runtimeIssues map[int64]string, staticConflictIDs map[int64]struct{}) []runtimeTunnelTarget {
	targets := make([]runtimeTunnelTarget, 0)
	for _, group := range groups {
		session, hasSession := s.session(group.ID)
		for _, tunnel := range group.Snapshot.Tunnels {
			targetID := runtimeTunnelTargetID{
				GroupID:  group.ID,
				TunnelID: tunnel.TunnelID,
			}
			if hasSession {
				targetID.SessionID = session.id.SessionID
			}

			runtimeReason := strings.TrimSpace(runtimeIssues[int64(tunnel.TunnelID)])
			staticConflict := false
			if _, ok := staticConflictIDs[int64(tunnel.TunnelID)]; ok {
				staticConflict = true
			}

			target := runtimeTunnelTarget{
				id:             targetID,
				tunnel:         tunnel,
				staticConflict: staticConflict,
				runtimeIssue:   runtimeReason,
				runtimeKind:    runtimeIssueKind(runtimeReason),
			}
			target.finalStatus, target.finalReason = observedTunnelStatus(tunnel, staticConflict, runtimeReason)

			if hasSession {
				target.listeners = append(target.listeners, session.listenersByTunnel[tunnel.TunnelID]...)
				if missing, ok := session.missingByTunnel[tunnel.TunnelID]; ok {
					target.missingPorts = append(target.missingPorts, missing.missingPorts...)
				}
			}

			targets = append(targets, target)
		}
	}
	return targets
}

func (t runtimeSessionTarget) hasPendingConfig() bool {
	return t.pending != nil && t.pending.requestID != 0
}

func (t runtimeSessionTarget) observedState() testsupport.SessionObservedState {
	observed := testsupport.SessionObservedState{
		GroupID:                t.id.GroupID,
		SessionID:              t.id.SessionID,
		ConnID:                 t.connID,
		EffectiveIP:            t.effectiveIP,
		SnapshotVersion:        t.snapshot.Version,
		SnapshotTunnelCount:    len(t.snapshot.Tunnels),
		LastAckedConfigVersion: t.lastAckedConfigVersion,
		RuntimeFrozen:          t.runtimeFrozen,
		ListenersStarted:       t.listenersStarted,
		RuntimeGeneration:      t.runtimeGeneration,
		RecoveryMode:           t.recoveryMode,
		ActiveStreams:          t.activeStreamCount,
		ActiveUDPSessions:      t.activeUDPSessionCount,
	}
	if t.pending != nil {
		observed.Pending = &testsupport.PendingConfigObservedState{
			RequestID:   t.pending.requestID,
			Version:     t.pending.snapshot.Version,
			TunnelCount: len(t.pending.snapshot.Tunnels),
			EffectiveIP: t.pending.effectiveIP,
		}
	}
	return observed
}

func (t runtimeSessionTarget) observedListeners() []testsupport.AttachedListenerObservedState {
	listeners := make([]testsupport.AttachedListenerObservedState, 0, len(t.listeners))
	for _, attached := range t.listeners {
		listeners = append(listeners, testsupport.AttachedListenerObservedState{
			GroupID:       attached.id.GroupID,
			SessionID:     attached.id.SessionID,
			TunnelID:      attached.id.TunnelID,
			Protocol:      attached.protocol,
			BindIP:        attached.bindIP,
			Port:          attached.port,
			ConfigVersion: attached.configVersion,
			Kind:          attached.kind,
		})
	}
	return listeners
}

func (t runtimeSessionTarget) observedMissingListeners() []testsupport.MissingListenerObservedState {
	missing := make([]testsupport.MissingListenerObservedState, 0, len(t.missingListeners))
	for _, listener := range t.missingListeners {
		ports := append([]uint16(nil), listener.missingPorts...)
		missing = append(missing, testsupport.MissingListenerObservedState{
			GroupID:      listener.id.GroupID,
			SessionID:    listener.id.SessionID,
			TunnelID:     listener.id.TunnelID,
			Protocol:     listener.protocol,
			MissingPorts: ports,
		})
	}
	return missing
}

func (t runtimeSessionTarget) observedConnections() []testsupport.ConnectionObservedState {
	connections := make([]testsupport.ConnectionObservedState, 0, len(t.connections))
	for _, connection := range t.connections {
		connections = append(connections, testsupport.ConnectionObservedState{
			GroupID:        connection.id.GroupID,
			SessionID:      connection.id.SessionID,
			ConnectionID:   connection.id.ConnectionID,
			Kind:           connection.id.Kind,
			Protocol:       connection.protocol,
			TunnelID:       connection.tunnelID,
			RemotePort:     connection.remotePort,
			ClientAddr:     connection.clientAddr,
			OpenedAtMs:     connection.openedAtMs,
			LastActiveAtMs: connection.lastActiveAtMs,
			IdleTimeoutMs:  connection.idleTimeoutMs,
		})
	}
	return connections
}

func (t runtimeTunnelTarget) observedState() testsupport.TunnelObservedState {
	return testsupport.TunnelObservedState{
		GroupID:        t.id.GroupID,
		TunnelID:       t.id.TunnelID,
		StaticConflict: t.staticConflict,
		RuntimeIssue:   t.runtimeIssue,
		RuntimeKind:    t.runtimeKind,
		FinalStatus:    t.finalStatus,
		FinalReason:    t.finalReason,
	}
}

func (s *Server) startRuntimeIssuePolling(parent context.Context) {
	if s == nil || s.isShuttingDown() {
		return
	}

	pollCtx, cancel := context.WithCancel(parent)

	s.mu.Lock()
	if s.isShuttingDown() {
		s.mu.Unlock()
		cancel()
		return
	}
	s.runtimeScanCancel = cancel
	s.mu.Unlock()

	task := s.scheduler.Every(pollCtx, "control.runtime_scan_poll", s.options.RuntimeScanPoll, func(ctx context.Context, _ time.Time) {
		if err := s.scanNonListeningTunnelRuntimeIssues(ctx); err != nil && !errors.Is(err, context.Canceled) {
			s.logger.Warn("scan non-listening tunnel runtime issues failed", "error", err)
		}
	})
	s.scanWG.Add(1)
	go func() {
		defer s.scanWG.Done()
		<-task.Done()
	}()
}

func (s *Server) scanNonListeningTunnelRuntimeIssues(ctx context.Context) error {
	if s == nil {
		return nil
	}
	if !s.beginRuntimeScanRound() {
		testhooks.Point("runtime.scan.skip_overlap")
		return nil
	}
	defer s.finishRuntimeScanRound()
	if s.repo == nil {
		return errors.New("repository not configured")
	}

	testhooks.Point("runtime.scan.before_round")
	groups, err := s.repo.ListGroupRuntimes(ctx)
	if err != nil {
		return err
	}

	knownTunnelIDs := collectKnownTunnelIDs(groups)
	s.clearUnknownTunnelRuntimeIssues(knownTunnelIDs)

	staticConflictIDs := detectConfiguredConflictTunnelIDs(groups)
	viewIndex := s.runtimeSnapshotIndex()

	for _, group := range groups {
		targetTunnels := viewIndex.selectNonListeningEnabledTunnels(group)
		issues := s.scanGroupRuntimeIssues(group, staticConflictIDs, targetTunnels)
		preserveHealthyIssues := s.preserveScannedHealthyRuntimeIssuesUntilRecovery(viewIndex, group, targetTunnels, staticConflictIDs, issues)
		s.applyScannedTunnelRuntimeIssues(group.Snapshot, staticConflictIDs, issues, preserveHealthyIssues)
		testhooks.Point(
			"runtime.scan.before_group_recover",
			testhooks.F("group_id", group.ID),
			testhooks.F("target_tunnel_count", len(targetTunnels)),
		)
		if err := s.recoverScannedActiveSessionTunnels(viewIndex, group, targetTunnels, staticConflictIDs, issues); err != nil {
			s.logger.Warn("recover scanned non-listening tunnels failed", "group_id", group.ID, "error", err)
		}
	}

	testhooks.Point("runtime.scan.after_round", testhooks.F("group_count", len(groups)))
	return nil
}

func (s *Server) beginRuntimeScanRound() bool {
	if s == nil {
		return false
	}

	s.runtimeScanStateMu.Lock()
	defer s.runtimeScanStateMu.Unlock()
	if s.runtimeScanInFlight {
		return false
	}
	s.runtimeScanInFlight = true
	return true
}

func (s *Server) finishRuntimeScanRound() {
	if s == nil {
		return
	}

	s.runtimeScanStateMu.Lock()
	s.runtimeScanInFlight = false
	s.runtimeScanStateMu.Unlock()
}

func (s *Server) scanGroupRuntimeIssues(group GroupRuntime, staticConflictIDs map[int64]struct{}, targetTunnels []protocol.TunnelEntry) map[uint32]string {
	if !group.Enabled {
		return nil
	}

	if len(targetTunnels) == 0 {
		return nil
	}

	bindIP, err := s.resolveGroupEffectiveIP(group)
	if err != nil {
		reason := buildGroupEffectiveIPRuntimeReason(group, err)
		issues := make(map[uint32]string, len(targetTunnels))
		for _, tunnel := range targetTunnels {
			if _, conflicted := staticConflictIDs[int64(tunnel.TunnelID)]; conflicted {
				continue
			}
			issues[tunnel.TunnelID] = reason
		}
		return issues
	}

	issues := s.detectRuntimePortConflictIssues(group, bindIP, targetTunnels)
	if len(issues) == 0 {
		issues = make(map[uint32]string)
	}

	for _, tunnel := range targetTunnels {
		if _, conflicted := staticConflictIDs[int64(tunnel.TunnelID)]; conflicted {
			continue
		}
		if strings.TrimSpace(issues[tunnel.TunnelID]) != "" {
			continue
		}
		if reason := s.probeTunnelRuntimeIssue(group.ID, bindIP, tunnel); reason != "" {
			issues[tunnel.TunnelID] = reason
		}
	}

	if len(issues) == 0 {
		return nil
	}
	return issues
}

func (s *Server) recoverScannedActiveSessionTunnels(viewIndex runtimeSnapshotIndex, group GroupRuntime, targetTunnels []protocol.TunnelEntry, staticConflictIDs map[int64]struct{}, issues map[uint32]string) error {
	if s == nil || group.ID <= 0 || len(targetTunnels) == 0 {
		return nil
	}
	active, ok := s.activeSession(group.ID)
	if !ok || active == nil || active.session == nil {
		return nil
	}

	target, ok := viewIndex.session(group.ID)
	if !ok || target.hasPendingConfig() || s.isShuttingDown() {
		return nil
	}

	if target.effectiveIP != group.EffectiveIP {
		return nil
	}

	logger := s.logger.With(
		"session_id", active.session.ID,
		"group_id", group.ID,
		"group_name", group.Name,
	)

	if sameRuntimeSnapshot(target.snapshot, group.Snapshot) {
		if !hasRecoverableScannedTunnels(targetTunnels, staticConflictIDs, issues) {
			return nil
		}
		logger.Info(
			"requesting active session listener recovery after runtime prerequisites returned",
			"config_version", group.Snapshot.Version,
			"tunnel_count", len(group.Snapshot.Tunnels),
		)
		return s.requestAuditedSessionRuntimeRecovery(target.id.SessionID, targetTunnels)
	}

	if !shouldRecoverScannedActiveSessionConfig(target.snapshot, group.Snapshot) {
		return nil
	}
	if _, err := s.resolveGroupEffectiveIP(group); err != nil {
		return nil
	}

	logger.Info(
		"recovering active session config after runtime prerequisites returned",
		"config_version", group.Snapshot.Version,
		"tunnel_count", len(group.Snapshot.Tunnels),
	)
	if runtime := s.runtimeExecutor(active.session.ID); runtime != nil {
		runtime.setDesiredGroup(group)
	}
	s.supervisor.UpdateDesiredRuntime(group.ID, desiredRuntimeFromGroup(group))
	return nil
}

func shouldRecoverScannedActiveSessionConfig(currentSnapshot, nextSnapshot ConfigSnapshot) bool {
	if sameRuntimeSnapshot(currentSnapshot, nextSnapshot) {
		return false
	}
	if len(currentSnapshot.Tunnels) != 0 {
		return false
	}
	return len(nextSnapshot.Tunnels) > 0
}

func hasRecoverableScannedTunnels(targetTunnels []protocol.TunnelEntry, staticConflictIDs map[int64]struct{}, issues map[uint32]string) bool {
	for _, tunnel := range targetTunnels {
		if _, conflicted := staticConflictIDs[int64(tunnel.TunnelID)]; conflicted {
			continue
		}
		if strings.TrimSpace(issues[tunnel.TunnelID]) != "" {
			continue
		}
		return true
	}
	return false
}

func (s *Server) requestAuditedSessionRuntimeRecovery(sessionID uint64, targetTunnels []protocol.TunnelEntry) error {
	if s == nil || s.supervisor == nil || sessionID == 0 || len(targetTunnels) == 0 {
		return nil
	}

	state, ok := s.supervisor.SessionState(sessionID)
	if !ok {
		return nil
	}

	targetTunnelIDs := make(map[uint32]struct{}, len(targetTunnels))
	for _, tunnel := range targetTunnels {
		targetTunnelIDs[tunnel.TunnelID] = struct{}{}
	}

	dispatched := false
	if state.RuntimePhase == controlsession.RuntimePhaseActive {
		for key := range state.Bindings {
			tunnelID := tunnelIDForBinding(state, key)
			if _, ok := targetTunnelIDs[tunnelID]; !ok {
				continue
			}
			dispatched = s.supervisor.DispatchBySessionID(sessionID, controlsession.BindingClosed{
				Key:    key,
				Reason: "runtime audit detected missing listener",
			}) || dispatched
		}
	}
	if dispatched {
		s.awaitAuditedSessionRecovery(sessionID, targetTunnelIDs)
		return nil
	}
	s.supervisor.DispatchBySessionID(sessionID, controlsession.ReconcileRequested{
		Reason: "runtime_audit_recover",
	})
	s.awaitAuditedSessionRecovery(sessionID, targetTunnelIDs)
	return nil
}

func (s *Server) awaitAuditedSessionRecovery(sessionID uint64, targetTunnelIDs map[uint32]struct{}) {
	if s == nil || len(targetTunnelIDs) == 0 {
		return
	}

	runtime := s.runtimeExecutor(sessionID)
	if runtime == nil || runtime.session == nil {
		return
	}

	deadline := time.Now().Add(200 * time.Millisecond)
	for time.Now().Before(deadline) {
		active := runtime.session.activeRuntimeTunnelIDs()
		recovered := true
		for tunnelID := range targetTunnelIDs {
			if _, ok := active[tunnelID]; !ok {
				recovered = false
				break
			}
		}
		if recovered {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func (s *Server) preserveScannedHealthyRuntimeIssuesUntilRecovery(viewIndex runtimeSnapshotIndex, group GroupRuntime, targetTunnels []protocol.TunnelEntry, staticConflictIDs map[int64]struct{}, issues map[uint32]string) map[uint32]struct{} {
	if s == nil || group.ID <= 0 || len(targetTunnels) == 0 {
		return nil
	}

	target, ok := viewIndex.session(group.ID)
	if !ok {
		return nil
	}
	if target.hasPendingConfig() {
		return preserveHealthyScannedTunnels(targetTunnels, staticConflictIDs, issues)
	}

	if target.effectiveIP != group.EffectiveIP {
		return nil
	}
	if !sameRuntimeSnapshot(target.snapshot, group.Snapshot) && !shouldRecoverScannedActiveSessionConfig(target.snapshot, group.Snapshot) {
		return nil
	}

	return preserveHealthyScannedTunnels(targetTunnels, staticConflictIDs, issues)
}

func preserveHealthyScannedTunnels(targetTunnels []protocol.TunnelEntry, staticConflictIDs map[int64]struct{}, issues map[uint32]string) map[uint32]struct{} {
	preserved := make(map[uint32]struct{})
	for _, tunnel := range targetTunnels {
		if _, conflicted := staticConflictIDs[int64(tunnel.TunnelID)]; conflicted {
			continue
		}
		if strings.TrimSpace(issues[tunnel.TunnelID]) != "" {
			continue
		}
		preserved[tunnel.TunnelID] = struct{}{}
	}
	if len(preserved) == 0 {
		return nil
	}
	return preserved
}

func detectConfiguredConflictTunnelIDs(groups []GroupRuntime) map[int64]struct{} {
	claims := make([]ports.Claim, 0)
	for _, group := range groups {
		if !group.Enabled {
			continue
		}
		for _, tunnel := range group.Snapshot.Tunnels {
			if tunnel.TunnelFlags&protocol.TunnelFlagEnabled == 0 {
				continue
			}
			claims = append(claims, ports.Claim{
				OwnerID:     int64(tunnel.TunnelID),
				Protocol:    protocolName(tunnel.Protocol),
				EffectiveIP: group.EffectiveIP,
				PortStart:   int64(tunnel.RemoteStart),
				PortEnd:     int64(tunnel.RemoteEnd),
			})
		}
	}

	conflicts := ports.DetectConflicts(claims)
	conflicted := make(map[int64]struct{}, len(conflicts))
	for tunnelID := range conflicts {
		conflicted[tunnelID] = struct{}{}
	}
	return conflicted
}

func collectKnownTunnelIDs(groups []GroupRuntime) map[int64]struct{} {
	known := make(map[int64]struct{})
	for _, group := range groups {
		for _, tunnel := range group.Snapshot.Tunnels {
			known[int64(tunnel.TunnelID)] = struct{}{}
		}
	}
	return known
}

func enabledTunnels(snapshot ConfigSnapshot) []protocol.TunnelEntry {
	enabled := make([]protocol.TunnelEntry, 0, len(snapshot.Tunnels))
	for _, tunnel := range snapshot.Tunnels {
		if tunnel.TunnelFlags&protocol.TunnelFlagEnabled == 0 {
			continue
		}
		enabled = append(enabled, tunnel)
	}
	return enabled
}

func buildGroupEffectiveIPRuntimeReason(group GroupRuntime, err error) string {
	var effectiveIPErr *groupEffectiveIPStartError
	if errors.As(err, &effectiveIPErr) {
		switch effectiveIPErr.Kind {
		case groupEffectiveIPStartErrorNotLocal:
			return fmt.Sprintf("生效 IP %q 当前不存在于本机，无法启动监听", group.EffectiveIP)
		case groupEffectiveIPStartErrorInvalid:
			return fmt.Sprintf("生效 IP %q 无效，无法启动监听", group.EffectiveIP)
		}
	}

	message := strings.ToLower(strings.TrimSpace(err.Error()))
	switch {
	case strings.Contains(message, "not a current local ip"):
		return fmt.Sprintf("生效 IP %q 当前不存在于本机，无法启动监听", group.EffectiveIP)
	case strings.Contains(message, "is invalid"):
		return fmt.Sprintf("生效 IP %q 无效，无法启动监听", group.EffectiveIP)
	default:
		return fmt.Sprintf("生效 IP %q 无法启动监听: %v", group.EffectiveIP, err)
	}
}

func buildInitialStartupRejectedReason(group GroupRuntime, err error) (string, bool) {
	var effectiveIPErr *groupEffectiveIPStartError
	if !errors.As(err, &effectiveIPErr) {
		return "", false
	}

	switch effectiveIPErr.Kind {
	case groupEffectiveIPStartErrorNotLocal:
		return fmt.Sprintf("生效 IP %q 当前不存在于本机，请联系管理员解决", group.EffectiveIP), true
	case groupEffectiveIPStartErrorInvalid:
		return fmt.Sprintf("生效 IP %q 无效，请联系管理员解决", group.EffectiveIP), true
	default:
		return "", false
	}
}

func (s *Server) probeTunnelRuntimeIssue(groupID int64, bindIP string, tunnel protocol.TunnelEntry) string {
	opCtx := newTunnelRuntimeProbeContext(groupID, tunnel, bindIP)
	switch tunnel.Protocol {
	case protocol.ProtocolTCP:
		listeners := make([]net.Listener, 0, opCtx.remotePortCount())
		for remotePort := int(opCtx.tunnel.RemoteStart); remotePort <= int(opCtx.tunnel.RemoteEnd); remotePort++ {
			bind := opCtx.listenerBind(uint16(remotePort))
			listener, err := s.listenTCP(context.Background(), bind)
			if err != nil {
				closeStartedTunnelListeners(listeners, nil)
				return buildTunnelListenerStartReason(opCtx.tunnel.Protocol, opCtx.bindIP, uint16(remotePort), err)
			}
			listeners = append(listeners, listener)
		}
		closeStartedTunnelListeners(listeners, nil)
	case protocol.ProtocolUDP:
		listeners := make([]UDPListener, 0, opCtx.remotePortCount())
		for remotePort := int(opCtx.tunnel.RemoteStart); remotePort <= int(opCtx.tunnel.RemoteEnd); remotePort++ {
			bind := opCtx.listenerBind(uint16(remotePort))
			udpAddr, err := s.resolveUDPAddr(context.Background(), bind)
			if err != nil {
				closeStartedTunnelListeners(nil, listeners)
				return buildTunnelListenerStartReason(opCtx.tunnel.Protocol, opCtx.bindIP, uint16(remotePort), err)
			}
			listener, err := s.listenUDP(context.Background(), bind, udpAddr)
			if err != nil {
				closeStartedTunnelListeners(nil, listeners)
				return buildTunnelListenerStartReason(opCtx.tunnel.Protocol, opCtx.bindIP, uint16(remotePort), err)
			}
			listeners = append(listeners, listener)
		}
		closeStartedTunnelListeners(nil, listeners)
	}
	return ""
}
