package runtime

import (
	"testing"

	"github.com/zightch/frp/frps/pkg/protocol"
)

func TestSamePushedConfigSnapshotDetectsRatePolicyChanges(t *testing.T) {
	base := ConfigSnapshot{
		Version:       7,
		GeneratedAtMs: 11,
		Tunnels: []protocol.TunnelEntry{{
			TunnelName:  "tcp-a",
			TunnelID:    3,
			Protocol:    protocol.ProtocolTCP,
			TunnelFlags: protocol.TunnelFlagEnabled,
			RemoteStart: 7000,
			RemoteEnd:   7000,
			LocalStart:  80,
			LocalEnd:    80,
			RatePolicy: protocol.TunnelRatePolicy{
				PolicyID:    9,
				Mode:        protocol.RatePolicyModeShared,
				DownlinkBPS: 10_000_000,
				UplinkBPS:   5_000_000,
			},
		}},
	}

	same := base
	same.Tunnels = append([]protocol.TunnelEntry(nil), base.Tunnels...)
	if !SamePushedConfigSnapshot(base, same) {
		t.Fatal("expected identical rate policy snapshot to match")
	}

	changed := base
	changed.Tunnels = append([]protocol.TunnelEntry(nil), base.Tunnels...)
	changed.Tunnels[0].RatePolicy.DownlinkBPS = 20_000_000
	if SamePushedConfigSnapshot(base, changed) {
		t.Fatal("expected rate policy change to invalidate pushed snapshot equality")
	}
}

func TestSamePushedConfigSnapshotDetectsTunnelNameChanges(t *testing.T) {
	base := ConfigSnapshot{
		Version:       7,
		GeneratedAtMs: 11,
		Tunnels: []protocol.TunnelEntry{{
			TunnelName: "tcp-a",
			TunnelID:   3,
			Protocol:   protocol.ProtocolTCP,
		}},
	}

	changed := base
	changed.Tunnels = append([]protocol.TunnelEntry(nil), base.Tunnels...)
	changed.Tunnels[0].TunnelName = "tcp-b"
	if SamePushedConfigSnapshot(base, changed) {
		t.Fatal("expected tunnel name change to invalidate pushed snapshot equality")
	}
}
