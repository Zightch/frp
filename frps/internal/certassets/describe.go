package certassets

import (
	"crypto/x509"
	"net"
	"sort"
	"time"
)

const (
	AssetStatusNormal  = "normal"
	AssetStatusWarning = "warning"
	AssetStatusBroken  = "broken"
)

type DescribedAsset struct {
	Asset
	CommonName   string
	Subject      string
	Issuer       string
	IssuerName   string
	SerialNumber string
	NotBefore    string
	NotAfter     string
	DNSNames     []string
	IPAddresses  []string
	KeyPresent   bool
	CanIssue     bool
	IsSelfSigned bool
	ChainLength  int
	Status       string
	StatusReason string
}

func DescribePreparedAssets(prepared []PreparedAsset) []DescribedAsset {
	if len(prepared) == 0 {
		return nil
	}

	issuerNames := make(map[int64]string, len(prepared))
	for _, item := range prepared {
		issuerNames[item.Asset.ID] = item.Asset.Name
	}

	items := make([]DescribedAsset, 0, len(prepared))
	for _, item := range prepared {
		items = append(items, describePreparedAsset(item, issuerNames))
	}
	sort.Slice(items, func(left, right int) bool {
		return items[left].ID < items[right].ID
	})
	return items
}

func DescribeAssetsBestEffort(assets []Asset) []DescribedAsset {
	if len(assets) == 0 {
		return nil
	}

	issuerNames := make(map[int64]string, len(assets))
	for _, item := range assets {
		issuerNames[item.ID] = item.Name
	}

	now := time.Now().UTC()
	prepared, systemCAPool := PrepareAssetsBestEffort(assets, PrepareOptions{
		Now:                    now,
		IgnoreTimeValidity:     true,
		IgnoreSemanticValidity: true,
	})
	preparedByID := make(map[int64]PreparedAsset, len(prepared))
	for _, item := range prepared {
		preparedByID[item.Asset.ID] = item
	}

	items := make([]DescribedAsset, 0, len(assets))
	for _, item := range assets {
		items = append(items, describeAssetBestEffort(item, issuerNames, preparedByID, systemCAPool, now))
	}
	sort.Slice(items, func(left, right int) bool {
		return items[left].ID < items[right].ID
	})
	return items
}

func describePreparedAsset(item PreparedAsset, issuerNames map[int64]string) DescribedAsset {
	leaf := item.Leaf
	described := DescribedAsset{
		Asset:        item.Asset,
		KeyPresent:   item.Asset.HasKey(),
		CanIssue:     item.Asset.Source == SourceGenerated && item.Asset.AssetType == AssetTypeCA && item.Asset.HasKey(),
		IsSelfSigned: isSelfSigned(leaf),
		ChainLength:  len(item.Certificates),
		Status:       AssetStatusNormal,
	}
	if item.Asset.HasIssuer() {
		described.IssuerName = issuerNames[*item.Asset.IssuerAssetID]
	}
	if leaf == nil {
		return described
	}

	described.CommonName = leaf.Subject.CommonName
	described.Subject = leaf.Subject.String()
	described.Issuer = leaf.Issuer.String()
	described.SerialNumber = leaf.SerialNumber.Text(16)
	described.NotBefore = leaf.NotBefore.UTC().Format(schemaTimestampLayout)
	described.NotAfter = leaf.NotAfter.UTC().Format(schemaTimestampLayout)
	described.DNSNames = append([]string(nil), leaf.DNSNames...)
	described.IPAddresses = ipAddressesToStrings(leaf.IPAddresses)
	return described
}

func describeAssetBestEffort(asset Asset, issuerNames map[int64]string, preparedByID map[int64]PreparedAsset, systemCAPool *x509.CertPool, now time.Time) DescribedAsset {
	described := DescribedAsset{
		Asset:        asset,
		KeyPresent:   asset.HasKey(),
		CanIssue:     asset.Source == SourceGenerated && asset.AssetType == AssetTypeCA && asset.HasKey(),
		IsSelfSigned: false,
		ChainLength:  0,
		Status:       AssetStatusBroken,
	}
	if asset.HasIssuer() {
		described.IssuerName = issuerNames[*asset.IssuerAssetID]
	}

	prepared, ok := preparedByID[asset.ID]
	if !ok {
		broken, err := parseAsset(asset, now, PrepareOptions{
			Now:                    now,
			IgnoreTimeValidity:     true,
			IgnoreSemanticValidity: true,
		})
		if err == nil {
			prepared = broken
		} else {
			described.StatusReason = err.Error()
			return described
		}
	}

	described = describePreparedAsset(prepared, issuerNames)
	described.Status = AssetStatusNormal

	if err := validateCertificateTimes(prepared.Certificates, now); err != nil {
		described.Status = AssetStatusWarning
		described.StatusReason = err.Error()
		return described
	}
	if err := validateAssetChain(prepared, preparedByID, systemCAPool, now, false); err != nil {
		described.Status = AssetStatusWarning
		described.StatusReason = err.Error()
		return described
	}
	return described
}

func ipAddressesToStrings(values []net.IP) []string {
	if len(values) == 0 {
		return nil
	}
	result := make([]string, 0, len(values))
	for _, value := range values {
		if value == nil {
			continue
		}
		result = append(result, value.String())
	}
	return result
}

func FindPreparedAssetByID(prepared []PreparedAsset, id int64) (PreparedAsset, bool) {
	for _, item := range prepared {
		if item.Asset.ID == id {
			return item, true
		}
	}
	return PreparedAsset{}, false
}

func LeafCertificate(item PreparedAsset) *x509.Certificate {
	return item.Leaf
}
