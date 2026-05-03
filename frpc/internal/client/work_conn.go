package client

import (
	"context"
	"fmt"
	"net"

	"github.com/zightch/frp/frps/pkg/protocol"
)

const (
	tcpWorkHelloRequestID    uint32 = 1
	tcpWorkRegisterRequestID uint32 = 2
)

func (c *Client) openTCPWorkConn(ctx context.Context, state *sessionState) (net.Conn, error) {
	if state == nil {
		return nil, fmt.Errorf("session state is nil")
	}

	sessionID, poolTarget, workSecret := state.tcpWorkConfig()
	if sessionID == 0 {
		return nil, fmt.Errorf("session id is not initialized")
	}
	if poolTarget == 0 {
		return nil, fmt.Errorf("tcp work pool target is not configured")
	}

	conn, err := c.dialContext(ctx, "tcp", c.config.Server)
	if err != nil {
		return nil, err
	}

	workConn, err := c.negotiateTCPWorkConn(conn, state, sessionID, workSecret)
	if err != nil {
		_ = conn.Close()
		return nil, err
	}
	return workConn, nil
}

func (c *Client) negotiateTCPWorkConn(conn net.Conn, state *sessionState, sessionID uint64, workSecret [32]byte) (net.Conn, error) {
	helloBody, err := protocol.MarshalTCPWorkHello(protocol.TCPWorkHello{
		SessionID:              sessionID,
		SupportedSecurityModes: protocol.TransportSecurityModePlain | protocol.TransportSecurityModeTLS,
	})
	if err != nil {
		return nil, err
	}
	if err := c.writeMessageWithState(conn, state, nil, protocol.Frame{
		Type:      protocol.TypeTCPWorkHello,
		RequestID: tcpWorkHelloRequestID,
		Body:      helloBody,
	}); err != nil {
		return nil, err
	}

	frame, err := c.readWorkFrame(conn, state)
	if err != nil {
		return nil, err
	}
	if err := expectLoginReply(frame, protocol.TypeTCPWorkServerHello, tcpWorkHelloRequestID); err != nil {
		return nil, err
	}

	serverHello, err := protocol.UnmarshalTCPWorkServerHello(frame.Body)
	if err != nil {
		return nil, err
	}
	switch serverHello.SelectedSecurityMode {
	case protocol.TransportSecurityModePlain:
	case protocol.TransportSecurityModeTLS:
		conn, err = c.upgradeConnToTLS(conn)
		if err != nil {
			return nil, err
		}
	default:
		return nil, fmt.Errorf("unsupported tcp work transport security mode %d", serverHello.SelectedSecurityMode)
	}

	registerBody, err := protocol.MarshalTCPWorkRegister(protocol.TCPWorkRegister{
		WorkSecret: workSecret,
	})
	if err != nil {
		return nil, err
	}
	if err := c.writeMessageWithState(conn, state, nil, protocol.Frame{
		Type:      protocol.TypeTCPWorkRegister,
		RequestID: tcpWorkRegisterRequestID,
		Body:      registerBody,
	}); err != nil {
		return nil, err
	}

	frame, err = c.readWorkFrame(conn, state)
	if err != nil {
		return nil, err
	}
	if err := expectLoginReply(frame, protocol.TypeTCPWorkReady, tcpWorkRegisterRequestID); err != nil {
		return nil, err
	}
	if _, err := protocol.UnmarshalTCPWorkReady(frame.Body); err != nil {
		return nil, err
	}
	return conn, nil
}

func (c *Client) readWorkFrame(conn net.Conn, state *sessionState) (protocol.Frame, error) {
	frame, err := c.readMessageWithState(conn, state, c.readTimeout)
	if err != nil {
		return protocol.Frame{}, err
	}
	if frame.Type == protocol.TypeError {
		return protocol.Frame{}, c.remoteError(frame)
	}
	return frame, nil
}
