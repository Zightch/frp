package certusages

import (
	"crypto/tls"
	"time"

	"github.com/zightch/frp/frps/internal/certassets"
)

type UsageType string

const (
	UsageTypeWebUIHTTPS         UsageType = "webui_https"
	UsageTypeControlListenerTLS UsageType = "control_listener_tls"
)

const globalTargetID int64 = 0

type Usage struct {
	ID        int64
	UsageType UsageType
	TargetID  int64
	AssetID   int64
	Enabled   bool
	CreatedAt time.Time
	UpdatedAt time.Time
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

func AllUsageTypes() []UsageType {
	return []UsageType{
		UsageTypeWebUIHTTPS,
		UsageTypeControlListenerTLS,
	}
}
