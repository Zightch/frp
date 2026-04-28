package control

import (
	"crypto/tls"
	"fmt"
	"net"

	controlhandshake "github.com/zightch/frp/frps/internal/control/protocol/handshake"
	"github.com/zightch/frp/frps/internal/settings/entrycerts"
)

func (s *Server) negotiateTransport(conn net.Conn) (net.Conn, [16]byte, error) {
	return controlhandshake.NegotiateTransport(controlhandshake.NegotiateOptions{
		Conn:         conn,
		Reader:       controlhandshake.FrameReaderFunc(s.readFrame),
		Writer:       s.frameWriter(conn, nil),
		Repository:   s.repo,
		ReadTimeout:  s.options.ReadTimeout,
		Clock:        s.clock,
		Certificates: s.controlTLS,
	})
}

func (s *Server) selectTransportSecurityMode(group GroupRuntime, supportedModes uint8) (uint8, error) {
	return controlhandshake.SelectTransportSecurityMode(group, supportedModes, s.controlTLS)
}

func (s *Server) ConfigureControlTLS(binding *entrycerts.ResolvedBinding) error {
	if s == nil {
		return fmt.Errorf("control server is unavailable")
	}
	if binding == nil {
		return fmt.Errorf("control tls binding is nil")
	}

	s.ensureControlTLSStore().Set(binding.TLSCertificate)
	return nil
}

func (s *Server) ClearControlTLS() error {
	if s == nil {
		return fmt.Errorf("control server is unavailable")
	}
	if s.controlTLS != nil {
		s.controlTLS.Clear()
	}
	return nil
}

func (s *Server) currentControlTLSCertificate() (*tls.Certificate, bool) {
	if s == nil {
		return nil, false
	}
	if s.controlTLS == nil {
		return nil, false
	}
	return s.controlTLS.CurrentCertificate()
}

func (s *Server) upgradeControlConnToTLS(conn net.Conn) (net.Conn, error) {
	return controlhandshake.UpgradeControlConnToTLS(conn, s.clock, s.options.ReadTimeout, s.controlTLS)
}

func (s *Server) ensureControlTLSStore() *controlhandshake.ControlTLSStore {
	if s.controlTLS == nil {
		s.controlTLS = controlhandshake.NewControlTLSStore()
	}
	return s.controlTLS
}
