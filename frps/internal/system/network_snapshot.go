package system

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/zightch/frp/frps/internal/clock"
	"github.com/zightch/frp/frps/internal/testhooks"
	"github.com/zightch/frp/frps/pkg/testsupport"
)

const defaultPollInterval = 5 * time.Second

type AddressFamily string

const (
	FamilyIPv4 AddressFamily = "ipv4"
	FamilyIPv6 AddressFamily = "ipv6"
)

type IPAddress struct {
	Addr   string        `json:"addr"`
	Family AddressFamily `json:"family"`
}

type NetworkInterface struct {
	Name  string      `json:"name"`
	Index int         `json:"index"`
	IPs   []IPAddress `json:"ips"`
}

type Snapshot struct {
	Platform     string             `json:"platform"`
	CapturedAt   time.Time          `json:"captured_at"`
	Interfaces   []NetworkInterface `json:"interfaces"`
	AvailableIPs []IPAddress        `json:"available_ips"`
}

func (s Snapshot) HasIP(addr string) bool {
	for _, ip := range s.AvailableIPs {
		if ip.Addr == addr {
			return true
		}
	}
	return false
}

func (s Snapshot) AvailableIPStrings() []string {
	values := make([]string, 0, len(s.AvailableIPs))
	for _, ip := range s.AvailableIPs {
		values = append(values, ip.Addr)
	}
	return values
}

type SnapshotReader interface {
	Current() Snapshot
}

type Options struct {
	PollInterval time.Duration
	Collector    collector
	Clock        clock.Clock
	Scheduler    clock.Scheduler
}

type NetworkSnapshotService struct {
	logger       *slog.Logger
	collector    collector
	pollInterval time.Duration
	clock        clock.Clock
	scheduler    clock.Scheduler

	mu       sync.RWMutex
	snapshot Snapshot
	started  bool
	cancel   context.CancelFunc
	version  uint64

	lastCollectSucceeded bool
	lastCollectError     string

	wg sync.WaitGroup
}

type collector interface {
	Collect() (Snapshot, error)
	Platform() string
}

func NewNetworkSnapshotService(options Options, logger *slog.Logger) *NetworkSnapshotService {
	if logger == nil {
		logger = slog.Default()
	}
	if options.PollInterval <= 0 {
		options.PollInterval = defaultPollInterval
	}
	if options.Collector == nil {
		options.Collector = newPlatformCollector()
	}
	if options.Clock == nil {
		realClock := clock.NewRealClock()
		options.Clock = realClock
	}
	if options.Scheduler == nil {
		options.Scheduler = clock.NewRealScheduler()
	}

	return &NetworkSnapshotService{
		logger:       logger,
		collector:    options.Collector,
		pollInterval: options.PollInterval,
		clock:        options.Clock,
		scheduler:    options.Scheduler,
	}
}

func (s *NetworkSnapshotService) Start(parent context.Context) error {
	if parent == nil {
		parent = context.Background()
	}

	testhooks.Point("network.snapshot.start.before_collect")
	initialSnapshot, err := s.collector.Collect()
	if err != nil {
		return fmt.Errorf("collect initial local network snapshot: %w", err)
	}
	testhooks.Point("network.snapshot.start.after_collect", testhooks.F("address_count", len(initialSnapshot.AvailableIPs)))

	ctx, cancel := context.WithCancel(parent)

	s.mu.Lock()
	if s.started {
		s.mu.Unlock()
		cancel()
		return nil
	}
	s.snapshot = cloneSnapshot(initialSnapshot)
	s.started = true
	s.cancel = cancel
	s.version++
	s.lastCollectSucceeded = true
	s.lastCollectError = ""
	s.mu.Unlock()

	s.logger.Info(
		"local network snapshot ready",
		"platform", initialSnapshot.Platform,
		"interface_count", len(initialSnapshot.Interfaces),
		"address_count", len(initialSnapshot.AvailableIPs),
	)

	task := s.scheduler.Every(ctx, "system.network_snapshot_poll", s.pollInterval, func(ctx context.Context, _ time.Time) {
		s.pollOnce(ctx)
	})
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		<-task.Done()
	}()
	return nil
}

func (s *NetworkSnapshotService) Current() Snapshot {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return cloneSnapshot(s.snapshot)
}

func (s *NetworkSnapshotService) Shutdown(ctx context.Context) error {
	s.mu.Lock()
	cancel := s.cancel
	started := s.started
	s.cancel = nil
	s.started = false
	s.mu.Unlock()

	if !started || cancel == nil {
		return nil
	}

	cancel()

	done := make(chan struct{})
	go func() {
		defer close(done)
		s.wg.Wait()
	}()

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-done:
		return nil
	}
}

func (s *NetworkSnapshotService) pollOnce(ctx context.Context) {
	select {
	case <-ctx.Done():
		return
	default:
	}

	testhooks.Point("network.snapshot.poll.before_collect")
	snapshot, err := s.collector.Collect()
	if err != nil {
		s.mu.Lock()
		s.lastCollectSucceeded = false
		s.lastCollectError = err.Error()
		s.mu.Unlock()
		s.logger.Warn(
			"refresh local network snapshot failed",
			"platform", s.collector.Platform(),
			"error", err,
		)
		return
	}
	testhooks.Point("network.snapshot.poll.after_collect", testhooks.F("address_count", len(snapshot.AvailableIPs)))

	if s.storeSnapshot(snapshot) {
		s.logger.Info(
			"local network snapshot updated",
			"platform", snapshot.Platform,
			"interface_count", len(snapshot.Interfaces),
			"address_count", len(snapshot.AvailableIPs),
		)
	}
}

func (s *NetworkSnapshotService) storeSnapshot(next Snapshot) bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	changed := !sameSnapshotContent(s.snapshot, next)
	s.snapshot = cloneSnapshot(next)
	s.version++
	s.lastCollectSucceeded = true
	s.lastCollectError = ""
	testhooks.Point(
		"network.snapshot.poll.after_store",
		testhooks.F("changed", changed),
		testhooks.F("address_count", len(next.AvailableIPs)),
	)
	return changed
}

func (s *NetworkSnapshotService) ObserveState() testsupport.SnapshotObservedState {
	if s == nil {
		return testsupport.SnapshotObservedState{}
	}

	s.mu.RLock()
	defer s.mu.RUnlock()
	state := testsupport.SnapshotObservedState{
		Started:              s.started,
		Version:              s.version,
		LastCollectSucceeded: s.lastCollectSucceeded,
		LastCollectError:     s.lastCollectError,
		CapturedAt:           s.snapshot.CapturedAt,
	}
	if len(s.snapshot.AvailableIPs) > 0 {
		state.AvailableIPs = make([]string, len(s.snapshot.AvailableIPs))
		for index, ip := range s.snapshot.AvailableIPs {
			state.AvailableIPs[index] = ip.Addr
		}
	}
	return state
}

func cloneSnapshot(src Snapshot) Snapshot {
	dst := Snapshot{
		Platform:   src.Platform,
		CapturedAt: src.CapturedAt,
	}
	if len(src.Interfaces) > 0 {
		dst.Interfaces = make([]NetworkInterface, len(src.Interfaces))
		for index, item := range src.Interfaces {
			dst.Interfaces[index] = NetworkInterface{
				Name:  item.Name,
				Index: item.Index,
			}
			if len(item.IPs) > 0 {
				dst.Interfaces[index].IPs = append([]IPAddress(nil), item.IPs...)
			}
		}
	}
	if len(src.AvailableIPs) > 0 {
		dst.AvailableIPs = append([]IPAddress(nil), src.AvailableIPs...)
	}
	return dst
}

func sameSnapshotContent(left, right Snapshot) bool {
	if left.Platform != right.Platform {
		return false
	}
	if len(left.Interfaces) != len(right.Interfaces) {
		return false
	}
	for index := range left.Interfaces {
		if !sameInterface(left.Interfaces[index], right.Interfaces[index]) {
			return false
		}
	}
	if len(left.AvailableIPs) != len(right.AvailableIPs) {
		return false
	}
	for index := range left.AvailableIPs {
		if left.AvailableIPs[index] != right.AvailableIPs[index] {
			return false
		}
	}
	return true
}

func sameInterface(left, right NetworkInterface) bool {
	if left.Name != right.Name || left.Index != right.Index || len(left.IPs) != len(right.IPs) {
		return false
	}
	for index := range left.IPs {
		if left.IPs[index] != right.IPs[index] {
			return false
		}
	}
	return true
}
