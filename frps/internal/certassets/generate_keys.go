package certassets

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"fmt"
	"strconv"
	"strings"
)

type GenerateKeyAlgorithm string

const minimumRSAGenerateKeyBits = 2048

const (
	GenerateKeyAlgorithmECDSA   GenerateKeyAlgorithm = "ecdsa"
	GenerateKeyAlgorithmRSA     GenerateKeyAlgorithm = "rsa"
	GenerateKeyAlgorithmED25519 GenerateKeyAlgorithm = "ed25519"
)

const defaultGenerateKeyAlgorithm = GenerateKeyAlgorithmECDSA

type GenerateKeySpec struct {
	Algorithm GenerateKeyAlgorithm
	Bits      int
}

func normalizeGenerateKeySpec(algorithm GenerateKeyAlgorithm, bits int) (GenerateKeySpec, error) {
	normalizedAlgorithm := normalizeGenerateKeyAlgorithm(algorithm)
	if normalizedAlgorithm == "" {
		normalizedAlgorithm = defaultGenerateKeyAlgorithm
	}

	if bits == 0 {
		bits = defaultGenerateKeyBits(normalizedAlgorithm)
	}

	switch normalizedAlgorithm {
	case GenerateKeyAlgorithmECDSA, GenerateKeyAlgorithmRSA, GenerateKeyAlgorithmED25519:
	default:
		return GenerateKeySpec{}, validationError("key_algorithm must be rsa, ecdsa or ed25519", ValidationIssue{
			Field:   "key_algorithm",
			Code:    "invalid_key_algorithm",
			Message: "key_algorithm must be rsa, ecdsa or ed25519",
		})
	}

	if err := validateGenerateKeyBits(normalizedAlgorithm, bits); err != nil {
		return GenerateKeySpec{}, err
	}

	return GenerateKeySpec{
		Algorithm: normalizedAlgorithm,
		Bits:      bits,
	}, nil
}

func normalizeGenerateKeyAlgorithm(value GenerateKeyAlgorithm) GenerateKeyAlgorithm {
	return GenerateKeyAlgorithm(strings.ToLower(strings.TrimSpace(string(value))))
}

func defaultGenerateKeyBits(algorithm GenerateKeyAlgorithm) int {
	switch algorithm {
	case GenerateKeyAlgorithmRSA:
		return minimumRSAGenerateKeyBits
	case GenerateKeyAlgorithmED25519:
		return 256
	default:
		return 256
	}
}

func supportedGenerateKeyBits(algorithm GenerateKeyAlgorithm) []int {
	switch algorithm {
	case GenerateKeyAlgorithmECDSA:
		return []int{256, 384, 521}
	case GenerateKeyAlgorithmED25519:
		return []int{256}
	default:
		return nil
	}
}

func formatGenerateKeyBits(algorithm GenerateKeyAlgorithm) string {
	if algorithm == GenerateKeyAlgorithmRSA {
		return fmt.Sprintf("an integer multiple of 8 and >= %d", minimumRSAGenerateKeyBits)
	}

	items := supportedGenerateKeyBits(algorithm)
	formatted := make([]string, 0, len(items))
	for _, item := range items {
		formatted = append(formatted, strconv.Itoa(item))
	}
	return strings.Join(formatted, ", ")
}

func validateGenerateKeyBits(algorithm GenerateKeyAlgorithm, bits int) error {
	if algorithm == GenerateKeyAlgorithmRSA {
		if bits < minimumRSAGenerateKeyBits || bits%8 != 0 {
			return validationError(
				fmt.Sprintf("key_bits must be %s for %s", formatGenerateKeyBits(algorithm), algorithm),
				ValidationIssue{
					Field:   "key_bits",
					Code:    "invalid_key_bits",
					Message: fmt.Sprintf("key_bits must be %s for %s", formatGenerateKeyBits(algorithm), algorithm),
				},
			)
		}
		return nil
	}

	for _, item := range supportedGenerateKeyBits(algorithm) {
		if item == bits {
			return nil
		}
	}

	return validationError(
		fmt.Sprintf("key_bits must be one of %s for %s", formatGenerateKeyBits(algorithm), algorithm),
		ValidationIssue{
			Field:   "key_bits",
			Code:    "invalid_key_bits",
			Message: fmt.Sprintf("key_bits must be one of %s for %s", formatGenerateKeyBits(algorithm), algorithm),
		},
	)
}

func generatePrivateKey(spec GenerateKeySpec) (crypto.Signer, error) {
	switch spec.Algorithm {
	case GenerateKeyAlgorithmRSA:
		return rsa.GenerateKey(rand.Reader, spec.Bits)
	case GenerateKeyAlgorithmECDSA:
		curve, err := curveForECDSABits(spec.Bits)
		if err != nil {
			return nil, err
		}
		return ecdsa.GenerateKey(curve, rand.Reader)
	case GenerateKeyAlgorithmED25519:
		_, privateKey, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			return nil, err
		}
		return privateKey, nil
	default:
		return nil, fmt.Errorf("unsupported key algorithm %q", spec.Algorithm)
	}
}

func curveForECDSABits(bits int) (elliptic.Curve, error) {
	switch bits {
	case 256:
		return elliptic.P256(), nil
	case 384:
		return elliptic.P384(), nil
	case 521:
		return elliptic.P521(), nil
	default:
		return nil, fmt.Errorf("unsupported ecdsa key bits %d", bits)
	}
}
