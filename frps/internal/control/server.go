package control

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/zightch/frp/frps/internal/storage"
	"github.com/zightch/frp/frps/internal/system"
	"github.com/zightch/frp/frps/pkg/protocol"
	"github.com/zightch/frp/frps/pkg/transport"
)

const (
	defaultReadTimeout       = 5 * time.Second
	defaultWriteTimeout      = 5 * time.Second
	defaultChallengeTTL      = 30 * time.Second
	defaultHeartbeatInterval = 15 * time.Second
	defaultUDPIdleTimeout    = 30 * time.Second
	defaultUDPIdleSweep      = time.Second
	defaultRuntimeScanPoll   = 5 * time.Second
)

type Options struct {
	Addr              string
	Store             *storage.SQL
	Repository        Repository
	Network           system.SnapshotReader
	ReadTimeout       time.Duration
	WriteTimeout      time.Duration
	ChallengeTTL      time.Duration
	HeartbeatInterval time.Duration
	RuntimeScanPoll   time.Duration
}

type Server struct {
	options Options
	logger  *slog.Logger
	version string
	repo    Repository
	network system.SnapshotReader

	mu         sync.Mutex
	listener   net.Listener
	activeConn map[net.Conn]struct{}
	sessions   map[int64]*activeSession
	// tunnelRuntimeIssues stores the latest listener-start failure observed for a tunnel.
	tunnelRuntimeIssues map[int64]string
	// groupSlots tracks the occupied single client slot for each proxy group.
	groupSlots map[int64]uint64
	closeOnce  sync.Once
	connWG     sync.WaitGroup
	scanWG     sync.WaitGroup

	challengeMu sync.Mutex
	challenges  map[uint32]*authChallenge

	initialRuntimeScanMu   sync.Mutex
	initialRuntimeScanDone bool
	runtimeScanCancel      context.CancelFunc

	nextChallengeID atomic.Uint32
	nextSessionID   atomic.Uint64
}

type activeSession struct {
	mu      sync.Mutex
	conn    net.Conn
	session *sessionState
}

func NewServer(options Options, logger *slog.Logger, version string) *Server {
	if options.ReadTimeout <= 0 {
		options.ReadTimeout = defaultReadTimeout
	}
	if options.WriteTimeout <= 0 {
		options.WriteTimeout = defaultWriteTimeout
	}
	if options.ChallengeTTL <= 0 {
		options.ChallengeTTL = defaultChallengeTTL
	}
	if options.HeartbeatInterval <= 0 {
		options.HeartbeatInterval = defaultHeartbeatInterval
	}
	if options.RuntimeScanPoll <= 0 {
		options.RuntimeScanPoll = defaultRuntimeScanPoll
	}
	if options.Repository == nil && options.Store != nil {
		options.Repository = NewRepository(options.Store)
	}

	return &Server{
		options:             options,
		logger:              logger,
		version:             version,
		repo:                options.Repository,
		network:             options.Network,
		activeConn:          make(map[net.Conn]struct{}),
		sessions:            make(map[int64]*activeSession),
		tunnelRuntimeIssues: make(map[int64]string),
		groupSlots:          make(map[int64]uint64),
		challenges:          make(map[uint32]*authChallenge),
	}
}

func (s *Server) TunnelRuntimeIssues() map[int64]string {
	if s == nil {
		return nil
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if len(s.tunnelRuntimeIssues) == 0 {
		return nil
	}

	issues := make(map[int64]string, len(s.tunnelRuntimeIssues))
	for tunnelID, reason := range s.tunnelRuntimeIssues {
		issues[tunnelID] = reason
	}
	return issues
}

func (s *Server) clearTunnelRuntimeIssues(tunnels []protocol.TunnelEntry) {
	if s == nil || len(tunnels) == 0 {
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	for _, tunnel := range tunnels {
		delete(s.tunnelRuntimeIssues, int64(tunnel.TunnelID))
	}
}

func (s *Server) recordTunnelRuntimeIssue(tunnelID uint32, reason string) {
	if s == nil || tunnelID == 0 {
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if strings.TrimSpace(reason) == "" {
		delete(s.tunnelRuntimeIssues, int64(tunnelID))
		return
	}
	s.tunnelRuntimeIssues[int64(tunnelID)] = strings.TrimSpace(reason)
}

func (s *Server) ListenAndServe(ctx context.Context) error {
	if err := s.EnsureInitialRuntimeScan(ctx); err != nil {
		return err
	}

	listenConfig := net.ListenConfig{}
	listener, err := listenConfig.Listen(ctx, "tcp", s.options.Addr)
	if err != nil {
		return err
	}

	s.mu.Lock()
	s.listener = listener
	s.mu.Unlock()

	s.startRuntimeIssuePolling(ctx)
	s.logger.Info("frpc control listener ready", "addr", s.options.Addr)

	for {
		conn, err := listener.Accept()
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return nil
			}
			return err
		}

		s.registerConn(conn)
		s.connWG.Add(1)
		go s.handleConnection(conn)
	}
}

func (s *Server) Shutdown(ctx context.Context) error {
	s.closeOnce.Do(func() {
		s.mu.Lock()
		listener := s.listener
		activeConn := make([]net.Conn, 0, len(s.activeConn))
		for conn := range s.activeConn {
			activeConn = append(activeConn, conn)
		}
		runtimeScanCancel := s.runtimeScanCancel
		s.mu.Unlock()

		if runtimeScanCancel != nil {
			runtimeScanCancel()
		}
		if listener != nil {
			_ = listener.Close()
		}
		for _, conn := range activeConn {
			_ = conn.Close()
		}
	})

	done := make(chan struct{})
	go func() {
		defer close(done)
		s.connWG.Wait()
		s.scanWG.Wait()
	}()

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-done:
		return nil
	}
}

func (s *Server) EnsureInitialRuntimeScan(ctx context.Context) error {
	if s == nil {
		return nil
	}

	s.initialRuntimeScanMu.Lock()
	defer s.initialRuntimeScanMu.Unlock()

	if s.initialRuntimeScanDone {
		return nil
	}
	if err := s.scanNonListeningTunnelRuntimeIssues(ctx); err != nil {
		return err
	}
	s.initialRuntimeScanDone = true
	return nil
}

func (s *Server) handleConnection(conn net.Conn) {
	defer s.connWG.Done()
	defer s.unregisterConn(conn)
	defer conn.Close()

	logger := s.logger.With("remote_addr", conn.RemoteAddr().String())
	logger.Info("frpc control connection accepted")

	if s.repo == nil {
		logger.Error("frpc control connection rejected", "reason", "repository not configured")
		logger.Info("frpc control connection closed", "reason", "repository not configured")
		return
	}

	session, err := s.authenticate(conn)
	if err != nil {
		level, reason := connectionErrorDetails(err)
		logConnection(logger, level, "frpc control login failed", err)
		logger.Info("frpc control connection closed", "reason", reason)
		return
	}

	logger = logger.With(
		"session_id", session.ID,
		"group_id", session.Group.ID,
		"group_name", session.Group.Name,
	)
	s.registerActiveSession(conn, session)
	defer s.unregisterActiveSession(session)
	defer s.shutdownSession(session)
	logger.Info(
		"frpc control login succeeded",
		"config_version", session.Snapshot.Version,
		"tunnel_count", len(session.Snapshot.Tunnels),
	)

	err = s.runSession(conn, logger, session)
	if err != nil {
		level, reason := connectionErrorDetails(err)
		logConnection(logger, level, "frpc control session ended", err)
		logger.Info("frpc control connection closed", "reason", reason)
		return
	}

	logger.Info("frpc control connection closed", "reason", "completed")
}

func (s *Server) runSession(conn net.Conn, logger *slog.Logger, session *sessionState) error {
	for {
		frame, err := s.readFrameWithTimeout(conn, session.readTimeout)
		if err != nil {
			return s.replyProtocolErrorWithSession(conn, session, frame, err)
		}

		switch frame.Type {
		case protocol.TypeConfigAck:
			if err := s.handleConfigAck(conn, logger, session, frame); err != nil {
				return err
			}
		case protocol.TypeHeartbeatPing:
			if err := s.handleHeartbeatPing(conn, session, frame); err != nil {
				return err
			}
		case protocol.TypeStreamOpened:
			if err := s.handleStreamOpened(conn, session, frame); err != nil {
				return err
			}
		case protocol.TypeStreamData:
			if err := s.handleStreamData(conn, session, frame); err != nil {
				return err
			}
		case protocol.TypeStreamClose:
			if err := s.handleStreamClose(session, frame); err != nil {
				return err
			}
		case protocol.TypeUDPData:
			if err := s.handleUDPData(conn, session, frame); err != nil {
				return err
			}
		case protocol.TypeUDPClose:
			if err := s.handleUDPClose(session, frame); err != nil {
				return err
			}
		default:
			return s.replyErrorWithSession(
				conn,
				session,
				frame.RequestID,
				frame.StreamID,
				protocol.ErrorCodeProtocolBadBody,
				"unexpected message type %s",
				frame.Type.String(),
			)
		}
	}
}

func (s *Server) handleHeartbeatPing(conn net.Conn, session *sessionState, frame protocol.Frame) error {
	if frame.RequestID == 0 {
		return s.replyErrorWithSession(conn, session, 0, frame.StreamID, protocol.ErrorCodeProtocolBadBody, "heartbeat.ping requestId must be non-zero")
	}
	if frame.StreamID != 0 {
		return s.replyErrorWithSession(conn, session, frame.RequestID, frame.StreamID, protocol.ErrorCodeProtocolBadBody, "heartbeat.ping streamId must be zero")
	}

	ping, err := protocol.UnmarshalHeartbeatPing(frame.Body)
	if err != nil {
		return s.replyProtocolErrorWithSession(conn, session, frame, err)
	}

	body, err := protocol.MarshalHeartbeatPong(protocol.HeartbeatPong{
		ClientUnixMs: ping.ClientUnixMs,
		ServerUnixMs: uint64(time.Now().UTC().UnixMilli()),
	})
	if err != nil {
		return err
	}
	return s.writeFrameWithSession(conn, session, protocol.Frame{
		Type:      protocol.TypeHeartbeatPong,
		RequestID: frame.RequestID,
		Body:      body,
	})
}

func (s *Server) readFrame(conn net.Conn) (protocol.Frame, error) {
	return s.readFrameWithTimeout(conn, s.options.ReadTimeout)
}

func (s *Server) readFrameWithTimeout(conn net.Conn, timeout time.Duration) (protocol.Frame, error) {
	frameBytes, err := transport.ReadFrame(conn, timeout)
	if err != nil {
		return protocol.Frame{}, err
	}
	return protocol.ParseFrame(frameBytes)
}

func (s *Server) writeFrame(conn net.Conn, frame protocol.Frame) error {
	frameBytes, err := frame.MarshalBinary()
	if err != nil {
		return err
	}
	return transport.WriteFrame(conn, frameBytes, s.options.WriteTimeout)
}

func (s *Server) replyProtocolError(conn net.Conn, frame protocol.Frame, err error) error {
	protocolErr := protocol.AsProtocolError(err)
	if protocolErr == nil {
		return err
	}
	if writeErr := s.writeError(conn, frame.RequestID, frame.StreamID, protocolErr.Code, false, protocolErr.Message); writeErr != nil {
		return errors.Join(err, writeErr)
	}
	return err
}

func (s *Server) replyError(conn net.Conn, requestID, streamID uint32, code uint16, format string, args ...any) error {
	err := protocol.NewError(code, format, args...)
	if writeErr := s.writeError(conn, requestID, streamID, code, false, err.Message); writeErr != nil {
		return errors.Join(err, writeErr)
	}
	return err
}

func (s *Server) writeError(conn net.Conn, requestID, streamID uint32, code uint16, retryable bool, message string) error {
	body, err := protocol.MarshalErrorBody(protocol.ErrorBody{
		ErrorCode: code,
		Retryable: retryable,
		Message:   message,
	})
	if err != nil {
		return err
	}
	return s.writeFrame(conn, protocol.Frame{
		Type:      protocol.TypeError,
		RequestID: requestID,
		StreamID:  streamID,
		Body:      body,
	})
}
