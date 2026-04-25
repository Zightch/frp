package certassets

import "testing"

func TestNormalizeGenerateKeySpecSupportsLongRSAKeySizes(t *testing.T) {
	for _, bits := range []int{2048, 3072, 4096, 5008, 6144, 8192} {
		spec, err := normalizeGenerateKeySpec(GenerateKeyAlgorithmRSA, bits)
		if err != nil {
			t.Fatalf("normalize rsa key spec %d: %v", bits, err)
		}
		if spec.Algorithm != GenerateKeyAlgorithmRSA {
			t.Fatalf("unexpected algorithm for %d: %q", bits, spec.Algorithm)
		}
		if spec.Bits != bits {
			t.Fatalf("unexpected bits for %d: got %d", bits, spec.Bits)
		}
	}
}

func TestNormalizeGenerateKeySpecRejectsUnsupportedRSAKeySize(t *testing.T) {
	_, err := normalizeGenerateKeySpec(GenerateKeyAlgorithmRSA, 1024)
	if err == nil {
		t.Fatal("expected rsa key size below minimum to be rejected")
	}

	_, err = normalizeGenerateKeySpec(GenerateKeyAlgorithmRSA, 2050)
	if err == nil {
		t.Fatal("expected non-byte-aligned rsa key size to be rejected")
	}
}
