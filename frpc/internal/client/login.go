package client

import (
	"crypto/sha256"
	"crypto/tls"
	"fmt"
	"net"
	"time"

	appconfig "github.com/zightch/frp/frpc/internal/config"
	"github.com/zightch/frp/frps/pkg/protocol"
	"github.com/zightch/frp/frps/pkg/transport"
)

func (c *Client) login(conn net.Conn, credentials appconfig.Credentials) (net.Conn, *sessionState, error) {
	conn, err := c.negotiateTransport(conn, credentials.ClientID)
	if err != nil {
		return nil, nil, err
	}

	beginBody, err := protocol.MarshalAuthBegin(protocol.AuthBegin{
		ClientID:      credentials.ClientID,
		ClientVersion: c.version,
		Hostname:      hostname(),
		OS:            detectOS(),
		Arch:          detectArch(),
	})
	if err != nil {
		return nil, nil, err
	}
	if err := c.writeMessage(conn, nil, protocol.Frame{
		Type:      protocol.TypeAuthBegin,
		RequestID: 2,
		Body:      beginBody,
	}); err != nil {
		return nil, nil, err
	}

	frame, err := c.readLoginFrame(conn)
	if err != nil {
		return nil, nil, err
	}
	if err := expectLoginReply(frame, protocol.TypeAuthChallenge, 2); err != nil {
		return nil, nil, err
	}

	challenge, err := protocol.UnmarshalAuthChallenge(frame.Body)
	if err != nil {
		return nil, nil, err
	}

	secretHash := sha256.Sum256(credentials.ClientSecret[:])
	response := protocol.ChallengeResponse(secretHash, challenge.Nonce)
	finishBody, err := protocol.MarshalAuthFinish(protocol.AuthFinish{
		ChallengeID: challenge.ChallengeID,
		Response:    response,
	})
	if err != nil {
		return nil, nil, err
	}
	if err := c.writeMessage(conn, nil, protocol.Frame{
		Type:      protocol.TypeAuthFinish,
		RequestID: 3,
		Body:      finishBody,
	}); err != nil {
		return nil, nil, err
	}

	frame, err = c.readLoginFrame(conn)
	if err != nil {
		return nil, nil, err
	}
	if err := expectLoginReply(frame, protocol.TypeServerHello, 3); err != nil {
		return nil, nil, err
	}

	hello, err := protocol.UnmarshalServerHello(frame.Body)
	if err != nil {
		return nil, nil, err
	}
	state := newSessionState(hello.HeartbeatIntervalMs)
	state.setIdentity(hello.SessionID, transport.ConnectionID(conn))
	state.setTCPWorkConfig(hello.TCPWorkPoolSize, hello.TCPWorkSecret)
	c.infof("login", "登录成功 server=%s", c.config.Server)

	frame, err = c.readLoginFrame(conn)
	if err != nil {
		return nil, nil, err
	}
	if frame.Type != protocol.TypeConfigPush {
		return nil, nil, fmt.Errorf("expected config.push, got %s", frame.Type.String())
	}
	if err := c.applyConfigPush(conn, state, frame); err != nil {
		return nil, nil, err
	}
	c.markSessionActive()

	return conn, state, nil
}

func (c *Client) negotiateTransport(conn net.Conn, clientID [16]byte) (net.Conn, error) {
	helloBody, err := protocol.MarshalTransportClientHello(protocol.TransportClientHello{
		ClientID:               clientID,
		SupportedSecurityModes: protocol.TransportSecurityModePlain | protocol.TransportSecurityModeTLS,
	})
	if err != nil {
		return nil, err
	}
	if err := c.writeMessage(conn, nil, protocol.Frame{
		Type:      protocol.TypeTransportClientHello,
		RequestID: 1,
		Body:      helloBody,
	}); err != nil {
		return nil, err
	}

	frame, err := c.readLoginFrame(conn)
	if err != nil {
		return nil, err
	}
	if err := expectLoginReply(frame, protocol.TypeTransportServerHello, 1); err != nil {
		return nil, err
	}

	serverHello, err := protocol.UnmarshalTransportServerHello(frame.Body)
	if err != nil {
		return nil, err
	}
	switch serverHello.SelectedSecurityMode {
	case protocol.TransportSecurityModePlain:
		return conn, nil
	case protocol.TransportSecurityModeTLS:
		return c.upgradeConnToTLS(conn)
	default:
		return nil, fmt.Errorf("unsupported transport security mode %d", serverHello.SelectedSecurityMode)
	}
}

func (c *Client) upgradeConnToTLS(conn net.Conn) (net.Conn, error) {
	host, _, err := net.SplitHostPort(c.config.Server)
	if err != nil {
		return nil, err
	}

	config, err := c.tlsConfigForHost(host)
	if err != nil {
		return nil, err
	}
	tlsConn := tls.Client(conn, config)
	if err := conn.SetDeadline(time.Now().Add(c.readTimeout)); err != nil {
		return nil, err
	}
	if err := tlsConn.Handshake(); err != nil {
		_ = conn.SetDeadline(time.Time{})
		return nil, err
	}
	if err := conn.SetDeadline(time.Time{}); err != nil {
		return nil, err
	}
	return tlsConn, nil
}

func (c *Client) tlsConfigForHost(host string) (*tls.Config, error) {
	if c != nil && c.buildTLSConfig != nil {
		return c.buildTLSConfig(host)
	}
	return &tls.Config{
		MinVersion: tls.VersionTLS12,
		ServerName: host,
	}, nil
}

func (c *Client) readLoginFrame(conn net.Conn) (protocol.Frame, error) {
	frame, err := c.readMessage(conn, c.readTimeout)
	if err != nil {
		return protocol.Frame{}, err
	}
	if frame.Type == protocol.TypeError {
		return protocol.Frame{}, c.remoteError(frame)
	}
	return frame, nil
}

func expectLoginReply(frame protocol.Frame, expectedType protocol.Type, requestID uint32) error {
	if frame.Type != expectedType {
		return fmt.Errorf("expected %s, got %s", expectedType.String(), frame.Type.String())
	}
	if frame.RequestID != requestID || frame.StreamID != 0 {
		return fmt.Errorf(
			"unexpected %s header: requestId=%d streamId=%d",
			expectedType.String(),
			frame.RequestID,
			frame.StreamID,
		)
	}
	return nil
}
