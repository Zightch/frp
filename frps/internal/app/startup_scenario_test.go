package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/zightch/frp/frps/internal/certassets"
	"github.com/zightch/frp/frps/internal/config"
	"github.com/zightch/frp/frps/internal/control"
	"github.com/zightch/frp/frps/internal/storage"
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

func TestAppStartupScenarioBlocksInitialScanUntilCertificatePreparationReleases(t *testing.T) {
	controller := testhooks.NewController()
	controller.AddBarrier("startup.certificate_assets.before_prepare", 1)
	controller.AddBarrier("startup.initial_scan.before_full_scan", 1)
	restoreHooks := testhooks.Install(controller)
	defer restoreHooks()

	allowPrepare := make(chan struct{})
	originalPrepare := prepareCertificateAssetRuntime
	prepareCertificateAssetRuntime = func(ctx context.Context, store *storage.SQL) (*certassets.Runtime, error) {
		testhooks.Point("startup.certificate_assets.prepare_entered")
		select {
		case <-allowPrepare:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		return &certassets.Runtime{}, nil
	}
	defer func() {
		prepareCertificateAssetRuntime = originalPrepare
	}()

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
		Name: "startup-certificate-preparation-gates-initial-scan",
		Steps: []scenariotest.ScenarioStep{
			{
				Name:    "hold-before-certificate-prepare",
				Actor:   "frps",
				WaitFor: []scenariotest.BarrierRef{{Point: "startup.certificate_assets.before_prepare", HitIndex: 1}},
				Do: func(context.Context) error {
					if err := expectTCPClosed(controlAddr); err != nil {
						return err
					}
					if err := expectTCPClosed(managementAddr); err != nil {
						return err
					}
					if hits := controller.Hits("startup.initial_scan.before_full_scan"); len(hits) != 0 {
						return fmt.Errorf("initial runtime scan started before certificate preparation: %#v", hits)
					}
					return nil
				},
				Observe: "before-prepare",
				Assert: []scenariotest.InvariantFunc{
					expectAppState(sharedtestsupport.AppObservedState{
						InitialRuntimeScanDone: false,
						ControlListenerOpen:    false,
						LoginGateOpen:          false,
						ManagementAPIVisible:   false,
					}),
				},
			},
			{
				Name:    "release-before-certificate-prepare",
				Actor:   "test",
				Release: []scenariotest.BarrierRef{{Point: "startup.certificate_assets.before_prepare", HitIndex: 1}},
			},
			{
				Name:    "prepare-entered-and-blocked",
				Actor:   "frps",
				WaitFor: []scenariotest.BarrierRef{{Point: "startup.certificate_assets.prepare_entered", HitIndex: 1}},
				Do: func(context.Context) error {
					if err := expectTCPClosed(controlAddr); err != nil {
						return err
					}
					if err := expectTCPClosed(managementAddr); err != nil {
						return err
					}
					if hits := controller.Hits("startup.initial_scan.before_full_scan"); len(hits) != 0 {
						return fmt.Errorf("initial runtime scan started while certificate preparation was blocked: %#v", hits)
					}
					return nil
				},
				Observe: "prepare-blocked",
				Assert: []scenariotest.InvariantFunc{
					expectAppState(sharedtestsupport.AppObservedState{
						InitialRuntimeScanDone: false,
						ControlListenerOpen:    false,
						LoginGateOpen:          false,
						ManagementAPIVisible:   false,
					}),
				},
			},
			{
				Name:  "release-certificate-prepare",
				Actor: "test",
				Do: func(context.Context) error {
					close(allowPrepare)
					return nil
				},
			},
			{
				Name:    "scan-starts-after-certificate-prepare",
				Actor:   "frps",
				WaitFor: []scenariotest.BarrierRef{{Point: "startup.initial_scan.before_full_scan", HitIndex: 1}},
				Do: func(context.Context) error {
					if err := expectTCPClosed(controlAddr); err != nil {
						return err
					}
					if err := expectTCPClosed(managementAddr); err != nil {
						return err
					}
					return nil
				},
				Observe: "scan-starting",
				Assert: []scenariotest.InvariantFunc{
					expectAppState(sharedtestsupport.AppObservedState{
						InitialRuntimeScanDone: false,
						ControlListenerOpen:    false,
						LoginGateOpen:          false,
						ManagementAPIVisible:   false,
					}),
				},
			},
			{
				Name:    "release-initial-scan",
				Actor:   "test",
				Release: []scenariotest.BarrierRef{{Point: "startup.initial_scan.before_full_scan", HitIndex: 1}},
			},
		},
	})
	if err != nil {
		t.Fatalf("run startup certificate preparation scenario: %v", err)
	}

	waitForHookHit(t, controller, "startup.control_listener.after_open", 1)
	waitForHookHit(t, controller, "startup.management_api.after_open", 1)

	if observed := result.Observations["prepare-blocked"].App; observed.InitialRuntimeScanDone {
		t.Fatalf("expected initial runtime scan to remain unpublished while certificate preparation is blocked, got %#v", observed)
	}
}

func TestAppStartupScenarioFirstManagementResponseContainsInitialRuntimeStatus(t *testing.T) {
	workdir := t.TempDir()
	t.Chdir(workdir)

	controller := testhooks.NewController()
	controller.AddBarrier("startup.management_api.before_open", 1)
	restoreHooks := testhooks.Install(controller)
	defer restoreHooks()

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	controlPort := freeTCPPort(t)
	managementPort := freeTCPPortExcept(t, controlPort)
	blockedPort := freeTCPPortExcept(t, controlPort, managementPort)

	blocker, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(blockedPort)))
	if err != nil {
		t.Fatalf("listen blocker: %v", err)
	}
	defer blocker.Close()

	dbPath := filepath.Join(workdir, "frps.sqlite")
	seedStartupScenarioDatabase(t, dbPath, blockedPort)

	application := New(config.Config{
		ControlListenAddr:    net.JoinHostPort("127.0.0.1", strconv.Itoa(controlPort)),
		ManagementListenAddr: net.JoinHostPort("127.0.0.1", strconv.Itoa(managementPort)),
		ReadHeaderTimeout:    "1s",
		ShutdownTimeout:      "1s",
		Database: config.DatabaseConfig{
			Type: "sqlite",
			Path: dbPath,
		},
		WebUI: config.WebUIConfig{
			DistDir: filepath.Join(workdir, "missing-webui"),
		},
	}, logger, "test-server")

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- application.Run(ctx)
	}()
	defer func() {
		cancel()
		waitForAppRunExit(t, done)
	}()

	waitForHookHit(t, controller, "startup.management_api.before_open", 1)

	token := issueStartupManagementToken(t, application, "startup-management-secret")

	preVisible := application.ObserveState()
	if preVisible.App.ManagementAPIVisible {
		t.Fatal("expected management api to remain hidden while before_open barrier is held")
	}
	if len(preVisible.Server.Tunnels) != 1 {
		t.Fatalf("expected one observed tunnel before management api becomes visible, got %#v", preVisible.Server.Tunnels)
	}
	if preVisible.Server.Tunnels[0].FinalStatus != "异常" || !strings.Contains(preVisible.Server.Tunnels[0].RuntimeIssue, "端口冲突") {
		t.Fatalf("expected initial scan to finish before management api opens, got %#v", preVisible.Server.Tunnels[0])
	}

	if !controller.Release("startup.management_api.before_open", 1) {
		t.Fatal("release management_api.before_open barrier failed")
	}
	waitForHookHit(t, controller, "startup.management_api.after_open", 1)

	payload := waitForAuthorizedJSON(
		t,
		"http://"+net.JoinHostPort("127.0.0.1", strconv.Itoa(managementPort))+"/api/v1/tunnels",
		token,
	)
	items, ok := payload["items"].([]any)
	if !ok || len(items) != 1 {
		t.Fatalf("unexpected tunnel payload: %#v", payload)
	}
	item, ok := items[0].(map[string]any)
	if !ok {
		t.Fatalf("unexpected tunnel item payload: %#v", items[0])
	}
	if item["status"] != "异常" {
		t.Fatalf("expected first visible tunnel status to already be 异常, got %#v", item)
	}
	reason, _ := item["status_reason"].(string)
	if !strings.Contains(reason, "端口冲突") {
		t.Fatalf("expected first visible tunnel reason to contain port conflict, got %#v", item)
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
			ID:               1,
			Name:             "group-a",
			Enabled:          true,
			EffectiveIP:      system.AnyIPv4,
			ClientSecretHash: tokenHash,
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

func seedStartupScenarioDatabase(t *testing.T, dbPath string, blockedPort int) {
	t.Helper()

	db, err := openDatabase(context.Background(), config.DatabaseConfig{
		Type: "sqlite",
		Path: dbPath,
	})
	if err != nil {
		t.Fatalf("open startup scenario database: %v", err)
	}
	defer db.Close()

	store := storage.NewSQL(appStoreObjectID)
	if err := store.SetConn(db); err != nil {
		t.Fatalf("attach startup scenario store: %v", err)
	}
	defer store.Close()

	if err := ensureDatabaseSchema(context.Background(), store, "sqlite"); err != nil {
		t.Fatalf("ensure startup scenario schema: %v", err)
	}

	clientID := hex.EncodeToString([]byte("startup-scan-token-id-1234567890"))[:32]
	tokenHash := sha256.Sum256([]byte("startup-scan-token-secret"))
	now := time.Now().UTC().Format("2006-01-02 15:04:05.000000")

	if _, err := store.Exec(
		`INSERT INTO proxy_groups (id, name, client_id, client_secret_hash, effective_ip, enabled, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		1,
		"group-a",
		clientID,
		hex.EncodeToString(tokenHash[:]),
		"127.0.0.1",
		1,
		now,
		now,
	); err != nil {
		t.Fatalf("insert startup scenario proxy group: %v", err)
	}

	if _, err := store.Exec(
		`INSERT INTO tunnels (id, group_id, name, protocol, remote_type, remote_start, remote_end, local_host, local_start, local_end, enabled, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		7,
		1,
		"blocked-tunnel",
		"tcp",
		"single",
		blockedPort,
		blockedPort,
		"127.0.0.1",
		8080,
		8080,
		1,
		now,
		now,
	); err != nil {
		t.Fatalf("insert startup scenario tunnel: %v", err)
	}
}

func issueStartupManagementToken(t *testing.T, application *App, secret string) string {
	t.Helper()

	if application == nil || application.auth == nil {
		t.Fatal("management auth is not initialized")
	}

	secretHash := sha256.Sum256([]byte(secret))
	if err := application.auth.Initialize(hex.EncodeToString(secretHash[:])); err != nil {
		t.Fatalf("initialize management auth: %v", err)
	}
	challenge, err := application.auth.IssueChallenge()
	if err != nil {
		t.Fatalf("issue management auth challenge: %v", err)
	}
	proof := sha256.Sum256([]byte(hex.EncodeToString(secretHash[:]) + challenge.Salt))
	_, token, err := application.auth.Login(challenge.ID, hex.EncodeToString(proof[:]))
	if err != nil {
		t.Fatalf("login management auth: %v", err)
	}
	return token
}

func waitForAuthorizedJSON(t *testing.T, url string, token string) map[string]any {
	t.Helper()

	deadline := time.Now().Add(2 * time.Second)
	client := &http.Client{Timeout: 200 * time.Millisecond}
	for {
		request, err := http.NewRequest(http.MethodGet, url, nil)
		if err != nil {
			t.Fatalf("new request %s: %v", url, err)
		}
		request.Header.Set("Authorization", "Bearer "+token)

		response, err := client.Do(request)
		if err == nil {
			rawBody, readErr := io.ReadAll(response.Body)
			_ = response.Body.Close()
			if readErr != nil {
				t.Fatalf("read response body for %s: %v", url, readErr)
			}
			if response.StatusCode == http.StatusOK {
				payload := make(map[string]any)
				if err := json.Unmarshal(rawBody, &payload); err != nil {
					t.Fatalf("decode response body for %s: %v body=%s", url, err, string(rawBody))
				}
				return payload
			}
		}

		if time.Now().After(deadline) {
			if err != nil {
				t.Fatalf("request %s: %v", url, err)
			}
			t.Fatalf("timed out waiting for %s to return 200", url)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
