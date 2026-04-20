package client

import (
	"fmt"
	"net"
	"strconv"

	"github.com/zightch/frp/frps/pkg/protocol"
)

func (s *sessionState) localTarget(open protocol.StreamOpen) (string, error) {
	return s.localTunnelTarget(open.TunnelID, open.RemotePort, protocol.ProtocolTCP)
}

func (s *sessionState) localUDPTarget(open protocol.UDPOpen) (string, error) {
	return s.localTunnelTarget(open.TunnelID, open.RemotePort, protocol.ProtocolUDP)
}

func (s *sessionState) localTunnelTarget(tunnelID uint32, remotePort uint16, wantProtocol uint8) (string, error) {
	tunnel, ok := s.tunnelByID(tunnelID)
	if !ok {
		return "", fmt.Errorf("tunnel %d not found", tunnelID)
	}
	if tunnel.Protocol != wantProtocol {
		return "", fmt.Errorf("tunnel %d is not %s", tunnelID, protocolName(wantProtocol))
	}
	if tunnel.TunnelFlags&protocol.TunnelFlagEnabled == 0 {
		return "", fmt.Errorf("tunnel %d is disabled", tunnelID)
	}

	localPort, err := localPortForRemote(tunnel, remotePort)
	if err != nil {
		return "", err
	}

	host := tunnel.LocalHost.String()
	if host == "" {
		return "", fmt.Errorf("tunnel %d has empty local host", tunnelID)
	}

	return net.JoinHostPort(host, strconv.Itoa(int(localPort))), nil
}

func localPortForRemote(tunnel protocol.TunnelEntry, remotePort uint16) (uint16, error) {
	if remotePort < tunnel.RemoteStart || remotePort > tunnel.RemoteEnd {
		return 0, fmt.Errorf("remote port %d is outside tunnel %d", remotePort, tunnel.TunnelID)
	}

	offset := int(remotePort - tunnel.RemoteStart)
	localPort := int(tunnel.LocalStart) + offset
	if localPort > int(tunnel.LocalEnd) {
		return 0, fmt.Errorf("local port mapping is out of range for tunnel %d", tunnel.TunnelID)
	}

	return uint16(localPort), nil
}

func (s *sessionState) tunnelByID(tunnelID uint32) (protocol.TunnelEntry, bool) {
	s.snapshotMu.RLock()
	defer s.snapshotMu.RUnlock()

	for _, tunnel := range s.snapshot.Tunnels {
		if tunnel.TunnelID == tunnelID {
			return tunnel, true
		}
	}
	return protocol.TunnelEntry{}, false
}

func protocolName(value uint8) string {
	switch value {
	case protocol.ProtocolTCP:
		return "tcp"
	case protocol.ProtocolUDP:
		return "udp"
	default:
		return fmt.Sprintf("protocol(%d)", value)
	}
}
