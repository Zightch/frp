package certassets

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"strings"
)

func SameAssetContent(left, right Asset) bool {
	if left.AssetType != right.AssetType || left.FormatType != right.FormatType {
		return false
	}

	leftCerts, err := parseCertificatesPEM(left.CRT)
	if err != nil {
		return false
	}
	rightCerts, err := parseCertificatesPEM(right.CRT)
	if err != nil {
		return false
	}
	if !sameCertificateChain(leftCerts, rightCerts) {
		return false
	}

	leftKey, err := canonicalizePrivateKey(left.Key)
	if err != nil {
		return false
	}
	rightKey, err := canonicalizePrivateKey(right.Key)
	if err != nil {
		return false
	}
	return bytes.Equal(leftKey, rightKey)
}

func sameCertificateChain(left, right []*x509.Certificate) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if !bytes.Equal(left[index].Raw, right[index].Raw) {
			return false
		}
	}
	return true
}

func canonicalizePrivateKey(value string) ([]byte, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil, nil
	}

	parsed, err := parsePrivateKeyPEM(value)
	if err != nil {
		return nil, err
	}

	switch key := parsed.(type) {
	case *rsa.PrivateKey:
		return x509.MarshalPKCS8PrivateKey(key)
	case *ecdsa.PrivateKey:
		return x509.MarshalPKCS8PrivateKey(key)
	case ed25519.PrivateKey:
		return x509.MarshalPKCS8PrivateKey(key)
	default:
		return nil, fmt.Errorf("unsupported private key type %T", parsed)
	}
}

func parsePrivateKeyPEM(value string) (any, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil, nil
	}

	block, rest := pem.Decode([]byte(value))
	if block == nil {
		return nil, fmt.Errorf("invalid pem data")
	}
	if len(bytes.TrimSpace(rest)) > 0 {
		return nil, fmt.Errorf("unexpected extra pem data in key")
	}

	switch block.Type {
	case "PRIVATE KEY":
		return x509.ParsePKCS8PrivateKey(block.Bytes)
	case "RSA PRIVATE KEY":
		return x509.ParsePKCS1PrivateKey(block.Bytes)
	case "EC PRIVATE KEY":
		return x509.ParseECPrivateKey(block.Bytes)
	default:
		return nil, fmt.Errorf("unsupported pem block %q in key", block.Type)
	}
}
