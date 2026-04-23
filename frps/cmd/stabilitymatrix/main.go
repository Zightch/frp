package main

import (
	"flag"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"slices"
	"strings"
)

type goTestRun struct {
	Name    string
	Package string
	Run     string
	Timeout string
	Count   int
	CPU     string
	Race    bool
	Tags    []string
	Env     map[string]string
}

type profile struct {
	Name        string
	Description string
	Runs        []goTestRun
}

func main() {
	os.Exit(runMain())
}

func runMain() int {
	var (
		profileSpec = flag.String("profile", "platform", "comma-separated profile list, or all")
		goBin       = flag.String("go", "go", "go executable to invoke")
		listOnly    = flag.Bool("list", false, "list available stability profiles")
		dryRun      = flag.Bool("dry-run", false, "print commands without executing them")
	)
	flag.Parse()

	catalog := profileCatalog()
	if *listOnly {
		printProfiles(catalog)
		return 0
	}

	selected, err := resolveProfiles(*profileSpec, catalog)
	if err != nil {
		fmt.Fprintf(os.Stderr, "resolve profiles: %v\n", err)
		return 1
	}

	fmt.Printf("stabilitymatrix: selected=%s goos=%s goarch=%s\n", profileNames(selected), runtime.GOOS, runtime.GOARCH)
	for _, current := range selected {
		fmt.Printf("==> profile %s: %s\n", current.Name, current.Description)
		for _, run := range current.Runs {
			if err := executeRun(*goBin, run, *dryRun); err != nil {
				fmt.Fprintf(os.Stderr, "profile %s run %s failed: %v\n", current.Name, run.Name, err)
				return 1
			}
		}
	}

	fmt.Println("stabilitymatrix: all selected profiles passed")
	return 0
}

func profileCatalog() map[string]profile {
	return map[string]profile{
		"race": {
			Name:        "race",
			Description: "用 -race 回归核心确定性场景和稳定性场景。",
			Runs: []goTestRun{
				{
					Name:    "app-startup-race",
					Package: "./internal/app",
					Run:     anchored(appStartupTests()...),
					Timeout: "180s",
					Count:   1,
					Race:    true,
					Tags:    []string{"testhooks"},
				},
				{
					Name:    "control-core-race",
					Package: "./internal/control",
					Run:     anchored(controlCoreTests()...),
					Timeout: "240s",
					Count:   1,
					Race:    true,
					Tags:    []string{"testhooks"},
				},
				{
					Name:    "control-stability-race",
					Package: "./internal/control",
					Run:     anchored(stabilityTests()...),
					Timeout: "240s",
					Count:   1,
					Race:    true,
					Tags:    []string{"testhooks"},
				},
			},
		},
		"gomaxprocs": {
			Name:        "gomaxprocs",
			Description: "在多 GOMAXPROCS 档位下复跑同一批确定性脚本。",
			Runs: []goTestRun{
				{
					Name:    "app-startup-cpu-matrix",
					Package: "./internal/app",
					Run:     anchored(appStartupTests()...),
					Timeout: "180s",
					Count:   1,
					CPU:     "1,2,4",
					Tags:    []string{"testhooks"},
				},
				{
					Name:    "control-core-cpu-matrix",
					Package: "./internal/control",
					Run:     anchored(controlCoreTests()...),
					Timeout: "240s",
					Count:   1,
					CPU:     "1,2,4",
					Tags:    []string{"testhooks"},
				},
				{
					Name:    "control-stability-cpu-matrix",
					Package: "./internal/control",
					Run:     anchored(stabilityTests()...),
					Timeout: "240s",
					Count:   1,
					CPU:     "1,2,4",
					Tags:    []string{"testhooks"},
				},
			},
		},
		"config-churn": {
			Name:        "config-churn",
			Description: "高频配置抖动回归，验证重复 RefreshGroup/空满配置切换仍能收敛。",
			Runs: []goTestRun{
				{
					Name:    "control-config-churn",
					Package: "./internal/control",
					Run:     anchored(configChurnTests()...),
					Timeout: "240s",
					Count:   5,
					Tags:    []string{"testhooks"},
					Env: map[string]string{
						"FRPS_STABILITY_CHURN_VERSIONS": "24",
					},
				},
			},
		},
		"soak": {
			Name:        "soak",
			Description: "长稳 soak 回归，使用 fake time 长周期推进后验证资源不泄漏。",
			Runs: []goTestRun{
				{
					Name:    "control-soak",
					Package: "./internal/control",
					Run:     anchored("TestServerStabilityScenarioFakeTimeSoakKeepsResourcesBounded"),
					Timeout: "240s",
					Count:   1,
					Tags:    []string{"testhooks"},
					Env: map[string]string{
						"FRPS_STABILITY_SOAK_CYCLES": "64",
					},
				},
			},
		},
		"platform": {
			Name:        "platform",
			Description: "平台基线入口；同一命令在 Windows 和 Linux/WSL 上应得到同一业务结论。",
			Runs: []goTestRun{
				{
					Name:    "app-startup-platform-baseline",
					Package: "./internal/app",
					Run:     anchored(appStartupTests()...),
					Timeout: "180s",
					Count:   1,
					Tags:    []string{"testhooks"},
				},
				{
					Name:    "control-core-platform-baseline",
					Package: "./internal/control",
					Run:     anchored(controlCoreTests()...),
					Timeout: "240s",
					Count:   1,
					Tags:    []string{"testhooks"},
				},
				{
					Name:    "control-stability-platform-baseline",
					Package: "./internal/control",
					Run:     anchored(stabilityTests()...),
					Timeout: "240s",
					Count:   1,
					Tags:    []string{"testhooks"},
				},
			},
		},
		"resource-pressure": {
			Name:        "resource-pressure",
			Description: "资源压力回归，放大 tunnel 数量和 refresh 次数，验证 listener 资源仍可收敛。",
			Runs: []goTestRun{
				{
					Name:    "control-resource-pressure",
					Package: "./internal/control",
					Run:     anchored(resourcePressureTests()...),
					Timeout: "240s",
					Count:   3,
					Tags:    []string{"testhooks"},
					Env: map[string]string{
						"FRPS_STABILITY_RESOURCE_TUNNELS": "48",
						"FRPS_STABILITY_RESOURCE_CYCLES":  "6",
					},
				},
			},
		},
	}
}

func resolveProfiles(spec string, catalog map[string]profile) ([]profile, error) {
	order := []string{"race", "gomaxprocs", "config-churn", "soak", "platform", "resource-pressure"}
	if strings.TrimSpace(spec) == "" {
		spec = "platform"
	}

	var selected []profile
	seen := make(map[string]struct{})
	appendProfile := func(name string) error {
		current, ok := catalog[name]
		if !ok {
			return fmt.Errorf("unknown profile %q", name)
		}
		if _, ok := seen[name]; ok {
			return nil
		}
		seen[name] = struct{}{}
		selected = append(selected, current)
		return nil
	}

	for _, raw := range strings.Split(spec, ",") {
		name := strings.TrimSpace(raw)
		if name == "" {
			continue
		}
		if name == "all" {
			for _, entry := range order {
				if err := appendProfile(entry); err != nil {
					return nil, err
				}
			}
			continue
		}
		if err := appendProfile(name); err != nil {
			return nil, err
		}
	}

	if len(selected) == 0 {
		return nil, fmt.Errorf("no profiles selected")
	}
	return selected, nil
}

func executeRun(goBin string, run goTestRun, dryRun bool) error {
	args := buildGoTestArgs(run)
	fmt.Printf("  -> %s\n", formatCommand(goBin, args, run.Env))
	if dryRun {
		return nil
	}

	cmd := exec.Command(goBin, args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Env = mergedEnv(run.Env)
	return cmd.Run()
}

func buildGoTestArgs(run goTestRun) []string {
	args := []string{"test", "-parallel=1"}
	if run.Count > 0 {
		args = append(args, fmt.Sprintf("-count=%d", run.Count))
	}
	if run.Race {
		args = append(args, "-race")
	}
	if len(run.Tags) > 0 {
		args = append(args, "-tags", strings.Join(run.Tags, ","))
	}
	if run.CPU != "" {
		args = append(args, "-cpu", run.CPU)
	}
	if run.Run != "" {
		args = append(args, "-run", run.Run)
	}
	if run.Timeout != "" {
		args = append(args, "-timeout", run.Timeout)
	}
	args = append(args, run.Package)
	return args
}

func formatCommand(goBin string, args []string, env map[string]string) string {
	parts := make([]string, 0, len(env)+len(args)+1)
	if len(env) > 0 {
		keys := make([]string, 0, len(env))
		for key := range env {
			keys = append(keys, key)
		}
		slices.Sort(keys)
		for _, key := range keys {
			parts = append(parts, fmt.Sprintf("%s=%s", key, env[key]))
		}
	}
	parts = append(parts, goBin)
	parts = append(parts, args...)
	return strings.Join(parts, " ")
}

func mergedEnv(overrides map[string]string) []string {
	if len(overrides) == 0 {
		return os.Environ()
	}

	envMap := make(map[string]string)
	for _, entry := range os.Environ() {
		key, value, ok := strings.Cut(entry, "=")
		if !ok {
			continue
		}
		envMap[key] = value
	}
	for key, value := range overrides {
		envMap[key] = value
	}

	keys := make([]string, 0, len(envMap))
	for key := range envMap {
		keys = append(keys, key)
	}
	slices.Sort(keys)

	merged := make([]string, 0, len(keys))
	for _, key := range keys {
		merged = append(merged, key+"="+envMap[key])
	}
	return merged
}

func printProfiles(catalog map[string]profile) {
	order := []string{"race", "gomaxprocs", "config-churn", "soak", "platform", "resource-pressure"}
	for _, name := range order {
		current := catalog[name]
		fmt.Printf("%s: %s\n", current.Name, current.Description)
	}
	fmt.Println("all: 顺序执行上述全部 profile")
}

func profileNames(selected []profile) string {
	names := make([]string, 0, len(selected))
	for _, current := range selected {
		names = append(names, current.Name)
	}
	return strings.Join(names, ",")
}

func anchored(names ...string) string {
	return "^(" + strings.Join(names, "|") + ")$"
}

func appStartupTests() []string {
	return []string{
		"TestAppStartupScenarioBlocksVisibilityUntilInitialScanReleases",
		"TestAppStartupScenarioPublishesControlBeforeManagementVisibility",
		"TestAppStartupScenarioFirstManagementResponseContainsInitialRuntimeStatus",
	}
}

func controlCoreTests() []string {
	return []string{
		"TestServerScenarioRejectsInitialInvalidEffectiveIPWithoutLeakingSessionState",
		"TestServerScenarioRecoversEmptyConfigOnlyAfterAckThenListenerBind",
		"TestServerScenarioRecoversNonListeningTunnelAfterPollingSeesOccupancyGone",
		"TestServerScenarioKeepsRuntimeIssueUntilSamePortFlappingReallyRecovers",
		"TestServerScenarioDeduplicatesMatchingRefreshAndScanRecoveryPush",
		"TestServerScenarioIgnoresStaleScanSnapshotAfterRefreshAdvancesVersion",
		"TestServerScenarioUsesFreshSnapshotWhenRepositoryChangesDuringLoginHandshake",
		"TestServerScenarioKeepsSessionAliveWhenPortBecomesOccupiedRightAfterStartupAck",
		"TestServerScenarioShrinksToEmptyConfigWhenEffectiveIPBecomesInvalidRightAfterStartupAck",
		"TestServerScenarioReconnectAfterEmptyConfigClearsOldRuntimeIssueAndListeners",
		"TestServerScenarioReconnectAfterPendingRecoveryPushDropsOldPendingConfig",
		"TestServerScenarioKeepsEffectiveIPRuntimeIssueAcrossHighFrequencyEmptyAndFullRecoveryFlaps",
		"TestServerScenarioKeepsSessionAliveWhenTCPRecoveryBindFailsWithPermissionDenied",
		"TestServerScenarioClosesPartiallyStartedTCPListenersAcrossRepeatedEMFILERecoveryChurn",
		"TestServerScenarioKeepsSessionAliveWhenUDPRecoveryBindFailsWithPermissionDenied",
		"TestServerScenarioServesHeartbeatWhileEmptyAndFullRecoveryConfigsArePending",
		"TestServerScenarioLateDelayedAckFromClosedSessionDoesNotPolluteReplacementSession",
		"TestServerScenarioRejectsOutOfOrderRecoveryAckWithoutLeavingPendingOrListeners",
		"TestServerScenarioRejectsDuplicateRefreshAckWithoutLeavingListenersOrPending",
		"TestServerScenarioDelayedOldHeartbeatAndErrorFramesDoNotAffectReplacementSession",
		"TestServerUnregisterOldSessionKeepsReplacementSlotAndSession",
		"TestServerScenarioReleasingBlockedStartupBindAfterClientDisconnectLeavesNoListenerLeak",
		"TestServerScenarioReleasingBlockedStartupBindAfterDisableRefreshDoesNotAttachStaleListener",
		"TestServerScenarioReleasingBlockedStartupBindAfterGroupDeletionLeavesNoListenerLeak",
		"TestServerScenarioSkipsOverlappingRuntimeScanRoundDuringRecoveryPush",
		"TestServerScenarioScanReusesFreshNetworkSnapshotBeforeRecoveryPush",
		"TestServerScenarioShutdownDuringInitialRuntimeScanDoesNotOpenControlListenerAfterRelease",
		"TestServerScenarioShutdownWhileScanRecoveryBindBlockedLeavesNoListenerLeak",
		"TestServerScenarioShutdownWhileRefreshBindBlockedDoesNotAttachStaleListener",
	}
}

func stabilityTests() []string {
	return []string{
		"TestServerStabilityScenarioConfigChurnConverges",
		"TestServerStabilityScenarioFakeTimeSoakKeepsResourcesBounded",
		"TestServerStabilityScenarioResourcePressureRefreshAcrossManyTunnelsConverges",
	}
}

func configChurnTests() []string {
	return []string{
		"TestServerScenarioKeepsEffectiveIPRuntimeIssueAcrossHighFrequencyEmptyAndFullRecoveryFlaps",
		"TestServerScenarioClosesPartiallyStartedTCPListenersAcrossRepeatedEMFILERecoveryChurn",
		"TestServerStabilityScenarioConfigChurnConverges",
	}
}

func resourcePressureTests() []string {
	return []string{
		"TestServerScenarioClosesPartiallyStartedTCPListenersAcrossRepeatedEMFILERecoveryChurn",
		"TestServerStabilityScenarioResourcePressureRefreshAcrossManyTunnelsConverges",
	}
}
