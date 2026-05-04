package client

import (
	"context"
	"fmt"
	"net"
	"sync/atomic"

	"github.com/zightch/frp/frps/pkg/protocol"
)

const maxConcurrentTCPWorkOpens = 64

func (c *Client) workPoolLoop(ctx context.Context, state *sessionState) error {
	if state == nil || state.tcpWorkPoolTargetValue() == 0 {
		return nil
	}

	wakeCh := make(chan struct{}, 1)
	errCh := make(chan error, 1)
	var opening atomic.Int32

	for {
		c.fillTCPWorkPool(ctx, state, wakeCh, errCh, &opening)
		select {
		case <-ctx.Done():
			return nil
		case err := <-errCh:
			if ctx.Err() != nil {
				return nil
			}
			return fmt.Errorf("tcp work pool failed: %v", err)
		case <-wakeCh:
		}
	}
}

func (c *Client) fillTCPWorkPool(ctx context.Context, state *sessionState, wakeCh chan struct{}, errCh chan error, opening *atomic.Int32) {
	if state == nil || opening == nil {
		return
	}

	target := int(state.tcpWorkPoolTargetValue())
	if target == 0 {
		return
	}

	inFlight := int(opening.Load())
	idle := state.tcpWorkConnCount()
	missing := target - idle - inFlight
	if missing <= 0 {
		return
	}

	budget := maxConcurrentTCPWorkOpens - inFlight
	if budget <= 0 {
		return
	}
	if missing > budget {
		missing = budget
	}

	for i := 0; i < missing; i++ {
		opening.Add(1)
		go c.openAndServeTCPWorkConn(ctx, state, wakeCh, errCh, opening)
	}
}

func (c *Client) openAndServeTCPWorkConn(ctx context.Context, state *sessionState, wakeCh chan struct{}, errCh chan error, opening *atomic.Int32) {
	defer func() {
		if opening != nil {
			opening.Add(-1)
		}
		c.notifyWake(wakeCh)
	}()

	conn, err := c.openTCPWorkConn(ctx, state)
	if err != nil {
		c.reportTCPWorkOpenError(ctx, errCh, err)
		return
	}
	if err := state.registerTCPWorkConn(conn); err != nil {
		_ = conn.Close()
		c.reportTCPWorkOpenError(ctx, errCh, err)
		return
	}
	go c.serveTCPWorkConn(ctx, state, conn, wakeCh)
}

func (c *Client) reportTCPWorkOpenError(ctx context.Context, errCh chan error, err error) {
	if ctx.Err() != nil || err == nil {
		return
	}
	select {
	case errCh <- err:
	default:
	}
}

func (c *Client) serveTCPWorkConn(ctx context.Context, state *sessionState, conn net.Conn, wakeCh chan struct{}) {
	defer func() {
		_ = conn.Close()
		_ = state.removeTCPWorkConn(conn)
	}()

	frame, err := c.readMessageWithState(conn, state, 0)
	if err != nil {
		c.notifyTCPWorkPoolWake(ctx, state.removeTCPWorkConn(conn), wakeCh)
		return
	}
	if frame.Type == protocol.TypeError {
		c.notifyTCPWorkPoolWake(ctx, state.removeTCPWorkConn(conn), wakeCh)
		return
	}
	if !state.markTCPWorkConnBusy(conn) {
		return
	}
	c.notifyTCPWorkPoolWake(ctx, true, wakeCh)

	_ = c.handleWorkStreamOpen(conn, state, frame)
}

func (c *Client) notifyTCPWorkPoolWake(ctx context.Context, removed bool, wakeCh chan struct{}) {
	if !removed || ctx.Err() != nil {
		return
	}

	c.notifyWake(wakeCh)
}

func (c *Client) notifyWake(wakeCh chan struct{}) {
	select {
	case wakeCh <- struct{}{}:
	default:
	}
}
