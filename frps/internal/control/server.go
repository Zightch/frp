package control

import (
	"context"
	"log/slog"
	"net"
	"sync"
	"sync/atomic"

	"github.com/zightch/frp/frps/internal/clock"
	controlauth "github.com/zightch/frp/frps/internal/control/protocol/auth"
	controlhandshake "github.com/zightch/frp/frps/internal/control/protocol/handshake"
	controlruntime "github.com/zightch/frp/frps/internal/control/runtime"
	"github.com/zightch/frp/frps/internal/system"
	"github.com/zightch/frp/frps/pkg/transport"
)

type Server struct {
	options   Options
	logger    *slog.Logger
	version   string
	repo      Repository
	network   system.SnapshotReader
	clock     clock.Clock
	scheduler clock.Scheduler
	listeners ListenerFactory
	frames    transport.FrameIO

	mu                  sync.Mutex
	listener            net.Listener
	controlListenerOpen bool
	activeConn          map[net.Conn]struct{}
	runtimeIssues       *controlruntime.IssueStore
	closeOnce           sync.Once
	connWG              sync.WaitGroup
	scanWG              sync.WaitGroup
	shutdownCh          chan struct{}

	authChallenges *controlauth.ChallengeService

	initialRuntimeScanMu   sync.Mutex
	initialRuntimeScanDone bool
	runtimeScanCancel      context.CancelFunc
	runtimeScanStateMu     sync.Mutex
	runtimeScanInFlight    bool

	nextSessionID atomic.Uint64

	controlTLS *controlhandshake.ControlTLSStore

	supervisor *Supervisor
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
	if options.Clock == nil {
		realClock := clock.NewRealClock()
		options.Clock = realClock
	}
	if options.Scheduler == nil {
		options.Scheduler = clock.NewRealScheduler()
	}
	if options.ListenerFactory == nil {
		options.ListenerFactory = NewNetListenerFactory()
	}
	if options.FrameIO == nil {
		options.FrameIO = transport.RealFrameIO{}
	}

	server := &Server{
		options:       options,
		logger:        logger,
		version:       version,
		repo:          options.Repository,
		network:       options.Network,
		clock:         options.Clock,
		scheduler:     options.Scheduler,
		listeners:     options.ListenerFactory,
		frames:        options.FrameIO,
		activeConn:    make(map[net.Conn]struct{}),
		runtimeIssues: controlruntime.NewIssueStore(),
		shutdownCh:    make(chan struct{}),
		authChallenges: controlauth.NewChallengeService(controlauth.ChallengeServiceOptions{
			Clock: options.Clock,
			TTL:   options.ChallengeTTL,
		}),
		controlTLS: controlhandshake.NewControlTLSStore(),
	}
	server.supervisor = NewSupervisor(newServerActionExecutor(server))
	return server
}
