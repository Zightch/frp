package control

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/zightch/frp/frps/pkg/protocol"
)

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
