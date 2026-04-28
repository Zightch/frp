package handshake

import (
	"crypto/tls"
	"fmt"
	"net"
	"sync"
	"time"
)

type Clock interface {
	Now() time.Time
}

type TLSCertificateProvider interface {
	CurrentCertificate() (*tls.Certificate, bool)
}

type ControlTLSStore struct {
	mu          sync.RWMutex
	certificate *tls.Certificate
}

func NewControlTLSStore() *ControlTLSStore {
	return &ControlTLSStore{}
}

func (s *ControlTLSStore) Set(certificate tls.Certificate) {
	if s == nil {
		return
	}
	certCopy := certificate
	s.mu.Lock()
	s.certificate = &certCopy
	s.mu.Unlock()
}

func (s *ControlTLSStore) Clear() {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.certificate = nil
	s.mu.Unlock()
}

func (s *ControlTLSStore) CurrentCertificate() (*tls.Certificate, bool) {
	if s == nil {
		return nil, false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.certificate == nil {
		return nil, false
	}
	certCopy := *s.certificate
	return &certCopy, true
}

func UpgradeControlConnToTLS(conn net.Conn, clock Clock, readTimeout time.Duration, certificates TLSCertificateProvider) (net.Conn, error) {
	if conn == nil {
		return nil, fmt.Errorf("control connection is nil")
	}
	if certificates == nil {
		return nil, fmt.Errorf("control tls certificate is unavailable")
	}
	cert, ok := certificates.CurrentCertificate()
	if !ok {
		return nil, fmt.Errorf("control tls certificate is unavailable")
	}

	tlsConn := tls.Server(conn, &tls.Config{
		MinVersion:   tls.VersionTLS12,
		Certificates: []tls.Certificate{*cert},
	})
	now := time.Now().UTC()
	if clock != nil {
		now = clock.Now()
	}
	if err := conn.SetDeadline(now.Add(readTimeout)); err != nil {
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
