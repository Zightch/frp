package client

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net"

	"github.com/zightch/frp/frps/pkg/protocol"
)

func dialTunnelBackend(tunnel protocol.TunnelEntry, target string) (net.Conn, error) {
	if tunnel.BackendTLSMode == protocol.TunnelTLSModeOff {
		return net.DialTimeout("tcp", target, defaultDialTimeout)
	}

	host, _, err := net.SplitHostPort(target)
	if err != nil {
		return nil, err
	}
	serverName := tunnel.BackendTLSServerName
	if serverName == "" {
		serverName = host
	}

	rootPool, err := buildBackendRootPool(tunnel)
	if err != nil {
		return nil, err
	}

	config := &tls.Config{
		MinVersion:         tls.VersionTLS12,
		ServerName:         serverName,
		InsecureSkipVerify: tunnel.BackendTLSInsecureSkipVerify,
		RootCAs:            rootPool,
	}

	if tunnel.BackendTLSMode == protocol.TunnelTLSModeMTLS {
		cert, err := tls.X509KeyPair([]byte(tunnel.BackendTLSClientCertPEM), []byte(tunnel.BackendTLSClientKeyPEM))
		if err != nil {
			return nil, fmt.Errorf("load backend client certificate: %w", err)
		}
		config.Certificates = []tls.Certificate{cert}
	}

	dialer := &net.Dialer{Timeout: defaultDialTimeout}
	return tls.DialWithDialer(dialer, "tcp", target, config)
}

func buildBackendRootPool(tunnel protocol.TunnelEntry) (*x509.CertPool, error) {
	var pool *x509.CertPool
	if tunnel.BackendTLSLoadSystemCA {
		systemPool, err := x509.SystemCertPool()
		if err != nil {
			return nil, fmt.Errorf("load system ca pool: %w", err)
		}
		if systemPool != nil {
			pool = systemPool
		}
	}
	if pool == nil {
		pool = x509.NewCertPool()
	}
	if tunnel.BackendTLSCAPEM != "" && !pool.AppendCertsFromPEM([]byte(tunnel.BackendTLSCAPEM)) {
		return nil, fmt.Errorf("append backend ca certificates")
	}
	return pool, nil
}
