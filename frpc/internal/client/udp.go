package client

import (
	"fmt"
	"net"
	"strconv"

	"github.com/zightch/frp/frps/pkg/protocol"
)

type localUDPSession struct {
	target string
	open   protocol.UDPOpen
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

	udpSession := &localUDPSession{
		target: target,
		open:   open,
	}
	if !state.addUDPSession(frame.StreamID, udpSession) {
		return c.sendUDPClose(conn, state, frame.StreamID, protocol.CloseReasonProtocolError, fmt.Sprintf("udp session %d already exists", frame.StreamID))
	}
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

	// The actual local UDP forwarding path is added in the next step.
	_ = udpSession
	state.closeUDPSession(frame.StreamID)
	return c.sendUDPClose(conn, state, frame.StreamID, protocol.CloseReasonProtocolError, "udp session not ready")
}

func (c *Client) handleUDPClose(state *sessionState, frame protocol.Frame) error {
	if frame.RequestID != 0 {
		return fmt.Errorf("udp.close requestId must be zero")
	}
	if frame.StreamID == 0 {
		return fmt.Errorf("udp.close streamId must be non-zero")
	}

	if _, err := protocol.UnmarshalUDPClose(frame.Body); err != nil {
		return err
	}
	state.closeUDPSession(frame.StreamID)
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
	_, ok := s.udpSessions[sessionID]
	if ok {
		delete(s.udpSessions, sessionID)
		s.activeUDPSessions.Add(^uint32(0))
	}
	s.udpMu.Unlock()
	return ok
}

func (s *sessionState) closeAllUDPSessions() {
	s.udpMu.Lock()
	for sessionID := range s.udpSessions {
		delete(s.udpSessions, sessionID)
	}
	s.activeUDPSessions.Store(0)
	s.udpMu.Unlock()
}

func (s *sessionState) localUDPTarget(open protocol.UDPOpen) (string, error) {
	tunnel, ok := s.tunnelByID(open.TunnelID)
	if !ok {
		return "", fmt.Errorf("tunnel %d not found", open.TunnelID)
	}
	if tunnel.Protocol != protocol.ProtocolUDP {
		return "", fmt.Errorf("tunnel %d is not udp", open.TunnelID)
	}
	if tunnel.TunnelFlags&protocol.TunnelFlagEnabled == 0 {
		return "", fmt.Errorf("tunnel %d is disabled", open.TunnelID)
	}
	if tunnel.TunnelFlags&protocol.TunnelFlagRange != 0 {
		return "", fmt.Errorf("tunnel %d range udp is not supported yet", open.TunnelID)
	}
	if open.RemotePort < tunnel.RemoteStart || open.RemotePort > tunnel.RemoteEnd {
		return "", fmt.Errorf("remote port %d is outside tunnel %d", open.RemotePort, open.TunnelID)
	}

	offset := int(open.RemotePort - tunnel.RemoteStart)
	localPort := int(tunnel.LocalStart) + offset
	if localPort > int(tunnel.LocalEnd) {
		return "", fmt.Errorf("local port mapping is out of range for tunnel %d", open.TunnelID)
	}

	host := tunnel.LocalHost.String()
	if host == "" {
		return "", fmt.Errorf("tunnel %d has empty local host", open.TunnelID)
	}

	return net.JoinHostPort(host, strconv.Itoa(localPort)), nil
}
