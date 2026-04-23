package config

import "testing"

func TestConfigValidate(t *testing.T) {
	cfg := Config{
		Server: "127.0.0.1:7000",
		Key:    "00112233445566778899aabbccddeeff0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
	}

	if err := cfg.Validate(); err != nil {
		t.Fatalf("validate config: %v", err)
	}
}

func TestParseKeyRejectsInvalidLength(t *testing.T) {
	if _, err := ParseKey("abcd"); err == nil {
		t.Fatal("expected invalid key length error")
	}
}

func TestParseKeyRejectsUppercase(t *testing.T) {
	_, err := ParseKey("00112233445566778899aabbccddeeff0123456789abcdef0123456789abcdef0123456789abcdef0123456789ABCDEF")
	if err == nil {
		t.Fatal("expected uppercase key rejection")
	}
}
