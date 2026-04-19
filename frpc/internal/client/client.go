package client

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"sync"
	"sync/atomic"
	"time"

	appconfig "github.com/zightch/frp/frpc/internal/config"
	"github.com/zightch/frp/frps/pkg/protocol"
	"github.com/zightch/frp/frps/pkg/transport"
)

const (
	defaultDialTimeout = 5 * time.Second
	defaultReadTimeout = 5 * time.Second
	defaultBackoff     = 1 * time.Second
	maxBackoff         = 30 * time.Second
)

type dialFunc func(context.Context, string, string) (net.Conn, error)

type Client struct {
	config      appconfig.Config
	logger      *slog.Logger
	version     string
	dialContext dialFunc
	readTimeout time.Duration
}

type sessionState struct {
	heartbeatInterval time.Duration
	readTimeout       time.Duration

	writeMu sync.Mutex

	nextRequestID          atomic.Uint32
	lastAckedConfigVersion atomic.Uint64
	activeStreams          atomic.Uint32
	activeUDPSessions      atomic.Uint32

	snapshotMu sync.RWMutex
	snapshot   protocol.ConfigPush

	streamMu sync.Mutex
	streams  map[uint32]*localStream

	udpMu       sync.Mutex
	udpSessions map[uint32]*localUDPSession
}

func New(cfg appconfig.Config, logger *slog.Logger, version string) *Client {
	dialer := &net.Dialer{Timeout: defaultDialTimeout}
	return &Client{
		config:      cfg,
		logger:      logger,
		version:     version,
		dialContext: dialer.DialContext,
		readTimeout: defaultReadTimeout,
	}
}

func (c *Client) Run(ctx context.Context) error {
	if err := c.config.Validate(); err != nil {
		return err
	}

	token, err := appconfig.ParseToken(c.config.Token)
	if err != nil {
		return err
	}

	backoff := defaultBackoff
	for {
		err := c.runOnce(ctx, token)
		if ctx.Err() != nil {
			return nil
		}
		if err == nil {
			return nil
		}

		c.logger.Warn("frpc session ended", "error", err, "retry_in", backoff.String())
		timer := time.NewTimer(backoff)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil
		case <-timer.C:
		}

		backoff *= 2
		if backoff > maxBackoff {
			backoff = maxBackoff
		}
	}
}

func (c *Client) runOnce(ctx context.Context, token appconfig.Token) error {
	conn, err := c.dialContext(ctx, "tcp", c.config.Server)
	if err != nil {
		return err
	}
	defer conn.Close()

	c.logger.Info("connected to frps", "server", c.config.Server)
	err = c.runSession(ctx, conn, token)
	if err == nil {
		return nil
	}
	return err
}

func (c *Client) runSession(ctx context.Context, conn net.Conn, token appconfig.Token) error {
	state, err := c.login(conn, token)
	if err != nil {
		return err
	}

	sessionCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	defer state.closeAllStreams()
	defer state.closeAllUDPSessions()

	errCh := make(chan error, 2)
	go func() {
		errCh <- c.readLoop(sessionCtx, conn, state)
	}()
	go func() {
		errCh <- c.heartbeatLoop(sessionCtx, conn, state)
	}()

	select {
	case <-ctx.Done():
		_ = conn.Close()
		return nil
	case err := <-errCh:
		cancel()
		_ = conn.Close()
		if errors.Is(err, context.Canceled) || errors.Is(err, net.ErrClosed) {
			return nil
		}
		return err
	}
}

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

	frame, err := c.readMessage(conn, c.readTimeout)
	if err != nil {
		return nil, err
	}
	if frame.Type == protocol.TypeError {
		return nil, c.remoteError(frame)
	}
	if frame.Type != protocol.TypeAuthChallenge {
		return nil, fmt.Errorf("expected auth.challenge, got %s", frame.Type.String())
	}
	if frame.RequestID != 1 || frame.StreamID != 0 {
		return nil, fmt.Errorf("unexpected auth.challenge header: requestId=%d streamId=%d", frame.RequestID, frame.StreamID)
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

	frame, err = c.readMessage(conn, c.readTimeout)
	if err != nil {
		return nil, err
	}
	if frame.Type == protocol.TypeError {
		return nil, c.remoteError(frame)
	}
	if frame.Type != protocol.TypeServerHello {
		return nil, fmt.Errorf("expected server.hello, got %s", frame.Type.String())
	}
	if frame.RequestID != 2 || frame.StreamID != 0 {
		return nil, fmt.Errorf("unexpected server.hello header: requestId=%d streamId=%d", frame.RequestID, frame.StreamID)
	}

	hello, err := protocol.UnmarshalServerHello(frame.Body)
	if err != nil {
		return nil, err
	}
	state := newSessionState(hello.HeartbeatIntervalMs)

	frame, err = c.readMessage(conn, c.readTimeout)
	if err != nil {
		return nil, err
	}
	if frame.Type == protocol.TypeError {
		return nil, c.remoteError(frame)
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

func (c *Client) readLoop(ctx context.Context, conn net.Conn, state *sessionState) error {
	for {
		frame, err := c.readMessage(conn, state.readTimeout)
		if err != nil {
			return err
		}

		switch frame.Type {
		case protocol.TypeHeartbeatPong:
			if _, err := protocol.UnmarshalHeartbeatPong(frame.Body); err != nil {
				return err
			}
		case protocol.TypeConfigPush:
			if err := c.applyConfigPush(conn, state, frame); err != nil {
				return err
			}
		case protocol.TypeStreamOpen:
			if err := c.handleStreamOpen(conn, state, frame); err != nil {
				return err
			}
		case protocol.TypeStreamData:
			if err := c.handleStreamData(conn, state, frame); err != nil {
				return err
			}
		case protocol.TypeStreamClose:
			if err := c.handleStreamClose(state, frame); err != nil {
				return err
			}
		case protocol.TypeUDPOpen:
			if err := c.handleUDPOpen(conn, state, frame); err != nil {
				return err
			}
		case protocol.TypeUDPData:
			if err := c.handleUDPData(conn, state, frame); err != nil {
				return err
			}
		case protocol.TypeUDPClose:
			if err := c.handleUDPClose(state, frame); err != nil {
				return err
			}
		case protocol.TypeError:
			return c.remoteError(frame)
		default:
			return fmt.Errorf("unsupported server message type %s", frame.Type.String())
		}

		select {
		case <-ctx.Done():
			return nil
		default:
		}
	}
}

func (c *Client) heartbeatLoop(ctx context.Context, conn net.Conn, state *sessionState) error {
	ticker := time.NewTicker(state.heartbeatInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			body, err := protocol.MarshalHeartbeatPing(protocol.HeartbeatPing{
				ClientUnixMs:           uint64(time.Now().UTC().UnixMilli()),
				ActiveStreams:          state.activeStreams.Load(),
				ActiveUDPSessions:      state.activeUDPSessions.Load(),
				LastAckedConfigVersion: state.lastAckedConfigVersion.Load(),
			})
			if err != nil {
				return err
			}
			if err := c.writeMessage(conn, &state.writeMu, protocol.Frame{
				Type:      protocol.TypeHeartbeatPing,
				RequestID: state.nextClientRequestID(),
				Body:      body,
			}); err != nil {
				return err
			}
		}
	}
}

func (c *Client) applyConfigPush(conn net.Conn, state *sessionState, frame protocol.Frame) error {
	if frame.RequestID == 0 {
		return fmt.Errorf("config.push requestId must be non-zero")
	}
	if frame.StreamID != 0 {
		return fmt.Errorf("config.push streamId must be zero")
	}

	push, err := protocol.UnmarshalConfigPush(frame.Body)
	if err != nil {
		return err
	}

	ackBody, err := protocol.MarshalConfigAck(protocol.ConfigAck{
		ConfigVersion: push.ConfigVersion,
		AppliedAtMs:   uint64(time.Now().UTC().UnixMilli()),
		Status:        protocol.StatusOK,
	})
	if err != nil {
		return err
	}

	state.setSnapshot(push)
	if err := c.writeMessage(conn, &state.writeMu, protocol.Frame{
		Type:      protocol.TypeConfigAck,
		RequestID: frame.RequestID,
		Body:      ackBody,
	}); err != nil {
		return err
	}
	state.lastAckedConfigVersion.Store(push.ConfigVersion)
	c.logger.Info("config applied", "config_version", push.ConfigVersion, "tunnel_count", len(push.Tunnels))
	return nil
}

func (c *Client) readMessage(conn net.Conn, timeout time.Duration) (protocol.Frame, error) {
	frameBytes, err := transport.ReadFrame(conn, timeout)
	if err != nil {
		return protocol.Frame{}, err
	}
	return protocol.ParseFrame(frameBytes)
}

func (c *Client) writeMessage(conn net.Conn, writeMu *sync.Mutex, frame protocol.Frame) error {
	if writeMu != nil {
		writeMu.Lock()
		defer writeMu.Unlock()
	}

	frameBytes, err := frame.MarshalBinary()
	if err != nil {
		return err
	}
	return transport.WriteFrame(conn, frameBytes, c.readTimeout)
}

func (c *Client) remoteError(frame protocol.Frame) error {
	errorBody, err := protocol.UnmarshalErrorBody(frame.Body)
	if err != nil {
		return err
	}
	return fmt.Errorf("frps error %d: %s", errorBody.ErrorCode, errorBody.Message)
}

func authResponse(tokenHash [32]byte, nonce [16]byte) [32]byte {
	var payload [48]byte
	copy(payload[:32], tokenHash[:])
	copy(payload[32:], nonce[:])
	return sha256.Sum256(payload[:])
}

func newSessionState(heartbeatIntervalMs uint32) *sessionState {
	heartbeatInterval := time.Duration(heartbeatIntervalMs) * time.Millisecond
	if heartbeatInterval <= 0 {
		heartbeatInterval = 15 * time.Second
	}

	state := &sessionState{
		heartbeatInterval: heartbeatInterval,
		readTimeout:       sessionReadTimeout(heartbeatInterval),
		streams:           make(map[uint32]*localStream),
		udpSessions:       make(map[uint32]*localUDPSession),
	}
	state.nextRequestID.Store(2)
	return state
}

func sessionReadTimeout(heartbeatInterval time.Duration) time.Duration {
	timeout := heartbeatInterval * 3
	if timeout < defaultReadTimeout {
		return defaultReadTimeout
	}
	return timeout
}

func (s *sessionState) nextClientRequestID() uint32 {
	requestID := s.nextRequestID.Add(1)
	if requestID == 0 {
		s.nextRequestID.Store(1)
		requestID = s.nextRequestID.Add(1)
	}
	return requestID
}

func (s *sessionState) setSnapshot(snapshot protocol.ConfigPush) {
	s.snapshotMu.Lock()
	defer s.snapshotMu.Unlock()
	s.snapshot = snapshot
}

func (s *sessionState) snapshotValue() protocol.ConfigPush {
	s.snapshotMu.RLock()
	defer s.snapshotMu.RUnlock()
	return s.snapshot
}
