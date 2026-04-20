package client

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"sync"
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
