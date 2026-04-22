package testsupport

import "testing"

func TestCheckSinglePendingConfig(t *testing.T) {
	violations := CheckSinglePendingConfig(InvariantCheckInput{
		After: &ObservedState{
			Server: ServerObservedState{
				Sessions: []SessionObservedState{
					{GroupID: 1, SessionID: 10, Pending: &PendingConfigObservedState{RequestID: 1}},
					{GroupID: 1, SessionID: 11, Pending: &PendingConfigObservedState{RequestID: 2}},
				},
			},
		},
	})
	if len(violations) != 1 {
		t.Fatalf("expected 1 violation, got %d", len(violations))
	}
	if violations[0].Rule != "session.single_pending_config" {
		t.Fatalf("unexpected rule: %s", violations[0].Rule)
	}
}

func TestCheckStaticConflictPriority(t *testing.T) {
	violations := CheckStaticConflictPriority(InvariantCheckInput{
		After: &ObservedState{
			Server: ServerObservedState{
				Tunnels: []TunnelObservedState{
					{
						GroupID:        1,
						TunnelID:       7,
						StaticConflict: true,
						RuntimeIssue:   "bind failed",
						FinalStatus:    "异常",
					},
				},
			},
		},
	})
	if len(violations) != 1 {
		t.Fatalf("expected 1 violation, got %d", len(violations))
	}
	if violations[0].Rule != "tunnel.static_conflict_priority" {
		t.Fatalf("unexpected rule: %s", violations[0].Rule)
	}
}

func TestCheckEmptyConfigRecoveryOrder(t *testing.T) {
	violations := CheckEmptyConfigRecoveryOrder(InvariantCheckInput{
		Before: &ObservedState{
			Server: ServerObservedState{
				Sessions: []SessionObservedState{
					{GroupID: 1, SessionID: 1, RecoveryMode: RecoveryModeEmptyConfig, LastAckedConfigVersion: 3, SnapshotTunnelCount: 0},
				},
			},
		},
		After: &ObservedState{
			Server: ServerObservedState{
				Sessions: []SessionObservedState{
					{GroupID: 1, SessionID: 1, RecoveryMode: RecoveryModeRunning, LastAckedConfigVersion: 3, SnapshotTunnelCount: 0},
				},
			},
		},
	})
	if len(violations) != 1 {
		t.Fatalf("expected 1 violation, got %d", len(violations))
	}
	if violations[0].Rule != "recovery.full_snapshot_before_listener_resume" {
		t.Fatalf("unexpected rule: %s", violations[0].Rule)
	}
}

func TestCheckEmptyConfigRecoveryOrderAllowsReusedConfigVersionWithFullSnapshot(t *testing.T) {
	violations := CheckEmptyConfigRecoveryOrder(InvariantCheckInput{
		Before: &ObservedState{
			Server: ServerObservedState{
				Sessions: []SessionObservedState{
					{GroupID: 1, SessionID: 1, RecoveryMode: RecoveryModeEmptyConfig, LastAckedConfigVersion: 3, SnapshotTunnelCount: 0},
				},
			},
		},
		After: &ObservedState{
			Server: ServerObservedState{
				Sessions: []SessionObservedState{
					{GroupID: 1, SessionID: 1, RecoveryMode: RecoveryModeRunning, LastAckedConfigVersion: 3, SnapshotTunnelCount: 2},
				},
			},
		},
	})
	if len(violations) != 0 {
		t.Fatalf("expected no violations, got %#v", violations)
	}
}
