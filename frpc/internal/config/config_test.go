package config

import "testing"

func TestConfigValidate(t *testing.T) {
	cfg := Config{
		Server:       "127.0.0.1:7000",
		ClientID:     "00112233445566778899aabbccddeeff",
		ClientSecret: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
	}

	if err := cfg.Validate(); err != nil {
		t.Fatalf("validate config: %v", err)
	}
}

func TestParseClientIDRejectsInvalidLength(t *testing.T) {
	if _, err := ParseClientID("abcd"); err == nil {
		t.Fatal("expected invalid client_id length error")
	}
}

func TestParseClientSecretRejectsUppercase(t *testing.T) {
	_, err := ParseClientSecret("0123456789abcdef0123456789abcdef0123456789abcdef0123456789ABCDEF")
	if err == nil {
		t.Fatal("expected uppercase client_secret rejection")
	}
}
