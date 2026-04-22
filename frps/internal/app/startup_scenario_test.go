//go:build testhooks

package app

import (
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"log/slog"
	"net"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/zightch/frp/frps/internal/config"
	"github.com/zightch/frp/frps/internal/control"
	"github.com/zightch/frp/frps/internal/system"
	"github.com/zightch/frp/frps/internal/testhooks"
	scenariotest "github.com/zightch/frp/frps/internal/testsupport"
	"github.com/zightch/frp/frps/pkg/protocol"
	sharedtestsupport "github.com/zightch/frp/frps/pkg/testsupport"
)

func TestAppStartupScenarioBlocksVisibilityUntilInitialScanReleases(t *testing.T) {
	controller := testhooks.NewController()
	controller.AddBarrier("startup.initial_scan.after_full_scan", 1)
	restoreHooks := testhooks.Install(controller)
	defer restoreHooks()

	application, controlAddr, managementAddr, cancel, done := startStartupScenarioApp(t)
	defer func() {
		cancel()
		waitForAppRunExit(t, done)
	}()

	ctx, timeoutCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer timeoutCancel()

	runner := &scenariotest.Runner{
		Hooks:   controller,
		Observe: application.ObserveState,
	}
	result, err := runner.Run(ctx, scenariotest.Scenario{
		Name: "startup-gate-before-initial-scan-release",
		Steps: []scenariotest.ScenarioStep{
			{
				Name:    "hold-after-full-scan",
				Actor:   "frps",
				WaitFor: []scenariotest.BarrierRef{{Point: "startup.initial_scan.after_full_scan", HitIndex: 1}},
				Do: func(context.Context) error {
					if err := expectTCPClosed(controlAddr); err != nil {
						return err
					}
					if err := expectTCPClosed(managementAddr); err != nil {
						return err
					}
					return nil
				},
				Observe: "scan-held",
				Assert: []scenariotest.InvariantFunc{
					sharedtestsupport.CheckStartupGate,
				},
			},
			{
				Name:    "release-initial-scan",
				Actor:   "test",
				Release: []scenariotest.BarrierRef{{Point: "startup.initial_scan.after_full_scan", HitIndex: 1}},
			},
		},
	})
	if err != nil {
		t.Fatalf("run startup gate scenario: %v", err)
	}

	observed := result.Observations["scan-held"]
	if observed.App.InitialRuntimeScanDone {
		t.Fatal("expected initial runtime scan to remain unpublished while barrier is held")
	}
	if observed.App.ControlListenerOpen || observed.App.LoginGateOpen || observed.App.ManagementAPIVisible {
		t.Fatalf("unexpected startup state while scan barrier held: %#v", observed.App)
	}

	waitForHookHit(t, controller, "startup.control_listener.after_open", 1)
	waitForHookHit(t, controller, "startup.management_api.after_open", 1)
}

func TestAppStartupScenarioPublishesControlBeforeManagementVisibility(t *testing.T) {
	controller := testhooks.NewController()
	controller.AddBarrier("startup.control_listener.before_open", 1)
	controller.AddBarrier("startup.management_api.before_open", 1)
	restoreHooks := testhooks.Install(controller)
	defer restoreHooks()

	application, controlAddr, managementAddr, cancel, done := startStartupScenarioApp(t)
	defer func() {
		cancel()
		waitForAppRunExit(t, done)
	}()

	ctx, timeoutCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer timeoutCancel()

	runner := &scenariotest.Runner{
		Hooks:   controller,
		Observe: application.ObserveState,
	}
	result, err := runner.Run(ctx, scenariotest.Scenario{
		Name: "startup-control-before-management",
		Steps: []scenariotest.ScenarioStep{
			{
				Name:  "wait-both-open-points",
				Actor: "frps",
				WaitFor: []scenariotest.BarrierRef{
					{Point: "startup.control_listener.before_open", HitIndex: 1},
					{Point: "startup.management_api.before_open", HitIndex: 1},
				},
				Do: func(context.Context) error {
					if err := expectTCPClosed(controlAddr); err != nil {
						return err
					}
					if err := expectTCPClosed(managementAddr); err != nil {
						return err
					}
					return nil
				},
				Observe: "post-scan-pre-open",
				Assert: []scenariotest.InvariantFunc{
					expectAppState(sharedtestsupport.AppObservedState{
						InitialRuntimeScanDone: true,
						ControlListenerOpen:    false,
						LoginGateOpen:          false,
						ManagementAPIVisible:   false,
					}),
				},
			},
			{
				Name:    "release-control-open",
				Actor:   "test",
				Release: []scenariotest.BarrierRef{{Point: "startup.control_listener.before_open", HitIndex: 1}},
			},
			{
				Name:    "observe-control-open",
				Actor:   "frps",
				WaitFor: []scenariotest.BarrierRef{{Point: "startup.control_listener.after_open", HitIndex: 1}},
				Do: func(context.Context) error {
					if err := expectTCPOpen(controlAddr); err != nil {
						return err
					}
					if err := expectTCPClosed(managementAddr); err != nil {
						return err
					}
					return nil
				},
				Observe: "control-open-only",
				Assert: []scenariotest.InvariantFunc{
					expectAppState(sharedtestsupport.AppObservedState{
						InitialRuntimeScanDone: true,
						ControlListenerOpen:    true,
						LoginGateOpen:          true,
						ManagementAPIVisible:   false,
					}),
				},
			},
			{
				Name:    "release-management-open",
				Actor:   "test",
				Release: []scenariotest.BarrierRef{{Point: "startup.management_api.before_open", HitIndex: 1}},
			},
			{
				Name:    "observe-management-open",
				Actor:   "frps",
				WaitFor: []scenariotest.BarrierRef{{Point: "startup.management_api.after_open", HitIndex: 1}},
				Do: func(context.Context) error {
					if err := expectTCPOpen(controlAddr); err != nil {
						return err
					}
					if err := expectTCPOpen(managementAddr); err != nil {
						return err
					}
					return nil
				},
				Observe: "management-open",
				Assert: []scenariotest.InvariantFunc{
					expectAppState(sharedtestsupport.AppObservedState{
						InitialRuntimeScanDone: true,
						ControlListenerOpen:    true,
						LoginGateOpen:          true,
						ManagementAPIVisible:   true,
					}),
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("run startup ordering scenario: %v", err)
	}

	if got := result.Observations["control-open-only"].App.ManagementAPIVisible; got {
		t.Fatal("expected management api to remain hidden when only control listener has been released")
	}
}

func startStartupScenarioApp(t *testing.T) (*App, string, string, context.CancelFunc, <-chan error) {
	t.Helper()

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	controlPort := freeTCPPort(t)
	managementPort := freeTCPPortExcept(t, controlPort)
	remotePort := freeTCPPortExcept(t, controlPort, managementPort)

	host, err := protocol.ParseHost("127.0.0.1")
	if err != nil {
		t.Fatalf("parse host: %v", err)
	}

	tokenHash := sha256.Sum256([]byte("startup-scan-token"))
	repo := &blockingControlRepository{
		group: control.GroupRuntime{
			ID:          1,
			Name:        "group-a",
			Enabled:     true,
			EffectiveIP: system.AnyIPv4,
			TokenHash:   tokenHash,
			Snapshot: control.ConfigSnapshot{
				Version:       1,
				GeneratedAtMs: 1,
				Tunnels: []protocol.TunnelEntry{
					{
						TunnelID:    7,
						Protocol:    protocol.ProtocolTCP,
						TunnelFlags: protocol.TunnelFlagEnabled,
						RemoteStart: uint16(remotePort),
						RemoteEnd:   uint16(remotePort),
						LocalHost:   host,
						LocalStart:  22,
						LocalEnd:    22,
					},
				},
			},
		},
	}

	originalNewControlServer := newControlServer
	newControlServer = func(options control.Options, logger *slog.Logger, version string) *control.Server {
		options.Repository = repo
		options.Store = nil
		options.RuntimeScanPoll = time.Hour
		return control.NewServer(options, logger, version)
	}
	t.Cleanup(func() {
		newControlServer = originalNewControlServer
	})

	application := New(config.Config{
		ControlListenAddr:    net.JoinHostPort("127.0.0.1", strconv.Itoa(controlPort)),
		ManagementListenAddr: net.JoinHostPort("127.0.0.1", strconv.Itoa(managementPort)),
		ReadHeaderTimeout:    "1s",
		ShutdownTimeout:      "1s",
		Database: config.DatabaseConfig{
			Type: "sqlite",
			Path: filepath.Join(t.TempDir(), "frps.sqlite"),
		},
		WebUI: config.WebUIConfig{
			DistDir: filepath.Join(t.TempDir(), "missing-webui"),
		},
	}, logger, "test-server")

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- application.Run(ctx)
	}()

	return application, net.JoinHostPort("127.0.0.1", strconv.Itoa(controlPort)), net.JoinHostPort("127.0.0.1", strconv.Itoa(managementPort)), cancel, done
}

func expectAppState(expected sharedtestsupport.AppObservedState) scenariotest.InvariantFunc {
	return func(input sharedtestsupport.InvariantCheckInput) []sharedtestsupport.InvariantViolation {
		if input.After == nil {
			return []sharedtestsupport.InvariantViolation{{
				Rule:     "app.state.available",
				Summary:  "缺少应用状态观测",
				Expected: "ObservedState.After != nil",
				Actual:   "ObservedState.After == nil",
				Fields:   []string{"app"},
			}}
		}

		actual := input.After.App
		if actual == expected {
			return nil
		}

		return []sharedtestsupport.InvariantViolation{{
			Rule:     "app.state.matches_expected",
			Summary:  "应用启动阶段状态与预期不一致",
			Expected: fmt.Sprintf("%+v", expected),
			Actual:   fmt.Sprintf("%+v", actual),
			Fields: []string{
				"app.initial_runtime_scan_done",
				"app.control_listener_open",
				"app.login_gate_open",
				"app.management_api_visible",
			},
		}}
	}
}

func expectTCPClosed(addr string) error {
	conn, err := net.DialTimeout("tcp", addr, 100*time.Millisecond)
	if err == nil {
		_ = conn.Close()
		return fmt.Errorf("expected %s to remain closed", addr)
	}
	return nil
}

func expectTCPOpen(addr string) error {
	deadline := time.Now().Add(time.Second)
	for {
		conn, err := net.DialTimeout("tcp", addr, 100*time.Millisecond)
		if err == nil {
			_ = conn.Close()
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("expected %s to be open: %w", addr, err)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func waitForHookHit(t *testing.T, controller *testhooks.Controller, point string, hitIndex int) {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := controller.WaitUntilHit(ctx, point, hitIndex); err != nil {
		t.Fatalf("wait for hook %s[%d]: %v", point, hitIndex, err)
	}
}

func waitForAppRunExit(t *testing.T, done <-chan error) {
	t.Helper()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("app run returned error: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("app run did not exit")
	}
}
