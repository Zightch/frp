package listener

import (
	"net"

	"github.com/zightch/frp/frps/internal/testhooks"
)

func CloseStartedTunnelListeners(tcpListeners []net.Listener, udpListeners []UDPListener) {
	for _, listener := range tcpListeners {
		testhooks.Point(
			"control.listener.before_close",
			testhooks.F("protocol", "tcp"),
			testhooks.F("addr", listener.Addr().String()),
		)
		_ = listener.Close()
		testhooks.Point(
			"control.listener.after_close",
			testhooks.F("protocol", "tcp"),
			testhooks.F("addr", listener.Addr().String()),
		)
	}
	for _, listener := range udpListeners {
		testhooks.Point(
			"control.listener.before_close",
			testhooks.F("protocol", "udp"),
			testhooks.F("addr", listener.LocalAddr().String()),
		)
		_ = listener.Close()
		testhooks.Point(
			"control.listener.after_close",
			testhooks.F("protocol", "udp"),
			testhooks.F("addr", listener.LocalAddr().String()),
		)
	}
}
