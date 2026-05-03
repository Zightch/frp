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

	conns  map[string]net.Conn
	closed bool
}

func newTCPWorkPoolState() *tcpWorkPoolState {
	return &tcpWorkPoolState{
		conns: make(map[string]net.Conn),
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
	if _, exists := p.conns[id]; exists {
		return fmt.Errorf("tcp work connection %s already registered", id)
	}
	p.conns[id] = conn
	return nil
}

func (p *tcpWorkPoolState) Remove(conn net.Conn) bool {
	if conn == nil {
		return false
	}
	id := transport.ConnectionID(conn)

	p.mu.Lock()
	_, exists := p.conns[id]
	if exists {
		delete(p.conns, id)
	}
	p.mu.Unlock()
	return exists
}

func (p *tcpWorkPoolState) Count() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.conns)
}

func (p *tcpWorkPoolState) CloseAll() {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return
	}
	p.closed = true

	conns := make([]net.Conn, 0, len(p.conns))
	for _, conn := range p.conns {
		conns = append(conns, conn)
	}
	p.conns = make(map[string]net.Conn)
	p.mu.Unlock()

	for _, conn := range conns {
		_ = conn.Close()
	}
}
