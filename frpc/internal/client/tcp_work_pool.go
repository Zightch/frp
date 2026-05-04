package client

import (
	"errors"
	"fmt"
	"net"
	"sync"

	"github.com/zightch/frp/frps/pkg/transport"
)

var errTCPWorkPoolClosed = errors.New("tcp work pool closed")

type tcpWorkPoolState struct {
	mu sync.Mutex

	idle   map[string]net.Conn
	busy   map[string]net.Conn
	closed bool
}

func newTCPWorkPoolState() *tcpWorkPoolState {
	return &tcpWorkPoolState{
		idle: make(map[string]net.Conn),
		busy: make(map[string]net.Conn),
	}
}

func (p *tcpWorkPoolState) Register(conn net.Conn) error {
	if conn == nil {
		return fmt.Errorf("tcp work connection is nil")
	}
	id := transport.ConnectionID(conn)
	if id == "" {
		return fmt.Errorf("tcp work connection id is empty")
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	if p.closed {
		return errTCPWorkPoolClosed
	}
	if _, exists := p.idle[id]; exists {
		return fmt.Errorf("tcp work connection %s already registered", id)
	}
	if _, exists := p.busy[id]; exists {
		return fmt.Errorf("tcp work connection %s already registered", id)
	}
	p.idle[id] = conn
	return nil
}

func (p *tcpWorkPoolState) MarkBusy(conn net.Conn) bool {
	if conn == nil {
		return false
	}
	id := transport.ConnectionID(conn)

	p.mu.Lock()
	registered, exists := p.idle[id]
	if exists {
		delete(p.idle, id)
		p.busy[id] = registered
	}
	p.mu.Unlock()
	return exists
}

func (p *tcpWorkPoolState) Remove(conn net.Conn) bool {
	if conn == nil {
		return false
	}
	id := transport.ConnectionID(conn)

	p.mu.Lock()
	_, exists := p.idle[id]
	if exists {
		delete(p.idle, id)
	}
	if !exists {
		_, exists = p.busy[id]
		if exists {
			delete(p.busy, id)
		}
	}
	p.mu.Unlock()
	return exists
}

func (p *tcpWorkPoolState) IdleCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.idle)
}

func (p *tcpWorkPoolState) CloseAll() {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return
	}
	p.closed = true

	conns := make([]net.Conn, 0, len(p.idle)+len(p.busy))
	for _, conn := range p.idle {
		conns = append(conns, conn)
	}
	for _, conn := range p.busy {
		conns = append(conns, conn)
	}
	p.idle = make(map[string]net.Conn)
	p.busy = make(map[string]net.Conn)
	p.mu.Unlock()

	for _, conn := range conns {
		_ = conn.Close()
	}
}
