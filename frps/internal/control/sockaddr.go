package control

import (
	"net"
	"strconv"

	"github.com/zightch/frp/frps/pkg/protocol"
)

func sockAddrFromNetAddr(addr net.Addr) protocol.SockAddr {
	if addr == nil {
		return protocol.SockAddr{}
	}
	switch typed := addr.(type) {
	case *net.TCPAddr:
		return protocol.SockAddr{IP: append(net.IP(nil), typed.IP...), Port: uint16(typed.Port)}
	case *net.UDPAddr:
		return protocol.SockAddr{IP: append(net.IP(nil), typed.IP...), Port: uint16(typed.Port)}
	default:
		host, portText, err := net.SplitHostPort(addr.String())
		if err != nil {
			return protocol.SockAddr{}
		}
		port, err := strconv.Atoi(portText)
		if err != nil {
			return protocol.SockAddr{}
		}
		return protocol.SockAddr{IP: net.ParseIP(host), Port: uint16(port)}
	}
}

func sockAddrString(addr protocol.SockAddr) string {
	if len(addr.IP) == 0 && addr.Port == 0 {
		return ""
	}
	ip := addr.IP
	if ip4 := ip.To4(); ip4 != nil {
		ip = ip4
	} else if ip16 := ip.To16(); ip16 != nil {
		ip = ip16
	}
	return net.JoinHostPort(ip.String(), strconv.Itoa(int(addr.Port)))
}
