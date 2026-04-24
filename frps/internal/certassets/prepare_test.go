package certassets

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"strings"
	"testing"
	"time"
)

func TestPrepareRuntimeWithOptionsValidatesIssuerChain(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 4, 24, 12, 0, 0, 0, time.UTC)
	root := issueTestCertificate(t, certificateSpec{
		CommonName: "root-ca",
		IsCA:       true,
		NotBefore:  now.Add(-time.Hour),
		NotAfter:   now.Add(24 * time.Hour),
	})
	leaf := issueTestCertificate(t, certificateSpec{
		CommonName: "leaf-cert",
		NotBefore:  now.Add(-time.Hour),
		NotAfter:   now.Add(24 * time.Hour),
		Issuer:     &root,
	})

	rootHash, err := ComputeCRTHash(root.CertPEM)
	if err != nil {
		t.Fatalf("compute root crt_hash: %v", err)
	}
	leafHash, err := ComputeCRTHash(leaf.CertPEM)
	if err != nil {
		t.Fatalf("compute leaf crt_hash: %v", err)
	}

	runtime, err := PrepareRuntimeWithOptions(context.Background(), staticRepository{
		assets: []Asset{
			{
				ID:         1,
				Name:       "root-ca",
				Remark:     "",
				Source:     SourceUpload,
				AssetType:  AssetTypeCA,
				FormatType: FormatTypePEM,
				CRT:        root.CertPEM,
				CRTHash:    rootHash,
				Key:        "",
			},
			{
				ID:            2,
				Name:          "leaf-cert",
				Remark:        "",
				Source:        SourceUpload,
				AssetType:     AssetTypeCertificate,
				FormatType:    FormatTypePEM,
				CRT:           leaf.CertPEM,
				CRTHash:       leafHash,
				Key:           leaf.KeyPEM,
				IssuerAssetID: int64ptr(1),
			},
		},
	}, PrepareOptions{
		Now: now,
		LoadSystemCertPool: func() (*x509.CertPool, error) {
			return x509.NewCertPool(), nil
		},
	})
	if err != nil {
		t.Fatalf("prepare runtime: %v", err)
	}

	if runtime.AssetCount() != 2 {
		t.Fatalf("unexpected asset count: got %d want %d", runtime.AssetCount(), 2)
	}
	if runtime.SystemCACount() != 0 {
		t.Fatalf("unexpected system ca count: got %d want %d", runtime.SystemCACount(), 0)
	}
}

func TestPrepareRuntimeWithOptionsRejectsExpiredAsset(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 4, 24, 12, 0, 0, 0, time.UTC)
	root := issueTestCertificate(t, certificateSpec{
		CommonName: "root-ca",
		IsCA:       true,
		NotBefore:  now.Add(-48 * time.Hour),
		NotAfter:   now.Add(48 * time.Hour),
	})
	leaf := issueTestCertificate(t, certificateSpec{
		CommonName: "expired-leaf",
		NotBefore:  now.Add(-48 * time.Hour),
		NotAfter:   now.Add(-time.Hour),
		Issuer:     &root,
	})

	rootHash, err := ComputeCRTHash(root.CertPEM)
	if err != nil {
		t.Fatalf("compute root crt_hash: %v", err)
	}
	leafHash, err := ComputeCRTHash(leaf.CertPEM)
	if err != nil {
		t.Fatalf("compute leaf crt_hash: %v", err)
	}

	_, err = PrepareRuntimeWithOptions(context.Background(), staticRepository{
		assets: []Asset{
			{
				ID:         1,
				Name:       "root-ca",
				Remark:     "",
				Source:     SourceUpload,
				AssetType:  AssetTypeCA,
				FormatType: FormatTypePEM,
				CRT:        root.CertPEM,
				CRTHash:    rootHash,
			},
			{
				ID:            2,
				Name:          "expired-leaf",
				Remark:        "",
				Source:        SourceUpload,
				AssetType:     AssetTypeCertificate,
				FormatType:    FormatTypePEM,
				CRT:           leaf.CertPEM,
				CRTHash:       leafHash,
				Key:           leaf.KeyPEM,
				IssuerAssetID: int64ptr(1),
			},
		},
	}, PrepareOptions{
		Now: now,
		LoadSystemCertPool: func() (*x509.CertPool, error) {
			return x509.NewCertPool(), nil
		},
	})
	if err == nil {
		t.Fatal("expected expired asset to fail")
	}
	if !strings.Contains(err.Error(), "expired at") {
		t.Fatalf("expected expired error, got %v", err)
	}
}

func TestPrepareRuntimeWithOptionsRejectsCRTHashMismatch(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 4, 24, 12, 0, 0, 0, time.UTC)
	root := issueTestCertificate(t, certificateSpec{
		CommonName: "root-ca",
		IsCA:       true,
		NotBefore:  now.Add(-time.Hour),
		NotAfter:   now.Add(24 * time.Hour),
	})

	_, err := PrepareRuntimeWithOptions(context.Background(), staticRepository{
		assets: []Asset{
			{
				ID:         1,
				Name:       "root-ca",
				Remark:     "",
				Source:     SourceUpload,
				AssetType:  AssetTypeCA,
				FormatType: FormatTypePEM,
				CRT:        root.CertPEM,
				CRTHash:    strings.Repeat("0", 64),
			},
		},
	}, PrepareOptions{
		Now: now,
		LoadSystemCertPool: func() (*x509.CertPool, error) {
			return x509.NewCertPool(), nil
		},
	})
	if err == nil {
		t.Fatal("expected crt_hash mismatch to fail")
	}
	if !strings.Contains(err.Error(), "crt_hash mismatch") {
		t.Fatalf("expected crt_hash mismatch error, got %v", err)
	}
}

type staticRepository struct {
	assets []Asset
}

func (r staticRepository) ListAssets(context.Context) ([]Asset, error) {
	return append([]Asset(nil), r.assets...), nil
}

type certificateSpec struct {
	CommonName string
	IsCA       bool
	NotBefore  time.Time
	NotAfter   time.Time
	Issuer     *issuedCertificate
}

type issuedCertificate struct {
	CertPEM string
	KeyPEM  string
	Cert    *x509.Certificate
	Key     *ecdsa.PrivateKey
}

func issueTestCertificate(t *testing.T, spec certificateSpec) issuedCertificate {
	t.Helper()

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}

	template := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()),
		Subject: pkix.Name{
			CommonName: spec.CommonName,
		},
		BasicConstraintsValid: true,
		IsCA:                  spec.IsCA,
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		NotBefore:             spec.NotBefore,
		NotAfter:              spec.NotAfter,
	}
	if spec.IsCA {
		template.KeyUsage = x509.KeyUsageCertSign | x509.KeyUsageCRLSign
	}

	parent := template
	signerKey := key
	if spec.Issuer != nil {
		parent = spec.Issuer.Cert
		signerKey = spec.Issuer.Key
	}

	der, err := x509.CreateCertificate(rand.Reader, template, parent, key.Public(), signerKey)
	if err != nil {
		t.Fatalf("create certificate: %v", err)
	}
	parsed, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatalf("parse generated certificate: %v", err)
	}

	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatalf("marshal private key: %v", err)
	}

	return issuedCertificate{
		CertPEM: string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})),
		KeyPEM:  string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})),
		Cert:    parsed,
		Key:     key,
	}
}

func int64ptr(value int64) *int64 {
	return &value
}
