package groupconfig

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/zightch/frp/frps/internal/proxygroups"
)

type tunnelTLSMode string

const (
	tunnelTLSModeOff  tunnelTLSMode = "off"
	tunnelTLSModeTLS  tunnelTLSMode = "tls"
	tunnelTLSModeMTLS tunnelTLSMode = "mtls"
)

func normalizeTunnelTLSMode(value string) tunnelTLSMode {
	switch tunnelTLSMode(strings.ToLower(strings.TrimSpace(value))) {
	case tunnelTLSModeOff:
		return tunnelTLSModeOff
	case tunnelTLSModeTLS:
		return tunnelTLSModeTLS
	case tunnelTLSModeMTLS:
		return tunnelTLSModeMTLS
	default:
		return ""
	}
}

func normalizeOptionalAssetID(value *int64, field string) (*int64, error) {
	if value == nil {
		return nil, nil
	}
	if *value <= 0 {
		return nil, &Error{Status: 400, Message: field + " must be greater than zero"}
	}
	normalized := *value
	return &normalized, nil
}

func normalizeAssetIDs(values []int64, field string) ([]int64, error) {
	if len(values) == 0 {
		return nil, nil
	}
	seen := make(map[int64]struct{}, len(values))
	items := make([]int64, 0, len(values))
	for _, value := range values {
		if value <= 0 {
			return nil, &Error{Status: 400, Message: field + " must contain only positive asset ids"}
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		items = append(items, value)
	}
	return items, nil
}

func (s *Service) validateTunnelTLS(ctx context.Context, tunnel normalizedTunnel) error {
	if tunnel.Protocol != "tcp" {
		if tunnel.ListenTLSMode != tunnelTLSModeOff {
			return &Error{Status: 400, Message: "listen tls is only supported for tcp tunnels"}
		}
		if tunnel.BackendTLSMode != tunnelTLSModeOff {
			return &Error{Status: 400, Message: "backend tls is only supported for tcp tunnels"}
		}
		return nil
	}

	if tunnel.ListenTLSMode == "" {
		return &Error{Status: 400, Message: "listen_tls_mode must be off, tls, or mtls"}
	}
	if tunnel.BackendTLSMode == "" {
		return &Error{Status: 400, Message: "backend_tls_mode must be off, tls, or mtls"}
	}

	if tunnel.ListenTLSServerCertAssetID != nil {
		if err := s.validateTunnelCertificateAsset(ctx, *tunnel.ListenTLSServerCertAssetID, "listen_tls_server_cert_asset_id"); err != nil {
			return err
		}
	}
	if tunnel.BackendTLSClientCertAssetID != nil {
		if err := s.validateTunnelCertificateAsset(ctx, *tunnel.BackendTLSClientCertAssetID, "backend_tls_client_cert_asset_id"); err != nil {
			return err
		}
	}
	if len(tunnel.ListenTLSClientCAAssetIDs) > 0 {
		if err := s.validateTunnelCAAssets(ctx, tunnel.ListenTLSClientCAAssetIDs, "listen_tls_client_ca_asset_ids"); err != nil {
			return err
		}
	}
	if len(tunnel.BackendTLSCAAssetIDs) > 0 {
		if err := s.validateTunnelCAAssets(ctx, tunnel.BackendTLSCAAssetIDs, "backend_tls_ca_asset_ids"); err != nil {
			return err
		}
	}

	if tunnel.ListenTLSMode != tunnelTLSModeOff && tunnel.ListenTLSServerCertAssetID == nil {
		return &Error{Status: 400, Message: "listen_tls_server_cert_asset_id is required when listen tls is enabled"}
	}
	if tunnel.ListenTLSMode == tunnelTLSModeMTLS && !tunnel.ListenTLSLoadSystemCA && len(tunnel.ListenTLSClientCAAssetIDs) == 0 {
		return &Error{Status: 400, Message: "listen mtls requires system ca or at least one client ca asset"}
	}

	if tunnel.BackendTLSMode == tunnelTLSModeMTLS && tunnel.BackendTLSClientCertAssetID == nil {
		return &Error{Status: 400, Message: "backend_tls_client_cert_asset_id is required when backend mtls is enabled"}
	}
	if tunnel.BackendTLSMode != tunnelTLSModeOff &&
		!tunnel.BackendTLSInsecureSkipVerify &&
		!tunnel.BackendTLSLoadSystemCA &&
		len(tunnel.BackendTLSCAAssetIDs) == 0 {
		return &Error{Status: 400, Message: "backend tls requires system ca, custom ca, or insecure skip verify"}
	}

	return nil
}

func (s *Service) validateTunnelCertificateAsset(ctx context.Context, assetID int64, field string) error {
	if s == nil || s.entryCerts == nil {
		return &Error{Status: 503, Message: "entry certificate service is unavailable"}
	}
	if _, err := s.entryCerts.ResolveCertificateAsset(ctx, assetID); err != nil {
		return mapTunnelTLSAssetError(field, "certificate", err)
	}
	return nil
}

func (s *Service) validateTunnelCAAssets(ctx context.Context, assetIDs []int64, field string) error {
	if s == nil || s.entryCerts == nil {
		return &Error{Status: 503, Message: "entry certificate service is unavailable"}
	}
	if _, err := s.entryCerts.ResolveCAPoolAssets(ctx, assetIDs); err != nil {
		return mapTunnelTLSAssetError(field, "ca", err)
	}
	return nil
}

func mapTunnelTLSAssetError(field, want string, err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, sql.ErrNoRows):
		return &Error{Status: 400, Message: field + " references a missing " + want + " asset"}
	default:
		return &Error{Status: 400, Message: fmt.Sprintf("%s is invalid: %v", field, err)}
	}
}

func tunnelMutationWarnings(group ProxyGroupView, tunnel normalizedTunnel) []string {
	if proxygroups.NormalizeControlTransportSecurity(group.ControlTransportSecurity) != proxygroups.ControlTransportSecurityPlain {
		return nil
	}
	if tunnel.BackendTLSMode == tunnelTLSModeOff {
		return nil
	}
	if tunnel.BackendTLSClientCertAssetID == nil && len(tunnel.BackendTLSCAAssetIDs) == 0 {
		return nil
	}
	return []string{
		"当前分组 control_transport_security=plain，backend TLS 证书或 CA 会通过明文控制连接下发给 frpc，建议改为 tls_required。",
	}
}

func viewTunnelTLSMode(mode tunnelTLSMode) string {
	if mode == "" {
		return string(tunnelTLSModeOff)
	}
	return string(mode)
}
