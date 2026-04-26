package control

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"

	"github.com/zightch/frp/frps/internal/settings/entrycerts"
	"github.com/zightch/frp/frps/pkg/protocol"
)

func (s *Server) loadTunnelListenerTLSConfig(ctx context.Context, tunnelID uint32) (*tls.Config, error) {
	if s == nil {
		return nil, fmt.Errorf("control server is unavailable")
	}
	if tunnelID == 0 {
		return nil, fmt.Errorf("tunnel id must be non-zero")
	}
	if s.options.Store == nil {
		return nil, fmt.Errorf("control store is unavailable")
	}

	row, err := s.options.Store.QueryOneContext(
		ctx,
		`
SELECT
	listen_tls_mode,
	listen_tls_load_system_ca
FROM tunnels
WHERE id = ?
`,
		int64(tunnelID),
	)
	if err != nil {
		return nil, fmt.Errorf("load tunnel listen tls config: %w", err)
	}

	mode, err := decodeTunnelTLSMode(rowString(row, "listen_tls_mode"))
	if err != nil {
		return nil, err
	}
	if mode == protocol.TunnelTLSModeOff {
		return nil, nil
	}
	loadSystemCA, err := rowBool(row, "listen_tls_load_system_ca")
	if err != nil {
		return nil, fmt.Errorf("decode listen_tls_load_system_ca: %w", err)
	}

	service := entrycerts.NewService(s.options.Store, entrycerts.ServiceOptions{})
	binding, ok, err := service.ResolveTargetCertificate(ctx, entrycerts.TargetTypeTunnel, int64(tunnelID), entrycerts.UsageTypeTunnelListenServerCert)
	if err != nil {
		return nil, fmt.Errorf("resolve tunnel listen server certificate: %w", err)
	}
	if !ok {
		return nil, fmt.Errorf("tunnel listen tls requires a bound server certificate")
	}

	config := &tls.Config{
		MinVersion:   tls.VersionTLS12,
		Certificates: []tls.Certificate{binding.TLSCertificate},
		ClientAuth:   tls.NoClientCert,
	}
	if mode != protocol.TunnelTLSModeMTLS {
		return config, nil
	}

	pool, err := buildTunnelClientCAPool(ctx, service, int64(tunnelID), loadSystemCA)
	if err != nil {
		return nil, err
	}
	config.ClientAuth = tls.RequireAndVerifyClientCert
	config.ClientCAs = pool
	return config, nil
}

func buildTunnelClientCAPool(ctx context.Context, service *entrycerts.Service, tunnelID int64, loadSystemCA bool) (*x509.CertPool, error) {
	if service == nil {
		return nil, fmt.Errorf("entry certificate service is unavailable")
	}

	var pool *x509.CertPool
	if loadSystemCA {
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

	customPool, err := service.ResolveTargetCAPool(ctx, entrycerts.TargetTypeTunnel, tunnelID, entrycerts.UsageTypeTunnelListenClientCA)
	if err != nil {
		return nil, fmt.Errorf("resolve tunnel listen client ca pool: %w", err)
	}
	if !loadSystemCA && customPool.PEM == "" {
		return nil, fmt.Errorf("tunnel listen mtls requires system ca or at least one client ca asset")
	}
	if customPool.PEM != "" && !pool.AppendCertsFromPEM([]byte(customPool.PEM)) {
		return nil, fmt.Errorf("append tunnel listen client ca certificates")
	}
	return pool, nil
}
