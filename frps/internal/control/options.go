package control

import (
	"time"

	"github.com/zightch/frp/frps/internal/clock"
	"github.com/zightch/frp/frps/internal/storage"
	"github.com/zightch/frp/frps/internal/system"
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
	Clock             clock.Clock
	Scheduler         clock.Scheduler
	ListenerFactory   ListenerFactory
	FrameIO           transport.FrameIO
}
