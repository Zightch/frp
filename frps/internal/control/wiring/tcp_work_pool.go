package wiring

import (
	"errors"
	"fmt"
	"net"
	"sync"

	"github.com/zightch/frp/frps/pkg/transport"
)

var errTCPWorkPoolClosed = errors.New("tcp work pool closed")

type tcpWorkConnHandle struct {
	id   string
	conn net.Conn

	done      chan struct{}
	closeOnce sync.Once
}

func newTCPWorkConnHandle(conn net.Conn) (*tcpWorkConnHandle, error) {
	if conn == nil {
		return nil, fmt.Errorf("tcp work connection is nil")
	}
	id := transport.ConnectionID(conn)
	if id == "" {
		return nil, fmt.Errorf("tcp work connection id is empty")
	}
	return &tcpWorkConnHandle{
		id:   id,
		conn: conn,
		done: make(chan struct{}),
	}, nil
}

func (h *tcpWorkConnHandle) Done() <-chan struct{} {
	if h == nil || h.done == nil {
		ch := make(chan struct{})
		close(ch)
		return ch
	}
	return h.done
}

func (h *tcpWorkConnHandle) closeConn() {
	if h == nil {
		return
	}
	h.closeOnce.Do(func() {
		if h.conn != nil {
			_ = h.conn.Close()
		}
		if h.done != nil {
			close(h.done)
		}
	})
}

type tcpWorkConnPool struct {
	mu sync.Mutex

	idleOrder []string
	idle      map[string]*tcpWorkConnHandle
	busy      map[string]*tcpWorkConnHandle
	closed    bool
}

func newTCPWorkConnPool() *tcpWorkConnPool {
	return &tcpWorkConnPool{
		idle: make(map[string]*tcpWorkConnHandle),
		busy: make(map[string]*tcpWorkConnHandle),
	}
}

func (p *tcpWorkConnPool) Register(conn net.Conn) (*tcpWorkConnHandle, error) {
	handle, err := newTCPWorkConnHandle(conn)
	if err != nil {
		return nil, err
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	if p.closed {
		return nil, errTCPWorkPoolClosed
	}
	if _, exists := p.idle[handle.id]; exists {
		return nil, fmt.Errorf("tcp work connection %s already registered", handle.id)
	}
	if _, exists := p.busy[handle.id]; exists {
		return nil, fmt.Errorf("tcp work connection %s already registered", handle.id)
	}

	p.idle[handle.id] = handle
	p.idleOrder = append(p.idleOrder, handle.id)
	return handle, nil
}

func (p *tcpWorkConnPool) Acquire() (net.Conn, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.closed {
		return nil, false
	}

	for len(p.idleOrder) > 0 {
		id := p.idleOrder[0]
		p.idleOrder = p.idleOrder[1:]

		handle, exists := p.idle[id]
		if !exists {
			continue
		}
		delete(p.idle, id)
		p.busy[id] = handle
		return handle.conn, true
	}
	return nil, false
}

func (p *tcpWorkConnPool) Release(conn net.Conn) bool {
	if conn == nil {
		return false
	}

	id := transport.ConnectionID(conn)

	p.mu.Lock()
	handle, exists := p.busy[id]
	if !exists {
		p.mu.Unlock()
		return false
	}
	delete(p.busy, id)
	if p.closed {
		p.mu.Unlock()
		handle.closeConn()
		return false
	}
	p.idle[id] = handle
	p.idleOrder = append(p.idleOrder, id)
	p.mu.Unlock()
	return true
}

func (p *tcpWorkConnPool) Retire(conn net.Conn) bool {
	if conn == nil {
		return false
	}

	id := transport.ConnectionID(conn)

	p.mu.Lock()
	handle := p.idle[id]
	if handle != nil {
		delete(p.idle, id)
	}
	if handle == nil {
		handle = p.busy[id]
		if handle != nil {
			delete(p.busy, id)
		}
	}
	p.mu.Unlock()

	if handle == nil {
		return false
	}
	handle.closeConn()
	return true
}

func (p *tcpWorkConnPool) CloseAll() {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return
	}
	p.closed = true

	handles := make([]*tcpWorkConnHandle, 0, len(p.idle)+len(p.busy))
	for _, handle := range p.idle {
		handles = append(handles, handle)
	}
	for _, handle := range p.busy {
		handles = append(handles, handle)
	}
	p.idle = make(map[string]*tcpWorkConnHandle)
	p.busy = make(map[string]*tcpWorkConnHandle)
	p.idleOrder = nil
	p.mu.Unlock()

	for _, handle := range handles {
		handle.closeConn()
	}
}

func (p *tcpWorkConnPool) Counts() (idle int, busy int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.idle), len(p.busy)
}
