package bind

import (
	"context"
	"net"
	"sync"
)

type UDPListener interface {
	Close() error
	LocalAddr() net.Addr
	ReadFromUDP([]byte) (int, *net.UDPAddr, error)
	WriteToUDP([]byte, *net.UDPAddr) (int, error)
}

func (netListenerFactory) ListenUDP(_ context.Context, _ ListenerBind, addr *net.UDPAddr) (UDPListener, error) {
	return net.ListenUDP("udp", addr)
}

func (f *ScriptedListenerFactory) ListenUDP(_ context.Context, bind ListenerBind, addr *net.UDPAddr) (UDPListener, error) {
	if err := f.beforeBind("listen_udp", bind); err != nil {
		return nil, err
	}
	return newFakeUDPListener(f, bind, addr), nil
}

type fakeUDPListener struct {
	factory *ScriptedListenerFactory
	bind    ListenerBind
	addr    *net.UDPAddr
	closed  chan struct{}
	once    sync.Once
}

func newFakeUDPListener(factory *ScriptedListenerFactory, bind ListenerBind, addr *net.UDPAddr) *fakeUDPListener {
	return &fakeUDPListener{
		factory: factory,
		bind:    bind,
		addr: &net.UDPAddr{
			IP:   append(net.IP(nil), addr.IP...),
			Port: addr.Port,
			Zone: addr.Zone,
		},
		closed: make(chan struct{}),
	}
}

func (l *fakeUDPListener) Close() error {
	l.once.Do(func() {
		close(l.closed)
		if l.factory != nil {
			l.factory.release(l.bind)
		}
	})
	return nil
}

func (l *fakeUDPListener) LocalAddr() net.Addr {
	return l.addr
}

func (l *fakeUDPListener) ReadFromUDP(_ []byte) (int, *net.UDPAddr, error) {
	<-l.closed
	return 0, nil, net.ErrClosed
}

func (l *fakeUDPListener) WriteToUDP(payload []byte, _ *net.UDPAddr) (int, error) {
	select {
	case <-l.closed:
		return 0, net.ErrClosed
	default:
		return len(payload), nil
	}
}

var _ UDPListener = (*net.UDPConn)(nil)
