package client

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"sync"
	"sync/atomic"
	"time"

	appconfig "github.com/zightch/frp/frpc/internal/config"
	"github.com/zightch/frp/frps/pkg/protocol"
	"github.com/zightch/frp/frps/pkg/testsupport"
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
	frameIO     transport.FrameIO

	attempt atomic.Uint64

	stateMu sync.RWMutex
	state   *sessionState
}

func New(cfg appconfig.Config, logger *slog.Logger, version string) *Client {
	dialer := &net.Dialer{Timeout: defaultDialTimeout}
	return &Client{
		config:      cfg,
		logger:      logger,
		version:     version,
		dialContext: dialer.DialContext,
		readTimeout: defaultReadTimeout,
		frameIO:     transport.RealFrameIO{},
	}
}

func (c *Client) Run(ctx context.Context) error {
	if err := c.config.Validate(); err != nil {
		return err
	}

	credentials, err := appconfig.ParseCredentials(c.config.ClientID, c.config.ClientSecret)
	if err != nil {
		return err
	}

	backoff := defaultBackoff
	for {
		c.attempt.Add(1)
		err := c.runOnce(ctx, credentials)
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

func (c *Client) runOnce(ctx context.Context, credentials appconfig.Credentials) error {
	conn, err := c.dialContext(ctx, "tcp", c.config.Server)
	if err != nil {
		return err
	}
	defer conn.Close()

	c.logger.Info("connected to frps", "server", c.config.Server)
	err = c.runSession(ctx, conn, credentials)
	if err == nil {
		return nil
	}
	return err
}

func (c *Client) runSession(ctx context.Context, conn net.Conn, credentials appconfig.Credentials) error {
	state, err := c.login(conn, credentials)
	if err != nil {
		return err
	}
	c.stateMu.Lock()
	c.state = state
	c.stateMu.Unlock()
	defer func() {
		c.stateMu.Lock()
		if c.state == state {
			c.state = nil
		}
		c.stateMu.Unlock()
	}()

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
	if err := validateConfigPush(push); err != nil {
		return err
	}

	reloadSummary := state.applyReloadedSnapshot(push)
	if len(push.Tunnels) == 0 {
		state.setRecoveryMode(testsupport.RecoveryModeEmptyConfig)
	} else {
		state.setRecoveryMode(testsupport.RecoveryModeRunning)
	}
	ackBody, err := protocol.MarshalConfigAck(protocol.ConfigAck{
		ConfigVersion: push.ConfigVersion,
		AppliedAtMs:   uint64(time.Now().UTC().UnixMilli()),
		Status:        protocol.StatusOK,
	})
	if err != nil {
		return err
	}

	if err := c.writeMessageWithState(conn, state, &state.writeMu, protocol.Frame{
		Type:      protocol.TypeConfigAck,
		RequestID: frame.RequestID,
		Body:      ackBody,
	}); err != nil {
		return err
	}
	state.lastAckedConfigVersion.Store(push.ConfigVersion)
	c.logger.Info(
		"config applied",
		"config_version", push.ConfigVersion,
		"tunnel_count", len(push.Tunnels),
		"added_tunnels", reloadSummary.addedTunnels,
		"removed_tunnels", reloadSummary.removedTunnels,
		"replaced_tunnels", reloadSummary.replacedTunnels,
		"unchanged_tunnels", reloadSummary.unchangedTunnels,
		"closed_streams", reloadSummary.closedStreams,
		"closed_udp_sessions", reloadSummary.closedUDPSessions,
	)
	return nil
}

func (c *Client) readMessage(conn net.Conn, timeout time.Duration) (protocol.Frame, error) {
	return c.readMessageWithState(conn, nil, timeout)
}

func (c *Client) readMessageWithState(conn net.Conn, state *sessionState, timeout time.Duration) (protocol.Frame, error) {
	frameBytes, err := c.frameIO.ReadFrame(conn, timeout, c.frameContext(conn, state))
	if err != nil {
		return protocol.Frame{}, err
	}
	return protocol.ParseFrame(frameBytes)
}

func (c *Client) writeMessage(conn net.Conn, writeMu *sync.Mutex, frame protocol.Frame) error {
	return c.writeMessageWithState(conn, nil, writeMu, frame)
}

func (c *Client) writeMessageWithState(conn net.Conn, state *sessionState, writeMu *sync.Mutex, frame protocol.Frame) error {
	if writeMu != nil {
		writeMu.Lock()
		defer writeMu.Unlock()
	}

	frameBytes, err := frame.MarshalBinary()
	if err != nil {
		return err
	}
	return c.frameIO.WriteFrame(conn, frameBytes, c.readTimeout, c.frameContext(conn, state))
}

func (c *Client) remoteError(frame protocol.Frame) error {
	errorBody, err := protocol.UnmarshalErrorBody(frame.Body)
	if err != nil {
		return err
	}
	return fmt.Errorf("frps error %d: %s", errorBody.ErrorCode, errorBody.Message)
}

func (c *Client) frameContext(conn net.Conn, state *sessionState) transport.FrameContext {
	frameContext := transport.FrameContext{
		Side:   transport.FrameSideClient,
		ConnID: transport.ConnectionID(conn),
	}
	if state != nil {
		frameContext.SessionID = state.sessionID
	}
	return frameContext
}

func (c *Client) ObserveState() testsupport.ClientObservedState {
	state := c.currentState()
	if state == nil {
		return testsupport.ClientObservedState{
			Attempt:      c.attempt.Load(),
			RecoveryMode: testsupport.RecoveryModeReconnect,
		}
	}
	return state.observeState(c.attempt.Load())
}

func (c *Client) currentState() *sessionState {
	c.stateMu.RLock()
	defer c.stateMu.RUnlock()
	return c.state
}
