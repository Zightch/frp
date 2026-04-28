//go:build testhooks

package wiring

import (
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"log/slog"
	"os"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/zightch/frp/frps/internal/clock"
	"github.com/zightch/frp/frps/pkg/protocol"
	sharedtestsupport "github.com/zightch/frp/frps/pkg/testsupport"
)

func TestServerStabilityScenarioConfigChurnConverges(t *testing.T) {
	versions := stabilityEnvInt("FRPS_STABILITY_CHURN_VERSIONS", 12)
	if versions < 2 {
		versions = 2
	}

	fixture := newStabilityFixture(t, stabilityFixtureOptions{
		TunnelCount:     1,
		InitialBasePort: 23000,
		RuntimeScanPoll: time.Second,
	})
	defer fixture.Close(t)

	previousPorts := fixture.currentPorts()
	previousObserved := fixture.waitForRunning(t, previousPorts)

	for version := 2; version <= versions+1; version++ {
		nextPorts := fixture.sequentialPorts(23000 + version)
		refreshDone, frame, push := fixture.refreshSnapshotAndReadPush(t, uint64(version), nextPorts)
		if push.ConfigVersion != uint64(version) {
			t.Fatalf("expected config version %d, got %d", version, push.ConfigVersion)
		}
		if len(push.Tunnels) != len(nextPorts) {
			t.Fatalf("expected %d tunnels in churn push, got %d", len(nextPorts), len(push.Tunnels))
		}
		if push.Tunnels[0].RemoteStart != nextPorts[0] {
			t.Fatalf("expected tunnel to move to port %d, got %#v", nextPorts[0], push.Tunnels[0])
		}

		writeConfigAck(t, fixture.clientConn, frame.RequestID, push.ConfigVersion)
		waitForRefreshDone(t, refreshDone)
		runningState := fixture.waitForRunning(t, nextPorts)
		assertObservedInvariants(t, &previousObserved, runningState)
		fixture.assertNoHandleLeakForPorts(t, runningState, previousPorts)

		fixture.advancePolls(1)
		stableState := fixture.waitForRunning(t, nextPorts)
		assertObservedInvariants(t, &runningState, stableState)

		previousPorts = nextPorts
		previousObserved = stableState
	}
}

func TestServerStabilityScenarioFakeTimeSoakKeepsResourcesBounded(t *testing.T) {
	cycles := stabilityEnvInt("FRPS_STABILITY_SOAK_CYCLES", 24)
	if cycles < 1 {
		cycles = 1
	}

	goroutinesBefore := runtime.NumGoroutine()
	fixture := newStabilityFixture(t, stabilityFixtureOptions{
		TunnelCount:     4,
		InitialBasePort: 23100,
		RuntimeScanPoll: time.Second,
	})
	defer fixture.Close(t)

	ports := fixture.currentPorts()
	previousObserved := fixture.waitForRunning(t, ports)

	for cycle := 0; cycle < cycles; cycle++ {
		refreshDone, frame, push := fixture.refreshEffectiveIPAndReadPush(t, "127.0.0.2")
		if len(push.Tunnels) != 0 {
			t.Fatalf("expected empty config during soak shrink, got %d tunnels", len(push.Tunnels))
		}
		writeConfigAck(t, fixture.clientConn, frame.RequestID, push.ConfigVersion)
		waitForRefreshDone(t, refreshDone)

		emptyState := fixture.waitForEmptyConfig(t, "当前不存在于本机")
		assertObservedInvariants(t, &previousObserved, emptyState)
		fixture.advancePolls(3)
		stableEmpty := fixture.waitForEmptyConfig(t, "当前不存在于本机")
		assertObservedInvariants(t, &emptyState, stableEmpty)

		refreshDone, frame, push = fixture.refreshEffectiveIPAndReadPush(t, "127.0.0.1")
		if len(push.Tunnels) != len(ports) {
			t.Fatalf("expected full config after soak recovery, got %d tunnels", len(push.Tunnels))
		}
		writeConfigAck(t, fixture.clientConn, frame.RequestID, push.ConfigVersion)
		waitForRefreshDone(t, refreshDone)

		runningState := fixture.waitForRunning(t, ports)
		assertObservedInvariants(t, &stableEmpty, runningState)
		fixture.advancePolls(3)
		stableRunning := fixture.waitForRunning(t, ports)
		assertObservedInvariants(t, &runningState, stableRunning)

		previousObserved = stableRunning
	}

	fixture.Close(t)
	finalObserved := fixture.observe()
	if len(finalObserved.Server.Sessions) != 0 {
		t.Fatalf("expected no sessions after soak fixture shutdown, got %#v", finalObserved.Server.Sessions)
	}
	if len(finalObserved.Server.Listeners) != 0 {
		t.Fatalf("expected no attached listeners after soak fixture shutdown, got %#v", finalObserved.Server.Listeners)
	}
	if finalObserved.Listeners != nil && len(finalObserved.Listeners.Handles) != 0 {
		t.Fatalf("expected no fake listener handles after soak fixture shutdown, got %#v", finalObserved.Listeners.Handles)
	}
	if pendingJobs := fixture.manual.PendingJobNames(); len(pendingJobs) != 0 {
		t.Fatalf("expected no pending scheduler jobs after soak fixture shutdown, got %#v", pendingJobs)
	}

	goroutinesAfter := runtime.NumGoroutine()
	if goroutinesAfter > goroutinesBefore+8 {
		t.Fatalf("expected goroutines to stay bounded after soak, before=%d after=%d", goroutinesBefore, goroutinesAfter)
	}
}

func TestServerStabilityScenarioResourcePressureRefreshAcrossManyTunnelsConverges(t *testing.T) {
	tunnelCount := stabilityEnvInt("FRPS_STABILITY_RESOURCE_TUNNELS", 24)
	if tunnelCount < 2 {
		tunnelCount = 2
	}
	cycles := stabilityEnvInt("FRPS_STABILITY_RESOURCE_CYCLES", 4)
	if cycles < 1 {
		cycles = 1
	}

	fixture := newStabilityFixture(t, stabilityFixtureOptions{
		TunnelCount:     tunnelCount,
		InitialBasePort: 23200,
		RuntimeScanPoll: time.Second,
	})
	defer fixture.Close(t)

	previousPorts := fixture.currentPorts()
	previousObserved := fixture.waitForRunning(t, previousPorts)

	for cycle := 1; cycle <= cycles; cycle++ {
		nextPorts := fixture.sequentialPorts(23200 + cycle*200)
		refreshDone, frame, push := fixture.refreshSnapshotAndReadPush(t, uint64(cycle+1), nextPorts)
		if push.ConfigVersion != uint64(cycle+1) {
			t.Fatalf("expected resource-pressure config version %d, got %d", cycle+1, push.ConfigVersion)
		}
		if len(push.Tunnels) != tunnelCount {
			t.Fatalf("expected %d tunnels under resource pressure, got %d", tunnelCount, len(push.Tunnels))
		}

		writeConfigAck(t, fixture.clientConn, frame.RequestID, push.ConfigVersion)
		waitForRefreshDone(t, refreshDone)
		runningState := fixture.waitForRunning(t, nextPorts)
		assertObservedInvariants(t, &previousObserved, runningState)
		fixture.assertNoHandleLeakForPorts(t, runningState, previousPorts)
		if len(runningState.Server.Listeners) != tunnelCount {
			t.Fatalf("expected %d attached listeners, got %d", tunnelCount, len(runningState.Server.Listeners))
		}

		fixture.advancePolls(2)
		stableState := fixture.waitForRunning(t, nextPorts)
		assertObservedInvariants(t, &runningState, stableState)

		previousPorts = nextPorts
		previousObserved = stableState
	}
}

type stabilityFixtureOptions struct {
	TunnelCount     int
	InitialBasePort int
	RuntimeScanPoll time.Duration
}

type stabilityFixture struct {
	groupID         int64
	manual          *clock.Manual
	repo            *mutableRepository
	network         *mutableSnapshotReader
	listenerFactory *ScriptedListenerFactory
	server          *Server
	clientConn      *connWithRemoteAddr
	done            chan struct{}
	pollCancel      context.CancelFunc
	closeOnce       sync.Once
	host            protocol.Host
}

func newStabilityFixture(t *testing.T, options stabilityFixtureOptions) *stabilityFixture {
	t.Helper()

	if options.TunnelCount <= 0 {
		t.Fatal("stability fixture requires at least one tunnel")
	}
	if options.InitialBasePort <= 0 {
		t.Fatal("stability fixture requires a positive base port")
	}
	if options.RuntimeScanPoll <= 0 {
		options.RuntimeScanPoll = time.Second
	}

	host, err := protocol.ParseHost("127.0.0.1")
	if err != nil {
		t.Fatalf("parse host: %v", err)
	}

	var tokenID [16]byte
	copy(tokenID[:], []byte("token-id-1234567"))

	var tokenSecret [32]byte
	copy(tokenSecret[:], []byte("0123456789abcdef0123456789abcdef"))
	tokenHash := sha256.Sum256(tokenSecret[:])

	manual := clock.NewManual(time.Unix(0, 0))
	listenerFactory := NewScriptedListenerFactory()
	repo := &mutableRepository{
		group: GroupRuntime{
			ID:               1,
			Name:             "group-a",
			Enabled:          true,
			EffectiveIP:      "127.0.0.1",
			ClientSecretHash: tokenHash,
			Snapshot:         buildStabilitySnapshot(host, 1, 100, options.InitialBasePort, options.TunnelCount),
		},
	}
	network := &mutableSnapshotReader{
		snapshot: localIPv4Snapshot("127.0.0.1"),
	}

	server := NewServer(
		Options{
			Repository:      repo,
			Network:         network,
			Clock:           manual,
			Scheduler:       manual,
			ListenerFactory: listenerFactory,
			RuntimeScanPoll: options.RuntimeScanPoll,
			ReadTimeout:     time.Minute,
			WriteTimeout:    time.Minute,
		},
		slog.New(slog.NewTextHandler(io.Discard, nil)),
		"test-server",
	)

	pollCtx, pollCancel := context.WithCancel(context.Background())
	server.startRuntimeIssuePolling(pollCtx)

	clientConn, done, configFrame := authenticateServerSession(t, server, tokenID, tokenHash)
	configPush, err := protocol.UnmarshalConfigPush(configFrame.Body)
	if err != nil {
		t.Fatalf("unmarshal initial config.push: %v", err)
	}
	writeConfigAck(t, clientConn, configFrame.RequestID, configPush.ConfigVersion)

	fixture := &stabilityFixture{
		groupID:         repo.group.ID,
		manual:          manual,
		repo:            repo,
		network:         network,
		listenerFactory: listenerFactory,
		server:          server,
		clientConn:      clientConn,
		done:            done,
		pollCancel:      pollCancel,
		host:            host,
	}

	fixture.waitForRunning(t, fixture.currentPorts())
	return fixture
}

func (f *stabilityFixture) Close(t *testing.T) {
	t.Helper()

	if f == nil {
		return
	}

	f.closeOnce.Do(func() {
		if f.pollCancel != nil {
			f.pollCancel()
		}
		if f.clientConn != nil {
			_ = f.clientConn.Close()
		}
		if f.done != nil {
			select {
			case <-f.done:
			case <-time.After(2 * time.Second):
				t.Fatal("stability fixture server connection did not exit")
			}
		}

		scanDone := make(chan struct{})
		go func() {
			defer close(scanDone)
			f.server.scanWG.Wait()
		}()
		select {
		case <-scanDone:
		case <-time.After(2 * time.Second):
			t.Fatal("stability fixture runtime scan did not stop")
		}

		deadline := time.Now().Add(2 * time.Second)
		for {
			if len(f.manual.PendingJobNames()) == 0 {
				return
			}
			if time.Now().After(deadline) {
				t.Fatalf("stability fixture still has pending scheduler jobs: %#v", f.manual.PendingJobNames())
			}
			time.Sleep(10 * time.Millisecond)
		}
	})
}

func (f *stabilityFixture) observe() sharedtestsupport.ObservedState {
	return observeServerAndListeners(f.server, f.listenerFactory)
}

func (f *stabilityFixture) currentPorts() []uint16 {
	ports := make([]uint16, 0, len(f.repo.group.Snapshot.Tunnels))
	for _, tunnel := range f.repo.group.Snapshot.Tunnels {
		ports = append(ports, tunnel.RemoteStart)
	}
	return ports
}

func (f *stabilityFixture) sequentialPorts(base int) []uint16 {
	ports := make([]uint16, 0, len(f.repo.group.Snapshot.Tunnels))
	for index := range f.repo.group.Snapshot.Tunnels {
		ports = append(ports, uint16(base+index))
	}
	return ports
}

func (f *stabilityFixture) refreshSnapshotAndReadPush(t *testing.T, version uint64, ports []uint16) (chan struct{}, protocol.Frame, protocol.ConfigPush) {
	t.Helper()

	f.repo.group.Snapshot = buildStabilitySnapshot(f.host, version, version*100, int(ports[0]), len(ports))
	for index := range ports {
		f.repo.group.Snapshot.Tunnels[index].RemoteStart = ports[index]
		f.repo.group.Snapshot.Tunnels[index].RemoteEnd = ports[index]
	}
	refreshDone := make(chan struct{})
	go func() {
		defer close(refreshDone)
		f.server.RefreshGroup(f.groupID)
	}()
	frame, push := f.readConfigPush(t)
	return refreshDone, frame, push
}

func (f *stabilityFixture) refreshEffectiveIPAndReadPush(t *testing.T, effectiveIP string) (chan struct{}, protocol.Frame, protocol.ConfigPush) {
	t.Helper()

	f.repo.group.EffectiveIP = effectiveIP
	refreshDone := make(chan struct{})
	go func() {
		defer close(refreshDone)
		f.server.RefreshGroup(f.groupID)
	}()
	frame, push := f.readConfigPush(t)
	return refreshDone, frame, push
}

func (f *stabilityFixture) readConfigPush(t *testing.T) (protocol.Frame, protocol.ConfigPush) {
	t.Helper()

	frame, err := readMessageWithin(f.clientConn, time.Second)
	if err != nil {
		t.Fatalf("read config.push: %v", err)
	}
	if frame.Type != protocol.TypeConfigPush {
		t.Fatalf("expected config.push, got %#v", frame)
	}
	push, err := protocol.UnmarshalConfigPush(frame.Body)
	if err != nil {
		t.Fatalf("unmarshal config.push: %v", err)
	}
	return frame, push
}

func (f *stabilityFixture) advancePolls(rounds int) {
	for range rounds {
		f.manual.Advance(time.Second)
		f.manual.WaitIdle()
	}
}

func (f *stabilityFixture) waitForRunning(t *testing.T, ports []uint16) sharedtestsupport.ObservedState {
	t.Helper()

	expectedKeys := listenerKeysForPorts("127.0.0.1", ports)
	return waitForObservedState(t, f.server, f.listenerFactory, func(observed sharedtestsupport.ObservedState) bool {
		session := findObservedSession(observed.Server, f.groupID)
		if session == nil || session.Pending != nil || session.RecoveryMode != sharedtestsupport.RecoveryModeRunning {
			return false
		}
		if len(observed.Server.Sessions) != 1 || len(observed.Server.GroupSlots) != 1 || observed.Server.GroupSlots[f.groupID] != session.SessionID {
			return false
		}
		if len(observed.Server.Listeners) != len(expectedKeys) || len(observed.Server.MissingListeners) != 0 {
			return false
		}
		if observed.Listeners == nil || len(observed.Listeners.Handles) != len(expectedKeys) {
			return false
		}
		for _, key := range expectedKeys {
			if handleCountForPort(observed, key) != 1 {
				return false
			}
		}
		for _, tunnel := range observed.Server.Tunnels {
			if tunnel.GroupID != f.groupID {
				continue
			}
			if tunnel.FinalStatus != "启用" || tunnel.RuntimeIssue != "" {
				return false
			}
		}
		return true
	})
}

func (f *stabilityFixture) waitForEmptyConfig(t *testing.T, issueSubstring string) sharedtestsupport.ObservedState {
	t.Helper()

	return waitForObservedState(t, f.server, f.listenerFactory, func(observed sharedtestsupport.ObservedState) bool {
		session := findObservedSession(observed.Server, f.groupID)
		if session == nil || session.Pending != nil || session.RecoveryMode != sharedtestsupport.RecoveryModeEmptyConfig {
			return false
		}
		if len(observed.Server.Listeners) != 0 || len(observed.Server.MissingListeners) != 0 {
			return false
		}
		if observed.Listeners == nil {
			return false
		}
		if len(observed.Listeners.Handles) != 0 {
			return false
		}
		for _, tunnel := range observed.Server.Tunnels {
			if tunnel.GroupID != f.groupID {
				continue
			}
			if tunnel.FinalStatus != "异常" || issueSubstring != "" && !containsSubstring(tunnel.RuntimeIssue, issueSubstring) {
				return false
			}
		}
		return true
	})
}

func (f *stabilityFixture) assertNoHandleLeakForPorts(t *testing.T, observed sharedtestsupport.ObservedState, oldPorts []uint16) {
	t.Helper()

	for _, port := range oldPorts {
		key := ListenKey{Protocol: "tcp", IP: "127.0.0.1", Port: port}
		if handleCountForPort(observed, key) != 0 {
			t.Fatalf("expected no listener handle leak for old port %d, got %#v", port, observed.Listeners)
		}
	}
}

func assertObservedInvariants(t *testing.T, before *sharedtestsupport.ObservedState, after sharedtestsupport.ObservedState) {
	t.Helper()

	violations := sharedtestsupport.CheckAll(sharedtestsupport.InvariantCheckInput{
		Before: before,
		After:  &after,
	})
	if len(violations) == 0 {
		return
	}
	t.Fatalf("observed invariant violations: %#v", violations)
}

func listenerKeysForPorts(ip string, ports []uint16) []ListenKey {
	keys := make([]ListenKey, 0, len(ports))
	for _, port := range ports {
		keys = append(keys, ListenKey{
			Protocol: "tcp",
			IP:       ip,
			Port:     port,
		})
	}
	return keys
}

func buildStabilitySnapshot(host protocol.Host, version uint64, generatedAtMs uint64, basePort int, tunnelCount int) ConfigSnapshot {
	tunnels := make([]protocol.TunnelEntry, 0, tunnelCount)
	for index := range tunnelCount {
		port := uint16(basePort + index)
		tunnels = append(tunnels, protocol.TunnelEntry{
			TunnelID:    uint32(index + 1),
			Protocol:    protocol.ProtocolTCP,
			TunnelFlags: protocol.TunnelFlagEnabled,
			RemoteStart: port,
			RemoteEnd:   port,
			LocalHost:   host,
			LocalStart:  uint16(2200 + index),
			LocalEnd:    uint16(2200 + index),
		})
	}
	return ConfigSnapshot{
		Version:       version,
		GeneratedAtMs: generatedAtMs,
		Tunnels:       tunnels,
	}
}

func stabilityEnvInt(key string, defaultValue int) int {
	raw := strings.TrimSpace(runtimeEnv(key))
	if raw == "" {
		return defaultValue
	}

	value, err := strconv.Atoi(raw)
	if err != nil || value <= 0 {
		panic(fmt.Sprintf("%s must be a positive integer, got %q", key, raw))
	}
	return value
}

func runtimeEnv(key string) string {
	return os.Getenv(key)
}

func containsSubstring(source string, needle string) bool {
	return needle == "" || source != "" && strings.Contains(source, needle)
}

func waitForRefreshDone(t *testing.T, done <-chan struct{}) {
	t.Helper()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("refresh group did not complete")
	}
}
