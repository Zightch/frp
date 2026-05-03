package client

import (
	"context"
	"fmt"
	"net"
)

func (c *Client) workPoolLoop(ctx context.Context, state *sessionState) error {
	if state == nil || state.tcpWorkPoolTargetValue() == 0 {
		return nil
	}

	wakeCh := make(chan struct{}, 1)
	for {
		if err := c.fillTCPWorkPool(ctx, state, wakeCh); err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return fmt.Errorf("tcp work pool failed: %v", err)
		}

		select {
		case <-ctx.Done():
			return nil
		case <-wakeCh:
		}
	}
}

func (c *Client) fillTCPWorkPool(ctx context.Context, state *sessionState, wakeCh chan struct{}) error {
	for {
		target := int(state.tcpWorkPoolTargetValue())
		if target == 0 || state.tcpWorkConnCount() >= target {
			return nil
		}

		conn, err := c.openTCPWorkConn(ctx, state)
		if err != nil {
			return err
		}
		if err := state.registerTCPWorkConn(conn); err != nil {
			_ = conn.Close()
			return err
		}
		go c.watchTCPWorkConn(ctx, state, conn, wakeCh)
	}
}

func (c *Client) watchTCPWorkConn(ctx context.Context, state *sessionState, conn net.Conn, wakeCh chan struct{}) {
	buffer := make([]byte, 1)
	_, _ = conn.Read(buffer)

	removed := state.removeTCPWorkConn(conn)
	if !removed || ctx.Err() != nil {
		return
	}

	select {
	case wakeCh <- struct{}{}:
	default:
	}
}
