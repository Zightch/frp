package control

import (
	"crypto/tls"
	"fmt"
	"net"
	"time"

	"github.com/zightch/frp/frps/internal/certusages"
)

func (s *Server) ConfigureControlTLS(binding *certusages.ResolvedBinding) error {
	if s == nil {
		return fmt.Errorf("control server is unavailable")
	}
	if binding == nil {
		return fmt.Errorf("control tls binding is nil")
	}

	certCopy := binding.TLSCertificate
	s.controlTLSMu.Lock()
	s.controlTLSCertificate = &certCopy
	s.controlTLSMu.Unlock()
	return nil
}

func (s *Server) ClearControlTLS() error {
	if s == nil {
		return fmt.Errorf("control server is unavailable")
	}
	s.controlTLSMu.Lock()
	s.controlTLSCertificate = nil
	s.controlTLSMu.Unlock()
	return nil
}

func (s *Server) currentControlTLSCertificate() (*tls.Certificate, bool) {
	if s == nil {
		return nil, false
	}
	s.controlTLSMu.RLock()
	defer s.controlTLSMu.RUnlock()
	if s.controlTLSCertificate == nil {
		return nil, false
	}
	return s.controlTLSCertificate, true
}

func (s *Server) upgradeControlConnToTLS(conn net.Conn) (net.Conn, error) {
	if conn == nil {
		return nil, fmt.Errorf("control connection is nil")
	}
	cert, ok := s.currentControlTLSCertificate()
	if !ok {
		return nil, fmt.Errorf("control tls certificate is unavailable")
	}

	tlsConn := tls.Server(conn, &tls.Config{
		MinVersion:   tls.VersionTLS12,
		Certificates: []tls.Certificate{*cert},
	})
	if err := conn.SetDeadline(s.clock.Now().Add(s.options.ReadTimeout)); err != nil {
		return nil, err
	}
	if err := tlsConn.Handshake(); err != nil {
		_ = conn.SetDeadline(time.Time{})
		return nil, err
	}
	if err := conn.SetDeadline(time.Time{}); err != nil {
		return nil, err
	}
	return tlsConn, nil
}
