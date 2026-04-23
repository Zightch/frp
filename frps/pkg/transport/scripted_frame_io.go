package transport

import (
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/zightch/frp/frps/pkg/protocol"
	"github.com/zightch/frp/frps/pkg/testsupport"
)

type ScriptedRule struct {
	ConnID     string
	Direction  testsupport.FrameDirection
	FrameType  protocol.Type
	RequestID  uint32
	StreamID   uint32
	SessionID  uint64
	Occurrence int
	Action     testsupport.FrameAction
	Err        error
}

type ReleaseFilter struct {
	ConnID    string
	Direction testsupport.FrameDirection
	RequestID uint32
	FrameType string
}

type ScriptedFrameIO struct {
	mu        sync.Mutex
	nextSeq   uint64
	rules     []ScriptedRule
	ruleHits  map[int]int
	pairs     map[string]*scriptedPair
	delivered []testsupport.FrameObservedState
	delayed   []delayedFrame
	dropped   []testsupport.FrameObservedState
	errFrames []testsupport.FrameObservedState
}

type delayedFrame struct {
	state testsupport.FrameObservedState
	bytes []byte
	peer  *scriptedEndpoint
}

type scriptedPair struct {
	id     string
	client *scriptedEndpoint
	server *scriptedEndpoint
}

type scriptedEndpoint struct {
	id          string
	side        FrameSide
	inbox       chan []byte
	rawIOError  error
	readClosed  atomic.Bool
	writeClosed atomic.Bool
	closed      atomic.Bool
	closedCh    chan struct{}
	closeOnce   sync.Once
	localAddr   net.Addr
	remoteAddr  net.Addr
}

type ScriptedConn struct {
	transport *ScriptedFrameIO
	endpoint  *scriptedEndpoint
	pair      *scriptedPair
}

type scriptedAddr struct {
	network string
	address string
}

func NewScriptedFrameIO() *ScriptedFrameIO {
	return &ScriptedFrameIO{
		ruleHits: make(map[int]int),
		pairs:    make(map[string]*scriptedPair),
	}
}

func (s *ScriptedFrameIO) OpenConnPair(connID string) (net.Conn, net.Conn) {
	if connID == "" {
		connID = strconv.FormatInt(time.Now().UnixNano(), 10)
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	clientEndpoint := &scriptedEndpoint{
		id:         connID,
		side:       FrameSideClient,
		inbox:      make(chan []byte, 32),
		closedCh:   make(chan struct{}),
		rawIOError: errors.New("scripted conn raw io is unsupported; use FrameIO"),
		localAddr:  scriptedAddr{network: "scripted", address: connID + "/client"},
		remoteAddr: scriptedAddr{network: "scripted", address: connID + "/server"},
	}
	serverEndpoint := &scriptedEndpoint{
		id:         connID,
		side:       FrameSideServer,
		inbox:      make(chan []byte, 32),
		closedCh:   make(chan struct{}),
		rawIOError: errors.New("scripted conn raw io is unsupported; use FrameIO"),
		localAddr:  scriptedAddr{network: "scripted", address: connID + "/server"},
		remoteAddr: scriptedAddr{network: "scripted", address: connID + "/client"},
	}
	pair := &scriptedPair{
		id:     connID,
		client: clientEndpoint,
		server: serverEndpoint,
	}
	s.pairs[connID] = pair
	return &ScriptedConn{transport: s, endpoint: clientEndpoint, pair: pair}, &ScriptedConn{transport: s, endpoint: serverEndpoint, pair: pair}
}

func (s *ScriptedFrameIO) AddRule(rule ScriptedRule) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.rules = append(s.rules, rule)
}

func (s *ScriptedFrameIO) CloseRead(conn net.Conn) bool {
	scripted, ok := conn.(*ScriptedConn)
	if !ok || scripted == nil {
		return false
	}
	return scripted.transport.closeRead(scripted.endpoint)
}

func (s *ScriptedFrameIO) CloseWrite(conn net.Conn) bool {
	scripted, ok := conn.(*ScriptedConn)
	if !ok || scripted == nil {
		return false
	}
	return scripted.transport.closeWrite(scripted.endpoint)
}

func (s *ScriptedFrameIO) ReleaseDelayed(filter ReleaseFilter) int {
	if s == nil {
		return 0
	}

	s.mu.Lock()
	remaining := s.delayed[:0]
	toRelease := make([]delayedFrame, 0)
	for _, delayed := range s.delayed {
		if !matchesReleaseFilter(delayed.state, filter) {
			remaining = append(remaining, delayed)
			continue
		}
		toRelease = append(toRelease, delayed)
	}
	s.delayed = remaining
	s.mu.Unlock()

	released := 0
	for _, delayed := range toRelease {
		if s.deliverToPeer(delayed.peer, delayed.bytes) {
			released++
			s.mu.Lock()
			s.delivered = append(s.delivered, delayed.state)
			s.mu.Unlock()
		}
	}
	return released
}

func (s *ScriptedFrameIO) ObserveState() testsupport.TransportObservedState {
	if s == nil {
		return testsupport.TransportObservedState{}
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	state := testsupport.TransportObservedState{
		Delivered: cloneFrames(s.delivered),
		Dropped:   cloneFrames(s.dropped),
		Errors:    cloneFrames(s.errFrames),
	}
	if len(s.delayed) > 0 {
		state.Delayed = make([]testsupport.FrameObservedState, len(s.delayed))
		for index, delayed := range s.delayed {
			state.Delayed[index] = delayed.state
		}
	}
	if len(s.pairs) > 0 {
		state.Connections = make([]testsupport.TransportConnObservedState, 0, len(s.pairs))
		for _, pair := range s.pairs {
			delayedCount := 0
			for _, delayed := range s.delayed {
				if delayed.state.ConnID == pair.id {
					delayedCount++
				}
			}
			state.Connections = append(state.Connections, testsupport.TransportConnObservedState{
				ConnID:       pair.id,
				ClientState:  endpointState(pair.client),
				ServerState:  endpointState(pair.server),
				DelayedCount: delayedCount,
			})
		}
	}
	return state
}

func (s *ScriptedFrameIO) ReadFrame(conn net.Conn, timeout time.Duration, _ FrameContext) ([]byte, error) {
	scripted, ok := conn.(*ScriptedConn)
	if !ok || scripted == nil {
		return nil, fmt.Errorf("scripted frame transport requires scripted conn")
	}
	return scripted.transport.readFrame(scripted.endpoint, timeout)
}

func (s *ScriptedFrameIO) WriteFrame(conn net.Conn, frame []byte, _ time.Duration, context FrameContext) error {
	scripted, ok := conn.(*ScriptedConn)
	if !ok || scripted == nil {
		return fmt.Errorf("scripted frame transport requires scripted conn")
	}
	return scripted.transport.writeFrame(scripted.pair, scripted.endpoint, frame, context)
}

func (s *ScriptedFrameIO) readFrame(endpoint *scriptedEndpoint, timeout time.Duration) ([]byte, error) {
	if endpoint == nil {
		return nil, net.ErrClosed
	}
	if endpoint.isClosed() || endpoint.isReadClosed() {
		return nil, io.EOF
	}

	if timeout <= 0 {
		select {
		case <-endpoint.closedCh:
			return nil, io.EOF
		case payload := <-endpoint.inbox:
			return append([]byte(nil), payload...), nil
		}
	}

	timer := time.NewTimer(timeout)
	defer timer.Stop()

	select {
	case <-endpoint.closedCh:
		return nil, io.EOF
	case payload := <-endpoint.inbox:
		return append([]byte(nil), payload...), nil
	case <-timer.C:
		return nil, &timeoutError{}
	}
}

func (s *ScriptedFrameIO) writeFrame(pair *scriptedPair, source *scriptedEndpoint, frame []byte, context FrameContext) error {
	if pair == nil || source == nil {
		return net.ErrClosed
	}

	sourceState := endpointState(source)
	if sourceState == testsupport.TransportConnStateWriteClosed || sourceState == testsupport.TransportConnStateClosed {
		return net.ErrClosed
	}

	peer := pair.peer(source.side)
	if peer == nil || endpointState(peer) == testsupport.TransportConnStateReadClosed || endpointState(peer) == testsupport.TransportConnStateClosed {
		return io.EOF
	}

	s.mu.Lock()
	s.nextSeq++
	sequence := s.nextSeq
	s.mu.Unlock()

	observed := buildObservedFrame(source.id, directionFor(source.side), frame, context)
	observed.Sequence = sequence

	s.mu.Lock()
	rule := s.matchRuleLocked(observed)
	s.mu.Unlock()

	action := testsupport.FrameActionDeliver
	var actionErr error
	if rule != nil {
		if rule.Action != "" {
			action = rule.Action
		}
		actionErr = rule.Err
	}
	observed.Action = action

	switch action {
	case testsupport.FrameActionDrop:
		s.mu.Lock()
		s.dropped = append(s.dropped, observed)
		s.mu.Unlock()
		return nil
	case testsupport.FrameActionDelay:
		s.mu.Lock()
		s.delayed = append(s.delayed, delayedFrame{
			state: observed,
			bytes: append([]byte(nil), frame...),
			peer:  peer,
		})
		s.mu.Unlock()
		return nil
	case testsupport.FrameActionDuplicate:
		delivered := 0
		if s.deliverToPeer(peer, frame) {
			delivered++
		}
		if s.deliverToPeer(peer, frame) {
			delivered++
		}
		s.mu.Lock()
		for count := 0; count < delivered; count++ {
			s.delivered = append(s.delivered, observed)
		}
		s.mu.Unlock()
		return nil
	case testsupport.FrameActionError:
		if actionErr == nil {
			actionErr = net.ErrClosed
		}
		s.mu.Lock()
		s.errFrames = append(s.errFrames, observed)
		s.mu.Unlock()
		return actionErr
	default:
		if !s.deliverToPeer(peer, frame) {
			return io.EOF
		}
		s.mu.Lock()
		s.delivered = append(s.delivered, observed)
		s.mu.Unlock()
		return nil
	}
}

func (s *ScriptedFrameIO) matchRuleLocked(frame testsupport.FrameObservedState) *ScriptedRule {
	for index := range s.rules {
		rule := &s.rules[index]
		if rule.ConnID != "" && rule.ConnID != frame.ConnID {
			continue
		}
		if rule.Direction != "" && rule.Direction != frame.Direction {
			continue
		}
		if rule.FrameType != 0 && rule.FrameType.String() != frame.FrameType {
			continue
		}
		if rule.RequestID != 0 && rule.RequestID != frame.RequestID {
			continue
		}
		if rule.StreamID != 0 && rule.StreamID != frame.StreamID {
			continue
		}
		if rule.SessionID != 0 && rule.SessionID != frame.SessionID {
			continue
		}
		s.ruleHits[index]++
		if rule.Occurrence > 0 && s.ruleHits[index] != rule.Occurrence {
			continue
		}
		return rule
	}
	return nil
}

func (s *ScriptedFrameIO) deliverToPeer(peer *scriptedEndpoint, payload []byte) bool {
	if peer == nil || peer.isClosed() || peer.isReadClosed() {
		return false
	}
	select {
	case peer.inbox <- append([]byte(nil), payload...):
		return true
	default:
		peer.inbox <- append([]byte(nil), payload...)
		return true
	}
}

func (s *ScriptedFrameIO) closeRead(endpoint *scriptedEndpoint) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if endpoint == nil || endpoint.isClosed() || endpoint.isReadClosed() {
		return false
	}
	endpoint.readClosed.Store(true)
	return true
}

func (s *ScriptedFrameIO) closeWrite(endpoint *scriptedEndpoint) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if endpoint == nil || endpoint.isClosed() || endpoint.isWriteClosed() {
		return false
	}
	endpoint.writeClosed.Store(true)
	return true
}

func (pair *scriptedPair) peer(side FrameSide) *scriptedEndpoint {
	if pair == nil {
		return nil
	}
	switch side {
	case FrameSideServer:
		return pair.client
	default:
		return pair.server
	}
}

func (c *ScriptedConn) TransportConnID() string {
	if c == nil || c.endpoint == nil {
		return ""
	}
	return c.endpoint.id
}

func (c *ScriptedConn) Read(_ []byte) (int, error) {
	if c == nil || c.endpoint == nil {
		return 0, net.ErrClosed
	}
	return 0, c.endpoint.rawIOError
}

func (c *ScriptedConn) Write(_ []byte) (int, error) {
	if c == nil || c.endpoint == nil {
		return 0, net.ErrClosed
	}
	return 0, c.endpoint.rawIOError
}

func (c *ScriptedConn) Close() error {
	if c == nil || c.transport == nil || c.endpoint == nil {
		return nil
	}
	if c.pair == nil {
		return nil
	}
	for _, endpoint := range []*scriptedEndpoint{c.pair.client, c.pair.server} {
		if endpoint == nil {
			continue
		}
		endpoint.close()
	}
	return nil
}

func (c *ScriptedConn) LocalAddr() net.Addr {
	if c == nil || c.endpoint == nil {
		return scriptedAddr{}
	}
	return c.endpoint.localAddr
}

func (c *ScriptedConn) RemoteAddr() net.Addr {
	if c == nil || c.endpoint == nil {
		return scriptedAddr{}
	}
	return c.endpoint.remoteAddr
}

func (c *ScriptedConn) SetDeadline(time.Time) error {
	return nil
}

func (c *ScriptedConn) SetReadDeadline(time.Time) error {
	return nil
}

func (c *ScriptedConn) SetWriteDeadline(time.Time) error {
	return nil
}

func (a scriptedAddr) Network() string {
	return a.network
}

func (a scriptedAddr) String() string {
	return a.address
}

func endpointState(endpoint *scriptedEndpoint) testsupport.TransportConnState {
	switch {
	case endpoint == nil || endpoint.isClosed():
		return testsupport.TransportConnStateClosed
	case endpoint.isReadClosed():
		return testsupport.TransportConnStateReadClosed
	case endpoint.isWriteClosed():
		return testsupport.TransportConnStateWriteClosed
	default:
		return testsupport.TransportConnStateOpen
	}
}

func (e *scriptedEndpoint) isClosed() bool {
	return e == nil || e.closed.Load()
}

func (e *scriptedEndpoint) isReadClosed() bool {
	return e == nil || e.readClosed.Load()
}

func (e *scriptedEndpoint) isWriteClosed() bool {
	return e == nil || e.writeClosed.Load()
}

func (e *scriptedEndpoint) close() {
	if e == nil {
		return
	}
	e.closed.Store(true)
	e.closeOnce.Do(func() {
		close(e.closedCh)
	})
}

func directionFor(side FrameSide) testsupport.FrameDirection {
	if side == FrameSideServer {
		return testsupport.FrameDirectionServerToClient
	}
	return testsupport.FrameDirectionClientToServer
}

func buildObservedFrame(connID string, direction testsupport.FrameDirection, frame []byte, context FrameContext) testsupport.FrameObservedState {
	observed := testsupport.FrameObservedState{
		ConnID:    connID,
		Direction: direction,
		SessionID: context.SessionID,
	}
	parsed, err := protocol.ParseFrame(frame)
	if err != nil {
		observed.FrameType = "invalid"
		return observed
	}
	observed.FrameType = parsed.Type.String()
	observed.RequestID = parsed.RequestID
	observed.StreamID = parsed.StreamID
	switch parsed.Type {
	case protocol.TypeConfigPush:
		push, err := protocol.UnmarshalConfigPush(parsed.Body)
		if err == nil {
			observed.ConfigVersion = push.ConfigVersion
		}
	case protocol.TypeConfigAck:
		ack, err := protocol.UnmarshalConfigAck(parsed.Body)
		if err == nil {
			observed.ConfigVersion = ack.ConfigVersion
		}
	}
	return observed
}

func matchesReleaseFilter(frame testsupport.FrameObservedState, filter ReleaseFilter) bool {
	if filter.ConnID != "" && filter.ConnID != frame.ConnID {
		return false
	}
	if filter.Direction != "" && filter.Direction != frame.Direction {
		return false
	}
	if filter.RequestID != 0 && filter.RequestID != frame.RequestID {
		return false
	}
	if filter.FrameType != "" && filter.FrameType != frame.FrameType {
		return false
	}
	return true
}

func cloneFrames(src []testsupport.FrameObservedState) []testsupport.FrameObservedState {
	if len(src) == 0 {
		return nil
	}
	cloned := make([]testsupport.FrameObservedState, len(src))
	copy(cloned, src)
	return cloned
}

type timeoutError struct{}

func (timeoutError) Error() string   { return "i/o timeout" }
func (timeoutError) Timeout() bool   { return true }
func (timeoutError) Temporary() bool { return true }
