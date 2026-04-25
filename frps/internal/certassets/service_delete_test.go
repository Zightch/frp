package certassets

import (
	"crypto/x509"
	"testing"
	"time"
)

func TestBuildDeleteImpactIncludesUploadedAssetsThatLoseDatabaseBackedChain(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 4, 25, 12, 0, 0, 0, time.UTC)
	root := issueTestCertificate(t, certificateSpec{
		CommonName: "delete-root",
		IsCA:       true,
		NotBefore:  now.Add(-time.Hour),
		NotAfter:   now.Add(24 * time.Hour),
	})
	intermediate := issueTestCertificate(t, certificateSpec{
		CommonName: "delete-intermediate",
		IsCA:       true,
		NotBefore:  now.Add(-time.Hour),
		NotAfter:   now.Add(24 * time.Hour),
		Issuer:     &root,
	})
	leaf := issueTestCertificate(t, certificateSpec{
		CommonName: "delete-leaf",
		NotBefore:  now.Add(-time.Hour),
		NotAfter:   now.Add(24 * time.Hour),
		Issuer:     &intermediate,
	})

	rootHash, err := ComputeCRTHash(root.CertPEM)
	if err != nil {
		t.Fatalf("compute root crt hash: %v", err)
	}
	intermediateHash, err := ComputeCRTHash(intermediate.CertPEM)
	if err != nil {
		t.Fatalf("compute intermediate crt hash: %v", err)
	}
	leafHash, err := ComputeCRTHash(leaf.CertPEM)
	if err != nil {
		t.Fatalf("compute leaf crt hash: %v", err)
	}

	assets := []Asset{
		{
			ID:         1,
			Name:       "delete-root",
			Source:     SourceUpload,
			AssetType:  AssetTypeCA,
			FormatType: FormatTypePEM,
			CRT:        root.CertPEM,
			CRTHash:    rootHash,
			Key:        root.KeyPEM,
		},
		{
			ID:         2,
			Name:       "delete-intermediate",
			Source:     SourceUpload,
			AssetType:  AssetTypeCA,
			FormatType: FormatTypePEM,
			CRT:        intermediate.CertPEM,
			CRTHash:    intermediateHash,
			Key:        intermediate.KeyPEM,
		},
		{
			ID:         3,
			Name:       "delete-leaf",
			Source:     SourceUpload,
			AssetType:  AssetTypeCertificate,
			FormatType: FormatTypePEM,
			CRT:        leaf.CertPEM,
			CRTHash:    leafHash,
			Key:        leaf.KeyPEM,
		},
	}

	options := PrepareOptions{
		Now: now,
		LoadSystemCertPool: func() (*x509.CertPool, error) {
			return x509.NewCertPool(), nil
		},
	}

	prepared, _, err := PrepareAssetsWithOptions(assets, options)
	if err != nil {
		t.Fatalf("prepare assets: %v", err)
	}

	impact, err := buildDeleteImpact(assets, prepared, 1, options)
	if err != nil {
		t.Fatalf("build delete impact: %v", err)
	}

	if impact.Target.ID != 1 {
		t.Fatalf("unexpected target id: got %d want 1", impact.Target.ID)
	}
	if len(impact.Affected) != 2 {
		t.Fatalf("unexpected affected count: %#v", impact)
	}

	affectedByID := make(map[int64]DeleteImpactItem, len(impact.Affected))
	for _, item := range impact.Affected {
		affectedByID[item.Item.ID] = item
	}
	if _, exists := affectedByID[2]; !exists {
		t.Fatalf("expected intermediate to be affected: %#v", impact)
	}
	if _, exists := affectedByID[3]; !exists {
		t.Fatalf("expected leaf to be affected: %#v", impact)
	}
}
