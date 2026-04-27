package control

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"sort"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/zightch/frp/frps/pkg/protocol"
	"github.com/zightch/frp/frps/pkg/transport"
)

type Logger interface {
	Info(msg string, args ...any)
	Warn(msg string, args ...any)
}

func (s *Server) registerConn(conn net.Conn) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.activeConn[conn] = struct{}{}
}

func (s *Server) unregisterConn(conn net.Conn) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.activeConn, conn)
}

func logConnection(logger *slog.Logger, level slog.Level, message string, err error) {
	if isExpectedConnectionClose(err) {
		logger.Log(context.Background(), slog.LevelInfo, message, "error", err)
		return
	}
	logger.Log(context.Background(), level, message, "error", err)
}

func connectionErrorDetails(err error) (slog.Level, string) {
	if err == nil {
		return slog.LevelInfo, "completed"
	}
	if isExpectedConnectionClose(err) {
		return slog.LevelInfo, connectionReason(err)
	}
	return slog.LevelWarn, connectionReason(err)
}

func isExpectedConnectionClose(err error) bool {
	if err == nil {
		return true
	}
	if errors.Is(err, io.EOF) || errors.Is(err, net.ErrClosed) {
		return true
	}

	var netErr net.Error
	return errors.As(err, &netErr) && netErr.Timeout()
}

func connectionReason(err error) string {
	switch {
	case err == nil:
		return "completed"
	case errors.Is(err, io.EOF):
		return "eof"
	case errors.Is(err, net.ErrClosed):
		return "closed"
	case errors.Is(err, transport.ErrFrameTooSmall):
		return "invalid frame length below minimum"
	case errors.Is(err, transport.ErrFrameTooLarge):
		return "invalid frame length above maximum"
	}

	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return "timeout"
	}

	var protocolErr *protocol.ProtocolError
	if errors.As(err, &protocolErr) {
		return protocolErr.Message
	}

	return err.Error()
}

const (
	observedRuntimeConnectionKindTCPStream  = "tcp_stream"
	observedRuntimeConnectionKindUDPSession = "udp_session"
)

type sessionRuntimeIOWriter struct {
	server        *Server
	conn          net.Conn
	session       *sessionState
	configVersion uint64
}

type sessionStreamOpenOperation struct {
	blocked   bool
	streamID  uint32
	stream    *publicStream
	openFrame protocol.Frame
}

type sessionUDPDatagramForwardOperation struct {
	blocked    bool
	created    bool
	udpSession *publicUDPSession
	frames     []protocol.Frame
}

type observedSessionRuntimeConnection struct {
	connectionID   uint32
	kind           string
	protocol       string
	tunnelID       uint32
	remotePort     uint16
	clientAddr     string
	openedAtMs     uint64
	lastActiveAtMs uint64
	idleTimeoutMs  uint32
}

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

type publicUDPSession struct {
	sessionID        uint32
	tunnelID         uint32
	remotePort       uint16
	clientAddr       protocol.SockAddr
	publicAddr       *net.UDPAddr
	listener         UDPListener
	openedAtMs       uint64
	idleTimeout      time.Duration
	lastActiveUnixMs atomic.Int64
}

func newSessionRuntimeIOWriter(server *Server, conn net.Conn, session *sessionState, configVersion uint64) sessionRuntimeIOWriter {
	return sessionRuntimeIOWriter{
		server:        server,
		conn:          conn,
		session:       session,
		configVersion: configVersion,
	}
}

func (w sessionRuntimeIOWriter) writeFrame(frame protocol.Frame) error {
	if w.server == nil || w.conn == nil || w.session == nil {
		return errRuntimeIOStopped
	}
	return w.server.writeRuntimeFrameWithSession(w.conn, w.session, w.configVersion, frame)
}

func (w sessionRuntimeIOWriter) writeFrames(frames ...protocol.Frame) error {
	if w.server == nil || w.conn == nil || w.session == nil {
		return errRuntimeIOStopped
	}
	return w.server.writeRuntimeFramesWithSession(w.conn, w.session, w.configVersion, frames...)
}

func (s *sessionState) preparePublicStreamOpen(configVersion uint64, tunnel protocol.TunnelEntry, remotePort uint16, publicConn net.Conn, now time.Time) (sessionStreamOpenOperation, error) {
	streamID := s.nextTunnelStreamID()
	requestID := s.nextRequestID()
	stream := newPublicStream(configVersion, tunnel, remotePort, requestID, publicConn, now)
	if !s.addPublicStream(streamID, stream, configVersion) {
		return sessionStreamOpenOperation{blocked: true}, nil
	}

	body, err := protocol.MarshalStreamOpen(protocol.StreamOpen{
		TunnelID:   tunnel.TunnelID,
		RemotePort: remotePort,
		ClientAddr: stream.clientAddr,
		OpenedAtMs: stream.openedAtMs,
	})
	if err != nil {
		s.closePublicStream(streamID)
		return sessionStreamOpenOperation{}, err
	}

	return sessionStreamOpenOperation{
		streamID: streamID,
		stream:   stream,
		openFrame: protocol.Frame{
			Type:      protocol.TypeStreamOpen,
			RequestID: requestID,
			StreamID:  streamID,
			Body:      body,
		},
	}, nil
}

func (s *sessionState) preparePublicUDPDatagramForward(configVersion uint64, tunnel protocol.TunnelEntry, remotePort uint16, listener UDPListener, clientAddr *net.UDPAddr, payload []byte, now time.Time) (sessionUDPDatagramForwardOperation, error) {
	udpSession := newPublicUDPSession(s.nextTunnelStreamID(), tunnel, remotePort, listener, clientAddr, now)
	udpSession, created := s.bindPublicUDPSession(udpSession, configVersion)
	if udpSession == nil {
		return sessionUDPDatagramForwardOperation{blocked: true}, nil
	}
	if !created {
		udpSession.touch(now)
		return sessionUDPDatagramForwardOperation{
			udpSession: udpSession,
			frames: []protocol.Frame{
				{
					Type:     protocol.TypeUDPData,
					StreamID: udpSession.sessionID,
					Body:     payload,
				},
			},
		}, nil
	}

	requestID := s.nextRequestID()
	openBody, err := protocol.MarshalUDPOpen(protocol.UDPOpen{
		TunnelID:      tunnel.TunnelID,
		RemotePort:    udpSession.remotePort,
		ClientAddr:    udpSession.clientAddr,
		IdleTimeoutMs: uint32(udpSession.idleTimeout / time.Millisecond),
	})
	if err != nil {
		s.closePublicUDPSession(udpSession.sessionID)
		return sessionUDPDatagramForwardOperation{}, err
	}

	return sessionUDPDatagramForwardOperation{
		created:    true,
		udpSession: udpSession,
		frames: []protocol.Frame{
			{
				Type:      protocol.TypeUDPOpen,
				RequestID: requestID,
				StreamID:  udpSession.sessionID,
				Body:      openBody,
			},
			{
				Type:     protocol.TypeUDPData,
				StreamID: udpSession.sessionID,
				Body:     payload,
			},
		},
	}, nil
}

func observeRuntimeConnections(streams map[uint32]*publicStream, udpSessions map[uint32]*publicUDPSession) []observedSessionRuntimeConnection {
	connections := make([]observedSessionRuntimeConnection, 0, len(streams)+len(udpSessions))
	for streamID, stream := range streams {
		if stream == nil {
			continue
		}
		connections = append(connections, stream.observedConnection(streamID))
	}
	for _, udpSession := range udpSessions {
		if udpSession == nil {
			continue
		}
		connections = append(connections, udpSession.observedConnection())
	}

	sort.Slice(connections, func(i, j int) bool {
		if connections[i].kind == connections[j].kind {
			if connections[i].tunnelID == connections[j].tunnelID {
				return connections[i].connectionID < connections[j].connectionID
			}
			return connections[i].tunnelID < connections[j].tunnelID
		}
		return connections[i].kind < connections[j].kind
	})

	return connections
}

func nonNegativeUnixMilli(value int64) uint64 {
	if value <= 0 {
		return 0
	}
	return uint64(value)
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

func (s *Server) handleUDPData(conn net.Conn, session *sessionState, frame protocol.Frame) error {
	if frame.RequestID != 0 {
		return s.replyErrorWithSession(conn, session, frame.RequestID, frame.StreamID, protocol.ErrorCodeProtocolBadBody, "udp.data requestId must be zero")
	}
	if frame.StreamID == 0 {
		return s.replyErrorWithSession(conn, session, 0, frame.StreamID, protocol.ErrorCodeProtocolBadBody, "udp.data streamId must be non-zero")
	}
	if len(frame.Body) > protocol.MaxDataBodyLen {
		return s.replyErrorWithSession(conn, session, 0, frame.StreamID, protocol.ErrorCodeProtocolBadBody, "udp.data body exceeds %d bytes", protocol.MaxDataBodyLen)
	}

	udpSession := session.publicUDPSession(frame.StreamID)
	if udpSession == nil {
		return s.sendUDPClose(conn, session, frame.StreamID, protocol.CloseReasonProtocolError, "udp session not found")
	}

	if _, err := udpSession.listener.WriteToUDP(frame.Body, udpSession.publicAddr); err != nil {
		if session.closePublicUDPSession(frame.StreamID) {
			return s.sendUDPClose(conn, session, frame.StreamID, protocol.CloseReasonWriteError, err.Error())
		}
		return nil
	}
	udpSession.touch(s.clock.Now())
	return nil
}

func (s *Server) handleUDPClose(session *sessionState, frame protocol.Frame) error {
	if frame.RequestID != 0 {
		return fmt.Errorf("udp.close requestId must be zero")
	}
	if frame.StreamID == 0 {
		return fmt.Errorf("udp.close streamId must be non-zero")
	}
	if _, err := protocol.UnmarshalUDPClose(frame.Body); err != nil {
		return err
	}
	session.closePublicUDPSession(frame.StreamID)
	return nil
}

func (s *Server) sendUDPClose(conn net.Conn, session *sessionState, sessionID uint32, reasonCode uint16, message string) error {
	body, err := protocol.MarshalUDPClose(protocol.UDPClose{
		ReasonCode: reasonCode,
		Initiator:  protocol.InitiatorFRPS,
		Message:    message,
	})
	if err != nil {
		return err
	}
	return s.writeFrameWithSession(conn, session, protocol.Frame{
		Type:     protocol.TypeUDPClose,
		StreamID: sessionID,
		Body:     body,
	})
}

func (s *Server) serveUDPIdleCleanup(conn net.Conn, logger Logger, session *sessionState) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go func() {
		<-session.doneCh()
		cancel()
	}()

	task := s.scheduler.Every(ctx, "control.udp_idle_cleanup", defaultUDPIdleSweep, func(ctx context.Context, now time.Time) {
		if err := s.cleanupIdlePublicUDPSessions(conn, logger, session, now.UTC()); err != nil {
			logger.Warn("udp session idle cleanup failed", "error", err)
		}
	})
	<-task.Done()
}

func (s *Server) cleanupIdlePublicUDPSessions(conn net.Conn, logger Logger, session *sessionState, now time.Time) error {
	idleSessions := session.takeIdlePublicUDPSessions(now)
	for _, udpSession := range idleSessions {
		if err := s.sendUDPClose(conn, session, udpSession.sessionID, protocol.CloseReasonIdleTimeout, "udp session idle timeout"); err != nil {
			return err
		}
		logger.Info(
			"udp session closed for idle timeout",
			"session_id", udpSession.sessionID,
			"tunnel_id", udpSession.tunnelID,
			"client_addr", udpSession.publicAddr.String(),
		)
	}
	return nil
}

func (s *Server) handlePublicUDPDatagram(serve tunnelRuntimeServeContext, listener UDPListener, clientAddr *net.UDPAddr, payload []byte) error {
	now := s.clock.Now()
	forwardOp, err := serve.session.preparePublicUDPDatagramForward(serve.runtimeIO.configVersion, serve.tunnel, serve.remotePort, listener, clientAddr, payload, now)
	if err != nil {
		return err
	}
	if forwardOp.blocked {
		return nil
	}
	err = serve.runtimeIO.writeFrames(forwardOp.frames...)
	if err != nil {
		if forwardOp.created {
			serve.session.closePublicUDPSession(forwardOp.udpSession.sessionID)
		}
		if errors.Is(err, errRuntimeIOStopped) {
			return nil
		}
		return err
	}

	if forwardOp.created {
		serve.logger.Info("udp session opened", "session_id", forwardOp.udpSession.sessionID, "tunnel_id", serve.tunnel.TunnelID, "client_addr", clientAddr.String())
	}
	return nil
}

func (s *sessionState) bindPublicUDPSession(udpSession *publicUDPSession, configVersion uint64) (*publicUDPSession, bool) {
	s.runtimeMu.Lock()
	defer s.runtimeMu.Unlock()

	if s.runtime.frozen || !s.runtime.listeners.started || s.runtime.generation != configVersion {
		return nil, false
	}

	key := udpSession.key()
	if sessionID, exists := s.runtime.udp.keys[key]; exists {
		if existing := s.runtime.udp.sessions[sessionID]; existing != nil {
			return existing, false
		}
		delete(s.runtime.udp.keys, key)
	}
	if _, exists := s.runtime.udp.sessions[udpSession.sessionID]; exists {
		return s.runtime.udp.sessions[udpSession.sessionID], false
	}
	s.runtime.udp.sessions[udpSession.sessionID] = udpSession
	s.runtime.udp.keys[key] = udpSession.sessionID
	return udpSession, true
}

func (s *sessionState) publicUDPSession(sessionID uint32) *publicUDPSession {
	s.runtimeMu.Lock()
	defer s.runtimeMu.Unlock()
	return s.runtime.udp.sessions[sessionID]
}

func (s *sessionState) closePublicUDPSession(sessionID uint32) bool {
	s.runtimeMu.Lock()
	udpSession, ok := s.runtime.udp.sessions[sessionID]
	if ok {
		delete(s.runtime.udp.sessions, sessionID)
		delete(s.runtime.udp.keys, udpSession.key())
	}
	s.runtimeMu.Unlock()
	return ok
}

func (s *sessionState) takeIdlePublicUDPSessions(now time.Time) []*publicUDPSession {
	s.runtimeMu.Lock()
	defer s.runtimeMu.Unlock()

	idleSessions := make([]*publicUDPSession, 0)
	for sessionID, udpSession := range s.runtime.udp.sessions {
		lastActiveUnixMs := udpSession.lastActiveUnixMs.Load()
		if lastActiveUnixMs == 0 {
			continue
		}
		lastActive := time.UnixMilli(lastActiveUnixMs).UTC()
		if now.Before(lastActive) || now.Sub(lastActive) < udpSession.idleTimeout {
			continue
		}
		delete(s.runtime.udp.sessions, sessionID)
		delete(s.runtime.udp.keys, udpSession.key())
		idleSessions = append(idleSessions, udpSession)
	}
	return idleSessions
}

func newPublicUDPSession(sessionID uint32, tunnel protocol.TunnelEntry, remotePort uint16, listener UDPListener, clientAddr *net.UDPAddr, now time.Time) *publicUDPSession {
	udpSession := &publicUDPSession{
		sessionID:   sessionID,
		tunnelID:    tunnel.TunnelID,
		remotePort:  remotePort,
		clientAddr:  sockAddrFromNetAddr(clientAddr),
		publicAddr:  cloneUDPAddr(clientAddr),
		listener:    listener,
		openedAtMs:  uint64(now.UTC().UnixMilli()),
		idleTimeout: defaultUDPIdleTimeout,
	}
	udpSession.touch(now)
	return udpSession
}

func (s *publicUDPSession) touch(now time.Time) {
	s.lastActiveUnixMs.Store(now.UnixMilli())
}

func (s *publicUDPSession) key() string {
	return publicUDPSessionKey(s.tunnelID, s.remotePort, s.clientAddr)
}

func (s *publicUDPSession) observedConnection() observedSessionRuntimeConnection {
	return observedSessionRuntimeConnection{
		connectionID:   s.sessionID,
		kind:           observedRuntimeConnectionKindUDPSession,
		protocol:       "udp",
		tunnelID:       s.tunnelID,
		remotePort:     s.remotePort,
		clientAddr:     sockAddrString(s.clientAddr),
		openedAtMs:     s.openedAtMs,
		lastActiveAtMs: nonNegativeUnixMilli(s.lastActiveUnixMs.Load()),
		idleTimeoutMs:  uint32(s.idleTimeout / time.Millisecond),
	}
}

func publicUDPSessionKey(tunnelID uint32, remotePort uint16, clientAddr protocol.SockAddr) string {
	ip := clientAddr.IP
	if ip4 := ip.To4(); ip4 != nil {
		ip = ip4
	} else {
		ip = ip.To16()
	}
	return strconv.FormatUint(uint64(tunnelID), 10) +
		"|" + strconv.FormatUint(uint64(remotePort), 10) +
		"|" + ip.String() +
		"|" + strconv.FormatUint(uint64(clientAddr.Port), 10)
}

func cloneUDPAddr(addr *net.UDPAddr) *net.UDPAddr {
	if addr == nil {
		return nil
	}
	return &net.UDPAddr{
		IP:   append(net.IP(nil), addr.IP...),
		Port: addr.Port,
		Zone: addr.Zone,
	}
}

func sockAddrFromNetAddr(addr net.Addr) protocol.SockAddr {
	if addr == nil {
		return protocol.SockAddr{}
	}
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

func sockAddrString(addr protocol.SockAddr) string {
	if len(addr.IP) == 0 && addr.Port == 0 {
		return ""
	}
	ip := addr.IP
	if ip4 := ip.To4(); ip4 != nil {
		ip = ip4
	} else if ip16 := ip.To16(); ip16 != nil {
		ip = ip16
	}
	return net.JoinHostPort(ip.String(), strconv.Itoa(int(addr.Port)))
}
