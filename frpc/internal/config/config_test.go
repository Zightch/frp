package config

import "testing"

func TestConfigValidate(t *testing.T) {
	cfg := Config{
		Server: "127.0.0.1:7000",
		Token:  "00112233445566778899aabbccddeeff0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
	}

	if err := cfg.Validate(); err != nil {
		t.Fatalf("validate config: %v", err)
	}
}

func TestParseTokenRejectsInvalidLength(t *testing.T) {
	if _, err := ParseToken("abcd"); err == nil {
		t.Fatal("expected invalid token length error")
	}
}

func TestParseTokenRejectsUppercase(t *testing.T) {
	_, err := ParseToken("00112233445566778899AABBCCDDEEFF0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef")
	if err == nil {
		t.Fatal("expected uppercase token rejection")
	}
}
