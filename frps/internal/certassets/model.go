package certassets

import "time"

type Source string

const (
	SourceUpload    Source = "upload"
	SourceGenerated Source = "generated"
)

type AssetType string

const (
	AssetTypeCertificate AssetType = "certificate"
	AssetTypeCA          AssetType = "ca"
)

type FormatType string

const (
	FormatTypePEM FormatType = "pem"
)

type Asset struct {
	ID            int64
	Name          string
	Remark        string
	Source        Source
	AssetType     AssetType
	FormatType    FormatType
	CRT           string
	CRTHash       string
	Key           string
	IssuerAssetID *int64
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

func (a Asset) HasIssuer() bool {
	return a.IssuerAssetID != nil && *a.IssuerAssetID > 0
}

func (a Asset) HasKey() bool {
	return a.Key != ""
}
