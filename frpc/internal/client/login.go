package client

import (
	"crypto/sha256"
	"fmt"
	"net"

	appconfig "github.com/zightch/frp/frpc/internal/config"
	"github.com/zightch/frp/frps/pkg/protocol"
)

func (c *Client) login(conn net.Conn, token appconfig.Token) (*sessionState, error) {
	beginBody, err := protocol.MarshalAuthBegin(protocol.AuthBegin{
		TokenID:       token.ID,
		ClientVersion: c.version,
		Hostname:      hostname(),
		OS:            detectOS(),
		Arch:          detectArch(),
	})
	if err != nil {
		return nil, err
	}
	if err := c.writeMessage(conn, nil, protocol.Frame{
		Type:      protocol.TypeAuthBegin,
		RequestID: 1,
		Body:      beginBody,
	}); err != nil {
		return nil, err
	}

	frame, err := c.readLoginFrame(conn)
	if err != nil {
		return nil, err
	}
	if err := expectLoginReply(frame, protocol.TypeAuthChallenge, 1); err != nil {
		return nil, err
	}

	challenge, err := protocol.UnmarshalAuthChallenge(frame.Body)
	if err != nil {
		return nil, err
	}

	tokenHash := sha256.Sum256(token.Secret[:])
	response := authResponse(tokenHash, challenge.Nonce)
	finishBody, err := protocol.MarshalAuthFinish(protocol.AuthFinish{
		ChallengeID: challenge.ChallengeID,
		Response:    response,
	})
	if err != nil {
		return nil, err
	}
	if err := c.writeMessage(conn, nil, protocol.Frame{
		Type:      protocol.TypeAuthFinish,
		RequestID: 2,
		Body:      finishBody,
	}); err != nil {
		return nil, err
	}

	frame, err = c.readLoginFrame(conn)
	if err != nil {
		return nil, err
	}
	if err := expectLoginReply(frame, protocol.TypeServerHello, 2); err != nil {
		return nil, err
	}

	hello, err := protocol.UnmarshalServerHello(frame.Body)
	if err != nil {
		return nil, err
	}
	state := newSessionState(hello.HeartbeatIntervalMs)

	frame, err = c.readLoginFrame(conn)
	if err != nil {
		return nil, err
	}
	if frame.Type != protocol.TypeConfigPush {
		return nil, fmt.Errorf("expected config.push, got %s", frame.Type.String())
	}
	if err := c.applyConfigPush(conn, state, frame); err != nil {
		return nil, err
	}

	snapshot := state.snapshotValue()
	c.logger.Info(
		"login succeeded",
		"session_id", hello.SessionID,
		"heartbeat_interval_ms", hello.HeartbeatIntervalMs,
		"config_version", snapshot.ConfigVersion,
		"tunnel_count", len(snapshot.Tunnels),
	)

	return state, nil
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

func authResponse(tokenHash [32]byte, nonce [16]byte) [32]byte {
	var payload [48]byte
	copy(payload[:32], tokenHash[:])
	copy(payload[32:], nonce[:])
	return sha256.Sum256(payload[:])
}
