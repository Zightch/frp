package client

import (
	"errors"
	"fmt"
	"io"
	"net"
	"sync"

	"github.com/zightch/frp/frps/pkg/protocol"
)

type localUDPSession struct {
	conn      *net.UDPConn
	target    string
	open      protocol.UDPOpen
	closeOnce sync.Once
}

func (c *Client) handleUDPOpen(conn net.Conn, state *sessionState, frame protocol.Frame) error {
	if frame.RequestID == 0 {
		return fmt.Errorf("udp.open requestId must be non-zero")
	}
	if frame.StreamID == 0 {
		return fmt.Errorf("udp.open streamId must be non-zero")
	}

	open, err := protocol.UnmarshalUDPOpen(frame.Body)
	if err != nil {
		return err
	}

	target, err := state.localUDPTarget(open)
	if err != nil {
		return c.sendUDPClose(conn, state, frame.StreamID, protocol.CloseReasonProtocolError, err.Error())
	}
	tunnel, _ := state.tunnelByID(open.TunnelID)

	localAddr, err := net.ResolveUDPAddr("udp", target)
	if err != nil {
		c.logBackendDialFailure(tunnel, target, err)
		return c.sendUDPClose(conn, state, frame.StreamID, protocol.CloseReasonLocalDialFailed, err.Error())
	}

	localConn, err := net.DialUDP("udp", nil, localAddr)
	if err != nil {
		c.logBackendDialFailure(tunnel, target, err)
		return c.sendUDPClose(conn, state, frame.StreamID, protocol.CloseReasonLocalDialFailed, err.Error())
	}
	c.clearBackendDialFailure(tunnel, target)

	udpSession := &localUDPSession{
		conn:   localConn,
		target: target,
		open:   open,
	}
	if !state.addUDPSession(frame.StreamID, udpSession) {
		udpSession.close()
		return c.sendUDPClose(conn, state, frame.StreamID, protocol.CloseReasonProtocolError, fmt.Sprintf("udp session %d already exists", frame.StreamID))
	}

	c.debugf("proxy", "隧道[%s] udp会话已打开 session=%d target=%s", tunnelDisplayName(tunnel), frame.StreamID, target)
	go c.copyLocalUDPToServer(conn, state, frame.StreamID, udpSession)
	return nil
}

func (c *Client) handleUDPData(conn net.Conn, state *sessionState, frame protocol.Frame) error {
	if frame.RequestID != 0 {
		return fmt.Errorf("udp.data requestId must be zero")
	}
	if frame.StreamID == 0 {
		return fmt.Errorf("udp.data streamId must be non-zero")
	}
	if len(frame.Body) > protocol.MaxDataBodyLen {
		return fmt.Errorf("udp.data body exceeds %d bytes", protocol.MaxDataBodyLen)
	}

	udpSession := state.udpSession(frame.StreamID)
	if udpSession == nil {
		return c.sendUDPClose(conn, state, frame.StreamID, protocol.CloseReasonProtocolError, "udp session not found")
	}

	n, err := udpSession.conn.Write(frame.Body)
	if err == nil && n == len(frame.Body) {
		return nil
	}

	message := fmt.Sprintf("short udp write: %d/%d", n, len(frame.Body))
	if err != nil {
		message = err.Error()
	}
	if state.closeUDPSession(frame.StreamID) {
		return c.sendUDPClose(conn, state, frame.StreamID, protocol.CloseReasonWriteError, message)
	}
	return nil
}

func (c *Client) handleUDPClose(state *sessionState, frame protocol.Frame) error {
	if frame.RequestID != 0 {
		return fmt.Errorf("udp.close requestId must be zero")
	}
	if frame.StreamID == 0 {
		return fmt.Errorf("udp.close streamId must be non-zero")
	}

	closeMessage, err := protocol.UnmarshalUDPClose(frame.Body)
	if err != nil {
		return err
	}
	if state.closeUDPSession(frame.StreamID) {
		c.debugf("proxy", "udp会话已关闭 session=%d reason=%d message=%s", frame.StreamID, closeMessage.ReasonCode, closeMessage.Message)
	}
	return nil
}

func (c *Client) sendUDPClose(conn net.Conn, state *sessionState, sessionID uint32, reasonCode uint16, message string) error {
	body, err := protocol.MarshalUDPClose(protocol.UDPClose{
		ReasonCode: reasonCode,
		Initiator:  protocol.InitiatorFRPC,
		Message:    message,
	})
	if err != nil {
		return err
	}
	return c.writeMessage(conn, &state.writeMu, protocol.Frame{
		Type:     protocol.TypeUDPClose,
		StreamID: sessionID,
		Body:     body,
	})
}

func (s *sessionState) addUDPSession(sessionID uint32, udpSession *localUDPSession) bool {
	s.udpMu.Lock()
	defer s.udpMu.Unlock()

	if _, exists := s.udpSessions[sessionID]; exists {
		return false
	}
	s.udpSessions[sessionID] = udpSession
	s.activeUDPSessions.Add(1)
	return true
}

func (s *sessionState) udpSession(sessionID uint32) *localUDPSession {
	s.udpMu.Lock()
	defer s.udpMu.Unlock()
	return s.udpSessions[sessionID]
}

func (s *sessionState) closeUDPSession(sessionID uint32) bool {
	s.udpMu.Lock()
	udpSession, ok := s.udpSessions[sessionID]
	if ok {
		delete(s.udpSessions, sessionID)
		s.activeUDPSessions.Add(^uint32(0))
	}
	s.udpMu.Unlock()

	if !ok {
		return false
	}

	udpSession.close()
	return ok
}

func (s *sessionState) closeAllUDPSessions() int {
	s.udpMu.Lock()
	sessions := make([]*localUDPSession, 0, len(s.udpSessions))
	for sessionID := range s.udpSessions {
		sessions = append(sessions, s.udpSessions[sessionID])
		delete(s.udpSessions, sessionID)
	}
	s.activeUDPSessions.Store(0)
	s.udpMu.Unlock()

	for _, udpSession := range sessions {
		udpSession.close()
	}
	return len(sessions)
}

func (c *Client) copyLocalUDPToServer(conn net.Conn, state *sessionState, sessionID uint32, udpSession *localUDPSession) {
	buffer := make([]byte, protocol.MaxDataBodyLen)
	for {
		n, err := udpSession.conn.Read(buffer)
		if n > 0 {
			payload := append([]byte(nil), buffer[:n]...)
			writeErr := c.writeMessage(conn, &state.writeMu, protocol.Frame{
				Type:     protocol.TypeUDPData,
				StreamID: sessionID,
				Body:     payload,
			})
			if writeErr != nil {
				state.closeUDPSession(sessionID)
				return
			}
		}

		if err == nil {
			continue
		}
		if errors.Is(err, net.ErrClosed) {
			return
		}

		reasonCode := protocol.CloseReasonReadError
		message := err.Error()
		if errors.Is(err, io.EOF) {
			reasonCode = protocol.CloseReasonEOF
			message = "eof"
		}

		if state.closeUDPSession(sessionID) {
			_ = c.sendUDPClose(conn, state, sessionID, reasonCode, message)
		}
		return
	}
}

func (s *localUDPSession) close() {
	s.closeOnce.Do(func() {
		_ = s.conn.Close()
	})
}
