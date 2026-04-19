package control

import (
	"errors"
	"net"
	"strconv"

	"github.com/zightch/frp/frps/pkg/protocol"
)

type tcpTunnelListener struct {
	tunnel     protocol.TunnelEntry
	remotePort uint16
	listener   net.Listener
}

type udpTunnelListener struct {
	tunnel     protocol.TunnelEntry
	remotePort uint16
	listener   *net.UDPConn
}

func (s *Server) ensureTunnelListeners(conn net.Conn, logger Logger, session *sessionState) error {
	session.runtimeMu.Lock()
	if session.listenersStarted {
		session.runtimeMu.Unlock()
		return nil
	}
	session.runtimeMu.Unlock()

	startedTCP := make([]net.Listener, 0)
	startedTCPByTunnel := make(map[uint32][]net.Listener)
	startedTCPRuntimes := make([]tcpTunnelListener, 0)
	startedUDP := make([]*net.UDPConn, 0)
	startedUDPByTunnel := make(map[uint32][]*net.UDPConn)
	startedUDPRuntimes := make([]udpTunnelListener, 0)

	for _, tunnel := range session.Snapshot.Tunnels {
		if tunnel.TunnelFlags&protocol.TunnelFlagEnabled == 0 {
			continue
		}
		switch tunnel.Protocol {
		case protocol.ProtocolTCP:
			for remotePort := int(tunnel.RemoteStart); remotePort <= int(tunnel.RemoteEnd); remotePort++ {
				addr := net.JoinHostPort("", strconv.Itoa(remotePort))
				listener, err := net.Listen("tcp", addr)
				if err != nil {
					closeStartedTunnelListeners(startedTCP, startedUDP)
					return err
				}
				startedTCP = append(startedTCP, listener)
				startedTCPByTunnel[tunnel.TunnelID] = append(startedTCPByTunnel[tunnel.TunnelID], listener)
				startedTCPRuntimes = append(startedTCPRuntimes, tcpTunnelListener{
					tunnel:     tunnel,
					remotePort: uint16(remotePort),
					listener:   listener,
				})
			}
		case protocol.ProtocolUDP:
			for remotePort := int(tunnel.RemoteStart); remotePort <= int(tunnel.RemoteEnd); remotePort++ {
				addr := net.JoinHostPort("", strconv.Itoa(remotePort))
				udpAddr, err := net.ResolveUDPAddr("udp", addr)
				if err != nil {
					closeStartedTunnelListeners(startedTCP, startedUDP)
					return err
				}
				listener, err := net.ListenUDP("udp", udpAddr)
				if err != nil {
					closeStartedTunnelListeners(startedTCP, startedUDP)
					return err
				}
				startedUDP = append(startedUDP, listener)
				startedUDPByTunnel[tunnel.TunnelID] = append(startedUDPByTunnel[tunnel.TunnelID], listener)
				startedUDPRuntimes = append(startedUDPRuntimes, udpTunnelListener{
					tunnel:     tunnel,
					remotePort: uint16(remotePort),
					listener:   listener,
				})
			}
		default:
			continue
		}
	}

	session.runtimeMu.Lock()
	if session.listenersStarted {
		session.runtimeMu.Unlock()
		closeStartedTunnelListeners(startedTCP, startedUDP)
		return nil
	}
	for tunnelID, listeners := range startedTCPByTunnel {
		session.listeners[tunnelID] = append(session.listeners[tunnelID], listeners...)
	}
	for tunnelID, listeners := range startedUDPByTunnel {
		session.udpListeners[tunnelID] = append(session.udpListeners[tunnelID], listeners...)
	}
	session.listenersStarted = true
	session.runtimeMu.Unlock()

	if len(startedUDPRuntimes) > 0 {
		go s.serveUDPIdleCleanup(conn, logger, session)
	}

	for _, runtime := range startedTCPRuntimes {
		logger.Info(
			"tcp tunnel listener ready",
			"tunnel_id", runtime.tunnel.TunnelID,
			"remote_port", runtime.remotePort,
			"addr", runtime.listener.Addr().String(),
		)
		go s.serveTunnelListener(conn, logger, session, runtime.tunnel, runtime.remotePort, runtime.listener)
	}
	for _, runtime := range startedUDPRuntimes {
		logger.Info(
			"udp tunnel listener ready",
			"tunnel_id", runtime.tunnel.TunnelID,
			"remote_port", runtime.remotePort,
			"addr", runtime.listener.LocalAddr().String(),
		)
		go s.serveUDPTunnelListener(conn, logger, session, runtime.tunnel, runtime.remotePort, runtime.listener)
	}

	return nil
}

func closeStartedTunnelListeners(tcpListeners []net.Listener, udpListeners []*net.UDPConn) {
	for _, listener := range tcpListeners {
		_ = listener.Close()
	}
	for _, listener := range udpListeners {
		_ = listener.Close()
	}
}

func (s *Server) serveTunnelListener(conn net.Conn, logger Logger, session *sessionState, tunnel protocol.TunnelEntry, remotePort uint16, listener net.Listener) {
	for {
		publicConn, err := listener.Accept()
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return
			}
			logger.Warn("tcp tunnel accept failed", "tunnel_id", tunnel.TunnelID, "error", err)
			continue
		}

		go s.handlePublicConnection(conn, logger, session, tunnel, remotePort, publicConn)
	}
}

func (s *Server) serveUDPTunnelListener(conn net.Conn, logger Logger, session *sessionState, tunnel protocol.TunnelEntry, remotePort uint16, listener *net.UDPConn) {
	buffer := make([]byte, protocol.MaxDataBodyLen)
	for {
		n, clientAddr, err := listener.ReadFromUDP(buffer)
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return
			}
			logger.Warn("udp tunnel read failed", "tunnel_id", tunnel.TunnelID, "error", err)
			continue
		}
		payload := append([]byte(nil), buffer[:n]...)
		if err := s.handlePublicUDPDatagram(conn, logger, session, tunnel, remotePort, listener, clientAddr, payload); err != nil {
			logger.Warn(
				"udp tunnel forward failed",
				"tunnel_id", tunnel.TunnelID,
				"remote_port", remotePort,
				"client_addr", clientAddr.String(),
				"error", err,
			)
		}
	}
}
