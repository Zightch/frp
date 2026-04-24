package certassets

import (
	"crypto/x509"
	"net"
	"sort"
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

func describePreparedAsset(item PreparedAsset, issuerNames map[int64]string) DescribedAsset {
	leaf := item.Leaf
	described := DescribedAsset{
		Asset:        item.Asset,
		KeyPresent:   item.Asset.HasKey(),
		CanIssue:     item.Asset.AssetType == AssetTypeCA && item.Asset.HasKey(),
		IsSelfSigned: isSelfSigned(leaf),
		ChainLength:  len(item.Certificates),
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
