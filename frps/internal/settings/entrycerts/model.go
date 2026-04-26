package entrycerts

import (
	"crypto/tls"
	"crypto/x509"
	"strings"
	"time"

	"github.com/zightch/frp/frps/internal/certassets"
)

type TargetType string

const (
	TargetTypeGlobal TargetType = "global"
	TargetTypeTunnel TargetType = "tunnel"
)

type UsageType string

const (
	UsageTypeWebUIHTTPS              UsageType = "webui_https"
	UsageTypeFrpcTLS                 UsageType = "frpc_tls"
	UsageTypeTunnelListenServerCert  UsageType = "tunnel_listen_server_cert"
	UsageTypeTunnelListenClientCA    UsageType = "tunnel_listen_client_ca"
	UsageTypeTunnelBackendClientCert UsageType = "tunnel_backend_client_cert"
	UsageTypeTunnelBackendCA         UsageType = "tunnel_backend_ca"
)

const globalTargetID int64 = 0

type Usage struct {
	ID         int64
	TargetType TargetType
	UsageType  UsageType
	TargetID   int64
	AssetID    int64
	Enabled    bool
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

type DescribedUsage struct {
	UsageType           UsageType
	AssetID             *int64
	AssetName           string
	Asset               *certassets.DescribedAsset
	Enabled             bool
	Status              string
	StatusReason        string
	ResolvedChainLength int
	UpdatedAt           time.Time
}

type ResolvedBinding struct {
	UsageType           UsageType
	Asset               certassets.DescribedAsset
	CertificatePEM      string
	KeyPEM              string
	TLSCertificate      tls.Certificate
	ResolvedChainLength int
}

type ResolvedCAPool struct {
	UsageType UsageType
	Assets    []certassets.DescribedAsset
	PEM       string
	Pool      *x509.CertPool
}

func GlobalUsageTypes() []UsageType {
	return []UsageType{
		UsageTypeWebUIHTTPS,
		UsageTypeFrpcTLS,
	}
}

func AllUsageTypes() []UsageType {
	return []UsageType{
		UsageTypeWebUIHTTPS,
		UsageTypeFrpcTLS,
		UsageTypeTunnelListenServerCert,
		UsageTypeTunnelListenClientCA,
		UsageTypeTunnelBackendClientCert,
		UsageTypeTunnelBackendCA,
	}
}

func NormalizeTargetType(value string) TargetType {
	switch TargetType(strings.ToLower(strings.TrimSpace(value))) {
	case TargetTypeGlobal:
		return TargetTypeGlobal
	case TargetTypeTunnel:
		return TargetTypeTunnel
	default:
		return ""
	}
}

func NormalizeUsageType(value string) UsageType {
	switch UsageType(strings.ToLower(strings.TrimSpace(value))) {
	case UsageTypeWebUIHTTPS:
		return UsageTypeWebUIHTTPS
	case UsageTypeFrpcTLS:
		return UsageTypeFrpcTLS
	case UsageTypeTunnelListenServerCert:
		return UsageTypeTunnelListenServerCert
	case UsageTypeTunnelListenClientCA:
		return UsageTypeTunnelListenClientCA
	case UsageTypeTunnelBackendClientCert:
		return UsageTypeTunnelBackendClientCert
	case UsageTypeTunnelBackendCA:
		return UsageTypeTunnelBackendCA
	default:
		return ""
	}
}

func IsMultiBindingUsage(value UsageType) bool {
	switch value {
	case UsageTypeTunnelListenClientCA, UsageTypeTunnelBackendCA:
		return true
	default:
		return false
	}
}
