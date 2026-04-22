package client

import (
	"fmt"

	"github.com/zightch/frp/frps/pkg/protocol"
)

func validateConfigPush(push protocol.ConfigPush) error {
	if push.ConfigVersion == 0 {
		return fmt.Errorf("config.push configVersion must be non-zero")
	}

	seenTunnelIDs := make(map[uint32]struct{}, len(push.Tunnels))
	for index, tunnel := range push.Tunnels {
		if err := validateConfigPushTunnel(tunnel); err != nil {
			return fmt.Errorf("config.push tunnel[%d]: %w", index, err)
		}
		if _, exists := seenTunnelIDs[tunnel.TunnelID]; exists {
			return fmt.Errorf("config.push contains duplicate tunnelId %d", tunnel.TunnelID)
		}
		seenTunnelIDs[tunnel.TunnelID] = struct{}{}
	}

	return nil
}

func validateConfigPushTunnel(tunnel protocol.TunnelEntry) error {
	if tunnel.TunnelID == 0 {
		return fmt.Errorf("tunnel id must be non-zero")
	}

	switch tunnel.Protocol {
	case protocol.ProtocolTCP, protocol.ProtocolUDP:
	default:
		return fmt.Errorf("unsupported tunnel protocol %d", tunnel.Protocol)
	}

	if tunnel.TunnelFlags&^(protocol.TunnelFlagEnabled|protocol.TunnelFlagRange) != 0 {
		return fmt.Errorf("unsupported tunnel flags %d", tunnel.TunnelFlags)
	}

	if tunnel.RemoteStart == 0 || tunnel.RemoteEnd == 0 || tunnel.LocalStart == 0 || tunnel.LocalEnd == 0 {
		return fmt.Errorf("tunnel ports must be between 1 and 65535")
	}
	if tunnel.RemoteEnd < tunnel.RemoteStart || tunnel.LocalEnd < tunnel.LocalStart {
		return fmt.Errorf("tunnel range end must be greater than or equal to start")
	}

	if tunnel.TunnelFlags&protocol.TunnelFlagRange == 0 {
		if tunnel.RemoteStart != tunnel.RemoteEnd || tunnel.LocalStart != tunnel.LocalEnd {
			return fmt.Errorf("single tunnel must use identical start and end ports")
		}
	} else if (tunnel.RemoteEnd - tunnel.RemoteStart) != (tunnel.LocalEnd - tunnel.LocalStart) {
		return fmt.Errorf("remote and local port ranges must be aligned")
	}

	if tunnel.LocalHost.String() == "" {
		return fmt.Errorf("local_host is invalid")
	}

	return nil
}
