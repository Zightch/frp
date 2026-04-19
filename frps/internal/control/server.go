package control

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"errors"
	"io"
	"log/slog"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/zightch/frp/frps/internal/storage"
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
)

type Options struct {
	Addr              string
	Store             *storage.SQL
	Repository        Repository
	ReadTimeout       time.Duration
	WriteTimeout      time.Duration
	ChallengeTTL      time.Duration
	HeartbeatInterval time.Duration
}

type Server struct {
	options Options
	logger  *slog.Logger
	version string
	repo    Repository

	mu         sync.Mutex
	listener   net.Listener
	activeConn map[net.Conn]struct{}
	// groupSlots tracks the occupied single client slot for each proxy group.
	groupSlots map[int64]uint64
	closeOnce  sync.Once
	connWG     sync.WaitGroup

	challengeMu sync.Mutex
	challenges  map[uint32]*authChallenge

	nextChallengeID atomic.Uint32
	nextSessionID   atomic.Uint64
}

type authChallenge struct {
	TokenHash [32]byte
	Nonce     [16]byte
	ExpiresAt time.Time
	Used      bool
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
	if options.Repository == nil && options.Store != nil {
		options.Repository = NewRepository(options.Store)
	}

	return &Server{
		options:    options,
		logger:     logger,
		version:    version,
		repo:       options.Repository,
		activeConn: make(map[net.Conn]struct{}),
		groupSlots: make(map[int64]uint64),
		challenges: make(map[uint32]*authChallenge),
	}
}

func (s *Server) ListenAndServe(ctx context.Context) error {
	listenConfig := net.ListenConfig{}
	listener, err := listenConfig.Listen(ctx, "tcp", s.options.Addr)
	if err != nil {
		return err
	}

	s.mu.Lock()
	s.listener = listener
	s.mu.Unlock()

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
		s.mu.Unlock()

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
	}()

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-done:
		return nil
	}
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

func (s *Server) authenticate(conn net.Conn) (*sessionState, error) {
	frame, err := s.readFrame(conn)
	if err != nil {
		return nil, s.replyProtocolError(conn, frame, err)
	}
	if frame.Type != protocol.TypeAuthBegin {
		return nil, s.replyError(
			conn,
			frame.RequestID,
			frame.StreamID,
			protocol.ErrorCodeProtocolBadBody,
			"expected auth.begin, got %s",
			frame.Type.String(),
		)
	}
	if frame.RequestID == 0 {
		return nil, s.replyError(conn, 0, frame.StreamID, protocol.ErrorCodeProtocolBadBody, "auth.begin requestId must be non-zero")
	}
	if frame.StreamID != 0 {
		return nil, s.replyError(conn, frame.RequestID, frame.StreamID, protocol.ErrorCodeProtocolBadBody, "auth.begin streamId must be zero")
	}

	begin, err := protocol.UnmarshalAuthBegin(frame.Body)
	if err != nil {
		return nil, s.replyProtocolError(conn, frame, err)
	}

	group, err := s.loadGroupRuntime(begin.TokenID)
	if err != nil {
		switch {
		case errors.Is(err, ErrGroupNotFound):
			return nil, s.replyError(conn, frame.RequestID, 0, protocol.ErrorCodeAuthInvalidToken, "token id not found")
		default:
			return nil, err
		}
	}
	if !group.Enabled {
		return nil, s.replyError(conn, frame.RequestID, 0, protocol.ErrorCodeAuthGroupDisabled, "proxy group is disabled")
	}

	if !clientIPAllowed(group.ClientAccessMode, group.ClientRules, remoteIP(conn.RemoteAddr())) {
		return nil, s.replyError(conn, frame.RequestID, 0, protocol.ErrorCodeAuthDeniedByIP, "client IP is not allowed")
	}

	challenge, err := s.issueChallenge(group.TokenHash)
	if err != nil {
		return nil, err
	}
	challengeBody, err := protocol.MarshalAuthChallenge(challenge)
	if err != nil {
		return nil, err
	}
	if err := s.writeFrame(conn, protocol.Frame{
		Type:      protocol.TypeAuthChallenge,
		RequestID: frame.RequestID,
		Body:      challengeBody,
	}); err != nil {
		return nil, err
	}

	frame, err = s.readFrame(conn)
	if err != nil {
		return nil, s.replyProtocolError(conn, frame, err)
	}
	if frame.Type != protocol.TypeAuthFinish {
		return nil, s.replyError(
			conn,
			frame.RequestID,
			frame.StreamID,
			protocol.ErrorCodeProtocolBadBody,
			"expected auth.finish, got %s",
			frame.Type.String(),
		)
	}
	if frame.RequestID == 0 {
		return nil, s.replyError(conn, 0, frame.StreamID, protocol.ErrorCodeProtocolBadBody, "auth.finish requestId must be non-zero")
	}
	if frame.StreamID != 0 {
		return nil, s.replyError(conn, frame.RequestID, frame.StreamID, protocol.ErrorCodeProtocolBadBody, "auth.finish streamId must be zero")
	}

	finish, err := protocol.UnmarshalAuthFinish(frame.Body)
	if err != nil {
		return nil, s.replyProtocolError(conn, frame, err)
	}
	if err := s.consumeChallenge(finish.ChallengeID, finish.Response); err != nil {
		return nil, s.replyProtocolError(conn, frame, err)
	}

	session := &sessionState{
		ID:             s.nextSessionID.Add(1),
		Group:          group,
		Snapshot:       group.Snapshot,
		readTimeout:    sessionReadTimeout(s.options.HeartbeatInterval, s.options.ReadTimeout),
		streams:        make(map[uint32]*publicStream),
		udpSessions:    make(map[uint32]*publicUDPSession),
		udpSessionKeys: make(map[string]uint32),
		listeners:      make(map[uint32][]net.Listener),
		udpListeners:   make(map[uint32][]*net.UDPConn),
		done:           make(chan struct{}),
	}
	session.nextServerRequestID.Store(initialServerRequestID - 1)
	if !s.reserveGroupSlot(session.Group.ID, session.ID) {
		return nil, s.replyError(conn, frame.RequestID, 0, protocol.ErrorCodeAuthClientLimitReached, "proxy group already has an active client")
	}

	helloBody, err := protocol.MarshalServerHello(protocol.ServerHello{
		HeartbeatIntervalMs: uint32(s.options.HeartbeatInterval / time.Millisecond),
		SessionID:           session.ID,
		CapabilityBits:      0,
		ServerVersion:       s.version,
	})
	if err != nil {
		s.releaseGroupSlot(session.Group.ID, session.ID)
		return nil, err
	}
	if err := s.writeFrame(conn, protocol.Frame{
		Type:      protocol.TypeServerHello,
		RequestID: frame.RequestID,
		Body:      helloBody,
	}); err != nil {
		s.releaseGroupSlot(session.Group.ID, session.ID)
		return nil, err
	}

	if err := s.pushConfig(conn, session); err != nil {
		s.releaseGroupSlot(session.Group.ID, session.ID)
		return nil, err
	}

	return session, nil
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

func (s *Server) issueChallenge(tokenHash [32]byte) (protocol.AuthChallenge, error) {
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return protocol.AuthChallenge{}, err
	}

	challengeID := s.nextChallengeID.Add(1)
	if challengeID == 0 {
		challengeID = s.nextChallengeID.Add(1)
	}

	now := time.Now().UTC()
	s.challengeMu.Lock()
	defer s.challengeMu.Unlock()

	s.purgeExpiredChallengesLocked(now)
	s.challenges[challengeID] = &authChallenge{
		TokenHash: tokenHash,
		Nonce:     nonce,
		ExpiresAt: now.Add(s.options.ChallengeTTL),
	}

	return protocol.AuthChallenge{
		ChallengeID: challengeID,
		Nonce:       nonce,
		ExpiresInMs: uint32(s.options.ChallengeTTL / time.Millisecond),
	}, nil
}

func (s *Server) consumeChallenge(challengeID uint32, response [32]byte) error {
	now := time.Now().UTC()

	s.challengeMu.Lock()
	defer s.challengeMu.Unlock()

	s.purgeExpiredChallengesLocked(now)

	challenge, ok := s.challenges[challengeID]
	if !ok {
		return protocol.NewError(protocol.ErrorCodeAuthChallengeExpired, "challenge %d is missing or expired", challengeID)
	}
	if challenge.Used {
		return protocol.NewError(protocol.ErrorCodeAuthChallengeReplayed, "challenge %d has already been used", challengeID)
	}
	if now.After(challenge.ExpiresAt) {
		challenge.Used = true
		return protocol.NewError(protocol.ErrorCodeAuthChallengeExpired, "challenge %d expired", challengeID)
	}

	challenge.Used = true
	expected := challengeResponse(challenge.TokenHash, challenge.Nonce)
	if subtle.ConstantTimeCompare(expected[:], response[:]) != 1 {
		return protocol.NewError(protocol.ErrorCodeAuthInvalidToken, "challenge response mismatch")
	}

	return nil
}

func (s *Server) purgeExpiredChallengesLocked(now time.Time) {
	for challengeID, challenge := range s.challenges {
		if now.After(challenge.ExpiresAt) {
			delete(s.challenges, challengeID)
		}
	}
}

func challengeResponse(tokenHash [32]byte, nonce [16]byte) [32]byte {
	var payload [48]byte
	copy(payload[:32], tokenHash[:])
	copy(payload[32:], nonce[:])
	return sha256.Sum256(payload[:])
}

func clientIPAllowed(mode string, rules []IPRule, ip net.IP) bool {
	allowMatched := false
	for _, rule := range rules {
		if !rule.Matches(ip) {
			continue
		}
		if rule.Action == "deny" {
			return false
		}
		if rule.Action == "allow" {
			allowMatched = true
		}
	}

	switch mode {
	case "allowlist", "allowlist_and_denylist":
		return allowMatched
	default:
		return true
	}
}

func remoteIP(addr net.Addr) net.IP {
	switch typed := addr.(type) {
	case *net.TCPAddr:
		return typed.IP
	case *net.UDPAddr:
		return typed.IP
	default:
		host, _, err := net.SplitHostPort(addr.String())
		if err != nil {
			return nil
		}
		return net.ParseIP(host)
	}
}

func (s *Server) registerConn(conn net.Conn) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.activeConn[conn] = struct{}{}
}

func (s *Server) unregisterConn(conn net.Conn) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.activeConn, conn)
}

func (s *Server) reserveGroupSlot(groupID int64, sessionID uint64) bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, ok := s.groupSlots[groupID]; ok {
		return false
	}
	s.groupSlots[groupID] = sessionID
	return true
}

func (s *Server) releaseGroupSlot(groupID int64, sessionID uint64) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.groupSlots[groupID] == sessionID {
		delete(s.groupSlots, groupID)
	}
}

func sessionReadTimeout(heartbeatInterval, minimum time.Duration) time.Duration {
	timeout := heartbeatInterval * 3
	if timeout < minimum {
		return minimum
	}
	return timeout
}

func logConnection(logger *slog.Logger, level slog.Level, message string, err error) {
	if isExpectedConnectionClose(err) {
		logger.Log(context.Background(), slog.LevelInfo, message, "error", err)
		return
	}
	logger.Log(context.Background(), level, message, "error", err)
}

func connectionErrorDetails(err error) (slog.Level, string) {
	if err == nil {
		return slog.LevelInfo, "completed"
	}
	if isExpectedConnectionClose(err) {
		return slog.LevelInfo, connectionReason(err)
	}
	return slog.LevelWarn, connectionReason(err)
}

func isExpectedConnectionClose(err error) bool {
	if err == nil {
		return true
	}
	if errors.Is(err, io.EOF) || errors.Is(err, net.ErrClosed) {
		return true
	}

	var netErr net.Error
	return errors.As(err, &netErr) && netErr.Timeout()
}

func connectionReason(err error) string {
	switch {
	case err == nil:
		return "completed"
	case errors.Is(err, io.EOF):
		return "eof"
	case errors.Is(err, net.ErrClosed):
		return "closed"
	case errors.Is(err, transport.ErrFrameTooSmall):
		return "invalid frame length below minimum"
	case errors.Is(err, transport.ErrFrameTooLarge):
		return "invalid frame length above maximum"
	}

	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return "timeout"
	}

	var protocolErr *protocol.ProtocolError
	if errors.As(err, &protocolErr) {
		return protocolErr.Message
	}

	return err.Error()
}
