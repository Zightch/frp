package bind

import "github.com/zightch/frp/frps/internal/control/session"

type ClaimOwner struct {
	GroupID   int64
	SessionID uint64
	Epoch     uint64
	TunnelID  uint32
}

type Conflict struct {
	Key   session.BindingKey
	Owner ClaimOwner
}

func ExpandBindings(effectiveIP string, tunnels []session.DesiredTunnelRuntime) []session.BindingKey {
	keys := make([]session.BindingKey, 0, len(tunnels))
	for _, tunnel := range tunnels {
		if !tunnel.Enabled {
			continue
		}
		for port := tunnel.RemoteStart; port <= tunnel.RemoteEnd; port++ {
			keys = append(keys, session.BindingKey{
				Protocol:    tunnel.Protocol,
				EffectiveIP: effectiveIP,
				Port:        port,
			})
			if port == tunnel.RemoteEnd {
				break
			}
		}
	}
	return keys
}
