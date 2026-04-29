package wiring

import (
	"net"

	controlruntime "github.com/zightch/frp/frps/internal/control/runtime"
)

func (s *Server) shutdownSession(session *sessionState) {
	session.CloseDone()
	listeners, udpListeners, streams, _ := session.FreezeTunnelRuntime()
	controlruntime.CloseStartedTunnelListeners(listeners, udpListeners)

	for _, stream := range streams {
		stream.SignalReady(net.ErrClosed)
		stream.Close()
	}
}
