package main

import (
	"reflect"
	"strings"
	"testing"
)

func TestResolveProfilesExpandsAllInStableOrder(t *testing.T) {
	selected, err := resolveProfiles("all", profileCatalog())
	if err != nil {
		t.Fatalf("resolve profiles: %v", err)
	}

	var names []string
	for _, current := range selected {
		names = append(names, current.Name)
	}

	want := []string{"race", "gomaxprocs", "config-churn", "soak", "platform", "resource-pressure"}
	if !reflect.DeepEqual(names, want) {
		t.Fatalf("unexpected profile order: got %#v want %#v", names, want)
	}
}

func TestBuildGoTestArgsIncludesFlagsInExpectedOrder(t *testing.T) {
	args := buildGoTestArgs(goTestRun{
		Package: "./internal/control",
		Run:     anchored("TestOne", "TestTwo"),
		Timeout: "90s",
		Count:   5,
		CPU:     "1,2,4",
		Race:    true,
		Tags:    []string{"testhooks"},
	})

	want := []string{
		"test",
		"-parallel=1",
		"-count=5",
		"-race",
		"-tags", "testhooks",
		"-cpu", "1,2,4",
		"-run", "^(TestOne|TestTwo)$",
		"-timeout", "90s",
		"./internal/control",
	}
	if !reflect.DeepEqual(args, want) {
		t.Fatalf("unexpected go test args: got %#v want %#v", args, want)
	}
}

func TestFormatCommandSortsEnvironmentAssignments(t *testing.T) {
	command := formatCommand("go", []string{"test", "./internal/control"}, map[string]string{
		"FRPS_STABILITY_SOAK_CYCLES":    "64",
		"FRPS_STABILITY_CHURN_VERSIONS": "24",
	})

	wantPrefix := "FRPS_STABILITY_CHURN_VERSIONS=24 FRPS_STABILITY_SOAK_CYCLES=64 go test ./internal/control"
	if !strings.HasPrefix(command, wantPrefix) {
		t.Fatalf("unexpected formatted command: %q", command)
	}
}
