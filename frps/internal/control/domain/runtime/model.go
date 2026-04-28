package runtime

import (
	"github.com/zightch/frp/frps/internal/proxygroups"
	"github.com/zightch/frp/frps/pkg/protocol"
)

type GroupRuntime struct {
	ID                       int64
	Name                     string
	Enabled                  bool
	EffectiveIP              string
	ControlTransportSecurity proxygroups.ControlTransportSecurity
	ClientSecretHash         [32]byte
	Snapshot                 ConfigSnapshot
}

type ConfigSnapshot struct {
	Version       uint64
	GeneratedAtMs uint64
	Tunnels       []protocol.TunnelEntry
}
