package bind

import (
	"context"
	"net"
	"strconv"
	"sync"
	"syscall"

	"github.com/zightch/frp/frps/pkg/testsupport"
)

type ListenKey struct {
	Protocol string
	IP       string
	Port     uint16
}

type BindKind string

const (
	BindKindRuntimeProbe BindKind = "runtime_probe"
	BindKindRuntimeStart BindKind = "runtime_start"
)

type ListenerBind struct {
	GroupID       int64
	TunnelID      uint32
	ConfigVersion uint64
	Kind          BindKind
	Key           ListenKey
}

type ListenerFactory interface {
	ListenTCP(ctx context.Context, bind ListenerBind) (net.Listener, error)
	ResolveUDP(ctx context.Context, bind ListenerBind) (*net.UDPAddr, error)
	ListenUDP(ctx context.Context, bind ListenerBind, addr *net.UDPAddr) (UDPListener, error)
}

type netListenerFactory struct{}

type ScriptedListenerFactory struct {
	mu             sync.Mutex
	callSeq        int
	failures       []ScriptedListenerFailure
	calls          []ListenerCall
	externalOwners map[ListenKey]struct{}
	handles        map[ListenKey]int
}

type ListenerCall struct {
	Sequence int
	Op       string
	Bind     ListenerBind
}

type ScriptedListenerFailure struct {
	Op         string
	Kind       BindKind
	Key        ListenKey
	Occurrence int
	Err        error
	used       int
}

func NewNetListenerFactory() ListenerFactory {
	return netListenerFactory{}
}

func NewScriptedListenerFactory() *ScriptedListenerFactory {
	return &ScriptedListenerFactory{
		externalOwners: make(map[ListenKey]struct{}),
		handles:        make(map[ListenKey]int),
	}
}

func (netListenerFactory) ListenTCP(_ context.Context, bind ListenerBind) (net.Listener, error) {
	addr := net.JoinHostPort(bind.Key.IP, strconv.Itoa(int(bind.Key.Port)))
	return net.Listen("tcp", addr)
}

func (netListenerFactory) ResolveUDP(_ context.Context, bind ListenerBind) (*net.UDPAddr, error) {
	addr := net.JoinHostPort(bind.Key.IP, strconv.Itoa(int(bind.Key.Port)))
	return net.ResolveUDPAddr("udp", addr)
}

func (f *ScriptedListenerFactory) ListenTCP(_ context.Context, bind ListenerBind) (net.Listener, error) {
	if err := f.beforeBind("listen_tcp", bind); err != nil {
		return nil, err
	}
	return newFakeTCPListener(f, bind), nil
}

func (f *ScriptedListenerFactory) ResolveUDP(_ context.Context, bind ListenerBind) (*net.UDPAddr, error) {
	if err := f.beforeResolve("resolve_udp", bind); err != nil {
		return nil, err
	}
	return &net.UDPAddr{IP: net.ParseIP(bind.Key.IP), Port: int(bind.Key.Port)}, nil
}

func (f *ScriptedListenerFactory) SetExternallyOccupied(key ListenKey, occupied bool) {
	if f == nil {
		return
	}

	f.mu.Lock()
	defer f.mu.Unlock()

	if occupied {
		f.externalOwners[key] = struct{}{}
		return
	}
	delete(f.externalOwners, key)
}

func (f *ScriptedListenerFactory) AddFailure(rule ScriptedListenerFailure) {
	if f == nil {
		return
	}

	f.mu.Lock()
	defer f.mu.Unlock()
	f.failures = append(f.failures, rule)
}

func (f *ScriptedListenerFactory) Calls() []ListenerCall {
	if f == nil {
		return nil
	}

	f.mu.Lock()
	defer f.mu.Unlock()

	if len(f.calls) == 0 {
		return nil
	}
	cloned := make([]ListenerCall, len(f.calls))
	copy(cloned, f.calls)
	return cloned
}

func (f *ScriptedListenerFactory) ObserveState() testsupport.ListenerWorldObservedState {
	if f == nil {
		return testsupport.ListenerWorldObservedState{}
	}

	f.mu.Lock()
	defer f.mu.Unlock()

	state := testsupport.ListenerWorldObservedState{}
	if len(f.externalOwners) > 0 {
		state.Occupied = make([]testsupport.ListenerOccupancyObservedState, 0, len(f.externalOwners))
		for key := range f.externalOwners {
			state.Occupied = append(state.Occupied, testsupport.ListenerOccupancyObservedState{
				Protocol: key.Protocol,
				IP:       key.IP,
				Port:     key.Port,
				Owner:    "external",
			})
		}
	}
	if len(f.handles) > 0 {
		state.Handles = make([]testsupport.ListenerHandleObservedState, 0, len(f.handles))
		for key, count := range f.handles {
			state.Handles = append(state.Handles, testsupport.ListenerHandleObservedState{
				Protocol: key.Protocol,
				IP:       key.IP,
				Port:     key.Port,
				Count:    count,
			})
		}
	}
	if len(f.calls) > 0 {
		state.Calls = make([]testsupport.ListenerCallObservedState, len(f.calls))
		for index, call := range f.calls {
			state.Calls[index] = testsupport.ListenerCallObservedState{
				Sequence:      call.Sequence,
				Op:            call.Op,
				GroupID:       call.Bind.GroupID,
				TunnelID:      call.Bind.TunnelID,
				ConfigVersion: call.Bind.ConfigVersion,
				Kind:          string(call.Bind.Kind),
				Protocol:      call.Bind.Key.Protocol,
				IP:            call.Bind.Key.IP,
				Port:          call.Bind.Key.Port,
			}
		}
	}
	return state
}

func (f *ScriptedListenerFactory) beforeResolve(op string, bind ListenerBind) error {
	if f == nil {
		return nil
	}

	f.mu.Lock()
	defer f.mu.Unlock()
	f.recordCallLocked(op, bind)
	return f.matchFailureLocked(op, bind)
}

func (f *ScriptedListenerFactory) beforeBind(op string, bind ListenerBind) error {
	if f == nil {
		return nil
	}

	f.mu.Lock()
	defer f.mu.Unlock()
	f.recordCallLocked(op, bind)
	if err := f.matchFailureLocked(op, bind); err != nil {
		return err
	}
	if _, occupied := f.externalOwners[bind.Key]; occupied {
		return listenAddressInUse(bind)
	}
	if f.handles[bind.Key] > 0 {
		return listenAddressInUse(bind)
	}
	f.handles[bind.Key]++
	return nil
}

func (f *ScriptedListenerFactory) release(bind ListenerBind) {
	if f == nil {
		return
	}

	f.mu.Lock()
	defer f.mu.Unlock()

	count := f.handles[bind.Key]
	if count <= 1 {
		delete(f.handles, bind.Key)
		return
	}
	f.handles[bind.Key] = count - 1
}

func (f *ScriptedListenerFactory) recordCallLocked(op string, bind ListenerBind) {
	f.callSeq++
	f.calls = append(f.calls, ListenerCall{
		Sequence: f.callSeq,
		Op:       op,
		Bind:     bind,
	})
}

func (f *ScriptedListenerFactory) matchFailureLocked(op string, bind ListenerBind) error {
	for index := range f.failures {
		rule := &f.failures[index]
		if rule.Op != "" && rule.Op != op {
			continue
		}
		if rule.Kind != "" && rule.Kind != bind.Kind {
			continue
		}
		if rule.Key != (ListenKey{}) && rule.Key != bind.Key {
			continue
		}
		rule.used++
		if rule.Occurrence > 0 && rule.used != rule.Occurrence {
			continue
		}
		return rule.Err
	}
	return nil
}

type fakeTCPListener struct {
	factory *ScriptedListenerFactory
	bind    ListenerBind
	addr    *net.TCPAddr
	closed  chan struct{}
	once    sync.Once
}

func newFakeTCPListener(factory *ScriptedListenerFactory, bind ListenerBind) *fakeTCPListener {
	return &fakeTCPListener{
		factory: factory,
		bind:    bind,
		addr: &net.TCPAddr{
			IP:   net.ParseIP(bind.Key.IP),
			Port: int(bind.Key.Port),
		},
		closed: make(chan struct{}),
	}
}

func (l *fakeTCPListener) Accept() (net.Conn, error) {
	<-l.closed
	return nil, net.ErrClosed
}

func (l *fakeTCPListener) Close() error {
	l.once.Do(func() {
		close(l.closed)
		if l.factory != nil {
			l.factory.release(l.bind)
		}
	})
	return nil
}

func (l *fakeTCPListener) Addr() net.Addr {
	return l.addr
}

func listenAddressInUse(bind ListenerBind) error {
	return &net.OpError{
		Op:   "listen",
		Net:  bind.Key.Protocol,
		Addr: listenAddr(bind),
		Err:  syscall.EADDRINUSE,
	}
}

func listenAddr(bind ListenerBind) net.Addr {
	switch bind.Key.Protocol {
	case "udp":
		return &net.UDPAddr{IP: net.ParseIP(bind.Key.IP), Port: int(bind.Key.Port)}
	default:
		return &net.TCPAddr{IP: net.ParseIP(bind.Key.IP), Port: int(bind.Key.Port)}
	}
}
