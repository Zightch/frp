package tlsconfig

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"strconv"
	"strings"

	"github.com/zightch/frp/frps/internal/settings/entrycerts"
	"github.com/zightch/frp/frps/internal/storage"
	"github.com/zightch/frp/frps/pkg/protocol"
)

func LoadTunnelListenerTLSConfig(ctx context.Context, store *storage.SQL, tunnelID uint32) (*tls.Config, error) {
	if tunnelID == 0 {
		return nil, fmt.Errorf("tunnel id must be non-zero")
	}
	if store == nil {
		return nil, nil
	}

	row, err := store.QueryOneContext(
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

	service := entrycerts.NewService(store, entrycerts.ServiceOptions{})
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

func decodeTunnelTLSMode(value string) (uint8, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", "off":
		return protocol.TunnelTLSModeOff, nil
	case "tls":
		return protocol.TunnelTLSModeTLS, nil
	case "mtls":
		return protocol.TunnelTLSModeMTLS, nil
	default:
		return 0, fmt.Errorf("unsupported tunnel tls mode %q", value)
	}
}

func rowString(row storage.Row, key string) string {
	value, ok := row[key]
	if !ok || value == nil {
		return ""
	}
	switch typed := value.(type) {
	case string:
		return typed
	case []byte:
		return string(typed)
	default:
		return fmt.Sprint(typed)
	}
}

func rowBool(row storage.Row, key string) (bool, error) {
	value, ok := row[key]
	if !ok || value == nil {
		return false, nil
	}
	switch typed := value.(type) {
	case bool:
		return typed, nil
	case int:
		return typed != 0, nil
	case int64:
		return typed != 0, nil
	case uint64:
		return typed != 0, nil
	case string:
		return parseBoolString(typed)
	case []byte:
		return parseBoolString(string(typed))
	default:
		return false, fmt.Errorf("unsupported bool value %T", value)
	}
}

func parseBoolString(value string) (bool, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", "0", "false", "no":
		return false, nil
	case "1", "true", "yes":
		return true, nil
	default:
		parsed, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64)
		return parsed != 0, err
	}
}
