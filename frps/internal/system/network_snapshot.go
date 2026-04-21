package system

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"
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
}

type NetworkSnapshotService struct {
	logger       *slog.Logger
	collector    collector
	pollInterval time.Duration

	mu       sync.RWMutex
	snapshot Snapshot
	started  bool
	cancel   context.CancelFunc

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

	return &NetworkSnapshotService{
		logger:       logger,
		collector:    options.Collector,
		pollInterval: options.PollInterval,
	}
}

func (s *NetworkSnapshotService) Start(parent context.Context) error {
	if parent == nil {
		parent = context.Background()
	}

	initialSnapshot, err := s.collector.Collect()
	if err != nil {
		return fmt.Errorf("collect initial local network snapshot: %w", err)
	}

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
	s.wg.Add(1)
	s.mu.Unlock()

	s.logger.Info(
		"local network snapshot ready",
		"platform", initialSnapshot.Platform,
		"interface_count", len(initialSnapshot.Interfaces),
		"address_count", len(initialSnapshot.AvailableIPs),
	)

	go s.poll(ctx)
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

func (s *NetworkSnapshotService) poll(ctx context.Context) {
	defer s.wg.Done()

	ticker := time.NewTicker(s.pollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			snapshot, err := s.collector.Collect()
			if err != nil {
				s.logger.Warn(
					"refresh local network snapshot failed",
					"platform", s.collector.Platform(),
					"error", err,
				)
				continue
			}

			if s.storeSnapshot(snapshot) {
				s.logger.Info(
					"local network snapshot updated",
					"platform", snapshot.Platform,
					"interface_count", len(snapshot.Interfaces),
					"address_count", len(snapshot.AvailableIPs),
				)
			}
		}
	}
}

func (s *NetworkSnapshotService) storeSnapshot(next Snapshot) bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	changed := !sameSnapshotContent(s.snapshot, next)
	s.snapshot = cloneSnapshot(next)
	return changed
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
