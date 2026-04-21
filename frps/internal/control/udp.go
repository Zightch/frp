package control

import (
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
	listener         *net.UDPConn
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
	udpSession.touch(time.Now().UTC())
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
	ticker := time.NewTicker(defaultUDPIdleSweep)
	defer ticker.Stop()

	for {
		select {
		case <-session.doneCh():
			return
		case now := <-ticker.C:
			if err := s.cleanupIdlePublicUDPSessions(conn, logger, session, now.UTC()); err != nil {
				logger.Warn("udp session idle cleanup failed", "error", err)
			}
		}
	}
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

func (s *Server) handlePublicUDPDatagram(controlConn net.Conn, logger Logger, session *sessionState, configVersion uint64, tunnel protocol.TunnelEntry, remotePort uint16, listener *net.UDPConn, clientAddr *net.UDPAddr, payload []byte) error {
	now := time.Now().UTC()
	udpSession := newPublicUDPSession(session.nextTunnelStreamID(), tunnel, remotePort, listener, clientAddr, now)
	udpSession, created := session.bindPublicUDPSession(udpSession, configVersion)
	if udpSession == nil {
		return nil
	}
	if !created {
		udpSession.touch(now)
		err := s.writeRuntimeFrameWithSession(controlConn, session, configVersion, protocol.Frame{
			Type:     protocol.TypeUDPData,
			StreamID: udpSession.sessionID,
			Body:     payload,
		})
		if errors.Is(err, errRuntimeIOStopped) {
			return nil
		}
		return err
	}

	requestID := session.nextRequestID()
	openBody, err := protocol.MarshalUDPOpen(protocol.UDPOpen{
		TunnelID:      tunnel.TunnelID,
		RemotePort:    udpSession.remotePort,
		ClientAddr:    udpSession.clientAddr,
		IdleTimeoutMs: uint32(udpSession.idleTimeout / time.Millisecond),
	})
	if err != nil {
		session.closePublicUDPSession(udpSession.sessionID)
		return err
	}

	err = s.writeRuntimeFramesWithSession(controlConn, session, configVersion,
		protocol.Frame{
			Type:      protocol.TypeUDPOpen,
			RequestID: requestID,
			StreamID:  udpSession.sessionID,
			Body:      openBody,
		},
		protocol.Frame{
			Type:     protocol.TypeUDPData,
			StreamID: udpSession.sessionID,
			Body:     payload,
		},
	)
	if err != nil {
		session.closePublicUDPSession(udpSession.sessionID)
		if errors.Is(err, errRuntimeIOStopped) {
			return nil
		}
		return err
	}

	logger.Info("udp session opened", "session_id", udpSession.sessionID, "tunnel_id", tunnel.TunnelID, "client_addr", clientAddr.String())
	return nil
}

func (s *sessionState) bindPublicUDPSession(udpSession *publicUDPSession, configVersion uint64) (*publicUDPSession, bool) {
	s.runtimeMu.Lock()
	defer s.runtimeMu.Unlock()

	if s.runtimeFrozen || !s.listenersStarted || s.runtimeGeneration != configVersion {
		return nil, false
	}

	key := udpSession.key()
	if sessionID, exists := s.udpSessionKeys[key]; exists {
		if existing := s.udpSessions[sessionID]; existing != nil {
			return existing, false
		}
		delete(s.udpSessionKeys, key)
	}
	if _, exists := s.udpSessions[udpSession.sessionID]; exists {
		return s.udpSessions[udpSession.sessionID], false
	}
	s.udpSessions[udpSession.sessionID] = udpSession
	s.udpSessionKeys[key] = udpSession.sessionID
	return udpSession, true
}

func (s *sessionState) publicUDPSession(sessionID uint32) *publicUDPSession {
	s.runtimeMu.Lock()
	defer s.runtimeMu.Unlock()
	return s.udpSessions[sessionID]
}

func (s *sessionState) closePublicUDPSession(sessionID uint32) bool {
	s.runtimeMu.Lock()
	udpSession, ok := s.udpSessions[sessionID]
	if ok {
		delete(s.udpSessions, sessionID)
		delete(s.udpSessionKeys, udpSession.key())
	}
	s.runtimeMu.Unlock()
	return ok
}

func (s *sessionState) takeIdlePublicUDPSessions(now time.Time) []*publicUDPSession {
	s.runtimeMu.Lock()
	defer s.runtimeMu.Unlock()

	idleSessions := make([]*publicUDPSession, 0)
	for sessionID, udpSession := range s.udpSessions {
		lastActiveUnixMs := udpSession.lastActiveUnixMs.Load()
		if lastActiveUnixMs == 0 {
			continue
		}
		lastActive := time.UnixMilli(lastActiveUnixMs).UTC()
		if now.Before(lastActive) || now.Sub(lastActive) < udpSession.idleTimeout {
			continue
		}
		delete(s.udpSessions, sessionID)
		delete(s.udpSessionKeys, udpSession.key())
		idleSessions = append(idleSessions, udpSession)
	}
	return idleSessions
}

func newPublicUDPSession(sessionID uint32, tunnel protocol.TunnelEntry, remotePort uint16, listener *net.UDPConn, clientAddr *net.UDPAddr, now time.Time) *publicUDPSession {
	udpSession := &publicUDPSession{
		sessionID:   sessionID,
		tunnelID:    tunnel.TunnelID,
		remotePort:  remotePort,
		clientAddr:  sockAddrFromNetAddr(clientAddr),
		publicAddr:  cloneUDPAddr(clientAddr),
		listener:    listener,
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
