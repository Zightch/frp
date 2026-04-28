package listener

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"

	controlbind "github.com/zightch/frp/frps/internal/control/bind"
	"github.com/zightch/frp/frps/internal/testhooks"
	"github.com/zightch/frp/frps/pkg/protocol"
)

type TLSConfigLoader func(ctx context.Context, tunnelID uint32) (*tls.Config, error)

type Starter struct {
	factory   controlbind.ListenerFactory
	tlsLoader TLSConfigLoader
}

type StarterOptions struct {
	Factory   controlbind.ListenerFactory
	TLSLoader TLSConfigLoader
}

func NewStarter(options StarterOptions) Starter {
	return Starter{
		factory:   options.Factory,
		tlsLoader: options.TLSLoader,
	}
}

func (s Starter) StartTunnelListeners(opCtx TunnelListenerOperationContext) (TunnelListenerBatch, error) {
	if s.factory == nil {
		return TunnelListenerBatch{}, fmt.Errorf("listener factory is nil")
	}

	started := TunnelListenerBatch{}
	switch opCtx.Tunnel.Protocol {
	case protocol.ProtocolTCP:
		tlsConfig, err := s.loadTLSConfig(context.Background(), opCtx.Tunnel.TunnelID)
		if err != nil {
			return TunnelListenerBatch{}, opCtx.ListenerStartError(opCtx.Tunnel.RemoteStart, err)
		}
		started.TCPListeners = make([]net.Listener, 0, opCtx.RemotePortCount())
		started.TCPRuntimes = make([]TCPTunnelListener, 0, opCtx.RemotePortCount())
		for remotePort := int(opCtx.Tunnel.RemoteStart); remotePort <= int(opCtx.Tunnel.RemoteEnd); remotePort++ {
			bind := opCtx.ListenerBind(uint16(remotePort))
			listener, err := s.listenTCP(context.Background(), bind)
			if err != nil {
				CloseStartedTunnelListeners(started.TCPListeners, started.UDPListeners)
				return TunnelListenerBatch{}, opCtx.ListenerStartError(uint16(remotePort), err)
			}
			if tlsConfig != nil {
				listener = tls.NewListener(listener, tlsConfig.Clone())
			}
			started.TCPListeners = append(started.TCPListeners, listener)
			started.TCPRuntimes = append(started.TCPRuntimes, TCPTunnelListener{
				ConfigVersion: opCtx.ConfigVersion,
				Tunnel:        opCtx.Tunnel,
				RemotePort:    uint16(remotePort),
				Listener:      listener,
			})
		}
	case protocol.ProtocolUDP:
		started.UDPListeners = make([]UDPListener, 0, opCtx.RemotePortCount())
		started.UDPRuntimes = make([]UDPTunnelListener, 0, opCtx.RemotePortCount())
		for remotePort := int(opCtx.Tunnel.RemoteStart); remotePort <= int(opCtx.Tunnel.RemoteEnd); remotePort++ {
			bind := opCtx.ListenerBind(uint16(remotePort))
			udpAddr, err := s.resolveUDPAddr(context.Background(), bind)
			if err != nil {
				CloseStartedTunnelListeners(started.TCPListeners, started.UDPListeners)
				return TunnelListenerBatch{}, opCtx.ListenerStartError(uint16(remotePort), err)
			}
			listener, err := s.listenUDP(context.Background(), bind, udpAddr)
			if err != nil {
				CloseStartedTunnelListeners(started.TCPListeners, started.UDPListeners)
				return TunnelListenerBatch{}, opCtx.ListenerStartError(uint16(remotePort), err)
			}
			started.UDPListeners = append(started.UDPListeners, listener)
			started.UDPRuntimes = append(started.UDPRuntimes, UDPTunnelListener{
				ConfigVersion: opCtx.ConfigVersion,
				Tunnel:        opCtx.Tunnel,
				RemotePort:    uint16(remotePort),
				Listener:      listener,
			})
		}
	}
	return started, nil
}

func (s Starter) loadTLSConfig(ctx context.Context, tunnelID uint32) (*tls.Config, error) {
	if s.tlsLoader == nil {
		return nil, nil
	}
	return s.tlsLoader(ctx, tunnelID)
}

func (s Starter) listenTCP(ctx context.Context, bind controlbind.ListenerBind) (net.Listener, error) {
	testhooks.Point("control.listener.before_bind",
		testhooks.F("protocol", bind.Key.Protocol),
		testhooks.F("kind", string(bind.Kind)),
		testhooks.F("group_id", bind.GroupID),
		testhooks.F("tunnel_id", bind.TunnelID),
		testhooks.F("port", bind.Key.Port))
	listener, err := s.factory.ListenTCP(ctx, bind)
	testhooks.Point("control.listener.after_bind",
		testhooks.F("protocol", bind.Key.Protocol),
		testhooks.F("kind", string(bind.Kind)),
		testhooks.F("group_id", bind.GroupID),
		testhooks.F("tunnel_id", bind.TunnelID),
		testhooks.F("port", bind.Key.Port),
		testhooks.F("ok", err == nil))
	return listener, err
}

func (s Starter) resolveUDPAddr(ctx context.Context, bind controlbind.ListenerBind) (*net.UDPAddr, error) {
	testhooks.Point("control.listener.before_resolve_udp",
		testhooks.F("kind", string(bind.Kind)),
		testhooks.F("group_id", bind.GroupID),
		testhooks.F("tunnel_id", bind.TunnelID),
		testhooks.F("port", bind.Key.Port),
	)
	addr, err := s.factory.ResolveUDP(ctx, bind)
	testhooks.Point("control.listener.after_resolve_udp",
		testhooks.F("kind", string(bind.Kind)),
		testhooks.F("group_id", bind.GroupID),
		testhooks.F("tunnel_id", bind.TunnelID),
		testhooks.F("port", bind.Key.Port),
		testhooks.F("ok", err == nil),
	)
	return addr, err
}

func (s Starter) listenUDP(ctx context.Context, bind controlbind.ListenerBind, addr *net.UDPAddr) (UDPListener, error) {
	testhooks.Point("control.listener.before_bind",
		testhooks.F("protocol", bind.Key.Protocol),
		testhooks.F("kind", string(bind.Kind)),
		testhooks.F("group_id", bind.GroupID),
		testhooks.F("tunnel_id", bind.TunnelID),
		testhooks.F("port", bind.Key.Port))
	listener, err := s.factory.ListenUDP(ctx, bind, addr)
	testhooks.Point("control.listener.after_bind",
		testhooks.F("protocol", bind.Key.Protocol),
		testhooks.F("kind", string(bind.Kind)),
		testhooks.F("group_id", bind.GroupID),
		testhooks.F("tunnel_id", bind.TunnelID),
		testhooks.F("port", bind.Key.Port),
		testhooks.F("ok", err == nil))
	return listener, err
}
