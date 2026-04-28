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
	"time"

	controlruntime "github.com/zightch/frp/frps/internal/control/runtime"
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

type publicStream = controlruntime.Stream

type publicUDPSession = controlruntime.UDPSession

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

// WriteFrame implements controlruntime.RuntimeIOWriter.
func (w sessionRuntimeIOWriter) WriteFrame(frame protocol.Frame) error {
	return w.writeFrame(frame)
}

// WriteFrames implements controlruntime.RuntimeIOWriter.
func (w sessionRuntimeIOWriter) WriteFrames(frames ...protocol.Frame) error {
	return w.writeFrames(frames...)
}

func (s *sessionState) preparePublicStreamOpen(configVersion uint64, tunnel protocol.TunnelEntry, remotePort uint16, publicConn net.Conn, now time.Time) (sessionStreamOpenOperation, error) {
	streamID := s.NextTunnelStreamID()
	requestID := s.NextRequestID()
	stream := newPublicStream(configVersion, tunnel, remotePort, requestID, publicConn, now)
	if !s.addPublicStream(streamID, stream, configVersion) {
		return sessionStreamOpenOperation{blocked: true}, nil
	}

	body, err := protocol.MarshalStreamOpen(protocol.StreamOpen{
		TunnelID:   tunnel.TunnelID,
		RemotePort: remotePort,
		ClientAddr: stream.ClientAddr,
		OpenedAtMs: stream.OpenedAtMs,
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
	udpSession := newPublicUDPSession(s.NextTunnelStreamID(), tunnel, remotePort, listener, clientAddr, now)
	udpSession, created := s.bindPublicUDPSession(udpSession, configVersion)
	if udpSession == nil {
		return sessionUDPDatagramForwardOperation{blocked: true}, nil
	}
	if !created {
		udpSession.Touch(now)
		return sessionUDPDatagramForwardOperation{
			udpSession: udpSession,
			frames: []protocol.Frame{
				{
					Type:     protocol.TypeUDPData,
					StreamID: udpSession.SessionID,
					Body:     payload,
				},
			},
		}, nil
	}

	requestID := s.NextRequestID()
	openBody, err := protocol.MarshalUDPOpen(protocol.UDPOpen{
		TunnelID:      tunnel.TunnelID,
		RemotePort:    udpSession.RemotePort,
		ClientAddr:    udpSession.ClientAddr,
		IdleTimeoutMs: uint32(udpSession.IdleTimeout / time.Millisecond),
	})
	if err != nil {
		s.closePublicUDPSession(udpSession.SessionID)
		return sessionUDPDatagramForwardOperation{}, err
	}

	return sessionUDPDatagramForwardOperation{
		created:    true,
		udpSession: udpSession,
		frames: []protocol.Frame{
			{
				Type:      protocol.TypeUDPOpen,
				RequestID: requestID,
				StreamID:  udpSession.SessionID,
				Body:      openBody,
			},
			{
				Type:     protocol.TypeUDPData,
				StreamID: udpSession.SessionID,
				Body:     payload,
			},
		},
	}, nil
}

func observeRuntimeConnections(streams map[uint32]*publicStream, udpSessions map[uint32]*publicUDPSession) []controlruntime.ObservedConnection {
	connections := make([]controlruntime.ObservedConnection, 0, len(streams)+len(udpSessions))
	for streamID, stream := range streams {
		if stream == nil {
			continue
		}
		connections = append(connections, stream.ObservedConnection(streamID))
	}
	for _, udpSession := range udpSessions {
		if udpSession == nil {
			continue
		}
		connections = append(connections, udpSession.ObservedConnection())
	}

	sort.Slice(connections, func(i, j int) bool {
		if connections[i].Kind == connections[j].Kind {
			if connections[i].TunnelID == connections[j].TunnelID {
				return connections[i].ConnectionID < connections[j].ConnectionID
			}
			return connections[i].TunnelID < connections[j].TunnelID
		}
		return connections[i].Kind < connections[j].Kind
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
	case openErr := <-openOp.stream.Ready:
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
	if frame.RequestID != stream.OpenRequestID {
		return s.replyErrorWithSession(conn, session, frame.RequestID, frame.StreamID, protocol.ErrorCodeProtocolBadBody, "unexpected stream.opened requestId %d", frame.RequestID)
	}

	switch opened.Status {
	case protocol.StatusOK:
		stream.SignalReady(nil)
	case protocol.StatusError:
		stream.SignalReady(fmt.Errorf("%s", opened.Message))
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

	if err := writeConnFull(stream.Conn, frame.Body); err != nil {
		if session.closePublicStream(frame.StreamID) {
			return s.sendStreamClose(conn, session, frame.StreamID, protocol.CloseReasonWriteError, err.Error())
		}
		return nil
	}
	stream.Touch(s.clock.Now())
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
		n, err := stream.Conn.Read(buffer)
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
			stream.Touch(s.clock.Now())
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
		ConfigVersion: configVersion,
		Conn:          publicConn,
		Tunnel:        tunnel,
		RemotePort:    remotePort,
		ClientAddr:    sockAddrFromNetAddr(publicConn.RemoteAddr()),
		OpenedAtMs:    uint64(now.UTC().UnixMilli()),
		OpenRequestID: openRequestID,
		Ready:         make(chan error, 1),
	}
	stream.Touch(now)
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

	if _, err := udpSession.Listener.WriteToUDP(frame.Body, udpSession.PublicAddr); err != nil {
		if session.closePublicUDPSession(frame.StreamID) {
			return s.sendUDPClose(conn, session, frame.StreamID, protocol.CloseReasonWriteError, err.Error())
		}
		return nil
	}
	udpSession.Touch(s.clock.Now())
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
		<-session.DoneCh()
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
		if err := s.sendUDPClose(conn, session, udpSession.SessionID, protocol.CloseReasonIdleTimeout, "udp session idle timeout"); err != nil {
			return err
		}
		logger.Info(
			"udp session closed for idle timeout",
			"session_id", udpSession.SessionID,
			"tunnel_id", udpSession.TunnelID,
			"client_addr", udpSession.PublicAddr.String(),
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
			serve.session.closePublicUDPSession(forwardOp.udpSession.SessionID)
		}
		if errors.Is(err, errRuntimeIOStopped) {
			return nil
		}
		return err
	}

	if forwardOp.created {
		serve.logger.Info("udp session opened", "session_id", forwardOp.udpSession.SessionID, "tunnel_id", serve.tunnel.TunnelID, "client_addr", clientAddr.String())
	}
	return nil
}

func (s *sessionState) bindPublicUDPSession(udpSession *publicUDPSession, configVersion uint64) (*publicUDPSession, bool) {
	s.RuntimeMu.Lock()
	defer s.RuntimeMu.Unlock()

	if s.Runtime.Frozen || !s.Runtime.Listeners.Started || s.Runtime.Generation != configVersion {
		return nil, false
	}

	key := udpSession.Key()
	if sessionID, exists := s.Runtime.UDP.Keys[key]; exists {
		if existing := s.Runtime.UDP.Sessions[sessionID]; existing != nil {
			return existing, false
		}
		delete(s.Runtime.UDP.Keys, key)
	}
	if _, exists := s.Runtime.UDP.Sessions[udpSession.SessionID]; exists {
		return s.Runtime.UDP.Sessions[udpSession.SessionID], false
	}
	s.Runtime.UDP.Sessions[udpSession.SessionID] = udpSession
	s.Runtime.UDP.Keys[key] = udpSession.SessionID
	return udpSession, true
}

func (s *sessionState) publicUDPSession(sessionID uint32) *publicUDPSession {
	s.RuntimeMu.Lock()
	defer s.RuntimeMu.Unlock()
	return s.Runtime.UDP.Sessions[sessionID]
}

func (s *sessionState) closePublicUDPSession(sessionID uint32) bool {
	s.RuntimeMu.Lock()
	udpSession, ok := s.Runtime.UDP.Sessions[sessionID]
	if ok {
		delete(s.Runtime.UDP.Sessions, sessionID)
		delete(s.Runtime.UDP.Keys, udpSession.Key())
	}
	s.RuntimeMu.Unlock()
	return ok
}

func (s *sessionState) takeIdlePublicUDPSessions(now time.Time) []*publicUDPSession {
	s.RuntimeMu.Lock()
	defer s.RuntimeMu.Unlock()

	idleSessions := make([]*publicUDPSession, 0)
	for sessionID, udpSession := range s.Runtime.UDP.Sessions {
		lastActiveUnixMs := udpSession.LastActiveUnixMs.Load()
		if lastActiveUnixMs == 0 {
			continue
		}
		lastActive := time.UnixMilli(lastActiveUnixMs).UTC()
		if now.Before(lastActive) || now.Sub(lastActive) < udpSession.IdleTimeout {
			continue
		}
		delete(s.Runtime.UDP.Sessions, sessionID)
		delete(s.Runtime.UDP.Keys, udpSession.Key())
		idleSessions = append(idleSessions, udpSession)
	}
	return idleSessions
}

func newPublicUDPSession(sessionID uint32, tunnel protocol.TunnelEntry, remotePort uint16, listener UDPListener, clientAddr *net.UDPAddr, now time.Time) *publicUDPSession {
	udpSession := &publicUDPSession{
		SessionID:   sessionID,
		TunnelID:    tunnel.TunnelID,
		RemotePort:  remotePort,
		ClientAddr:  sockAddrFromNetAddr(clientAddr),
		PublicAddr:  cloneUDPAddr(clientAddr),
		Listener:    listener,
		OpenedAtMs:  uint64(now.UTC().UnixMilli()),
		IdleTimeout: defaultUDPIdleTimeout,
	}
	udpSession.Touch(now)
	return udpSession
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
