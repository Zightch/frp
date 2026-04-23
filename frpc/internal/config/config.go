package config

import (
	"encoding/hex"
	"fmt"
	"net"
	"strings"
)

const (
	clientIDHexLen     = 32
	clientSecretHexLen = 64
)

type Config struct {
	Server       string
	ClientID     string
	ClientSecret string
}

type Credentials struct {
	ClientID     [16]byte
	ClientSecret [32]byte
}

func (c *Config) Validate() error {
	c.Server = strings.TrimSpace(c.Server)
	c.ClientID = strings.TrimSpace(c.ClientID)
	c.ClientSecret = strings.TrimSpace(c.ClientSecret)

	if c.Server == "" {
		return fmt.Errorf("server is required")
	}
	if c.ClientID == "" {
		return fmt.Errorf("client_id is required")
	}
	if c.ClientSecret == "" {
		return fmt.Errorf("client_secret is required")
	}
	if _, _, err := net.SplitHostPort(c.Server); err != nil {
		return fmt.Errorf("server: %w", err)
	}
	if _, err := ParseClientID(c.ClientID); err != nil {
		return fmt.Errorf("client_id: %w", err)
	}
	if _, err := ParseClientSecret(c.ClientSecret); err != nil {
		return fmt.Errorf("client_secret: %w", err)
	}

	return nil
}

func ParseCredentials(clientIDValue, clientSecretValue string) (Credentials, error) {
	var credentials Credentials
	var err error

	credentials.ClientID, err = ParseClientID(clientIDValue)
	if err != nil {
		return credentials, err
	}
	credentials.ClientSecret, err = ParseClientSecret(clientSecretValue)
	if err != nil {
		return credentials, err
	}
	return credentials, nil
}

func ParseClientID(value string) ([16]byte, error) {
	raw, err := decodeLowerHex(value, clientIDHexLen)
	if err != nil {
		return [16]byte{}, err
	}

	var clientID [16]byte
	if len(raw) != len(clientID) {
		return clientID, fmt.Errorf("expected %d client_id bytes, got %d", len(clientID), len(raw))
	}
	copy(clientID[:], raw)
	return clientID, nil
}

func ParseClientSecret(value string) ([32]byte, error) {
	raw, err := decodeLowerHex(value, clientSecretHexLen)
	if err != nil {
		return [32]byte{}, err
	}

	var clientSecret [32]byte
	if len(raw) != len(clientSecret) {
		return clientSecret, fmt.Errorf("expected %d client_secret bytes, got %d", len(clientSecret), len(raw))
	}
	copy(clientSecret[:], raw)
	return clientSecret, nil
}

func decodeLowerHex(value string, expectedHexLen int) ([]byte, error) {
	value = strings.TrimSpace(value)
	if len(value) != expectedHexLen {
		return nil, fmt.Errorf("expected %d hex characters, got %d", expectedHexLen, len(value))
	}
	if strings.ToLower(value) != value {
		return nil, fmt.Errorf("must use lowercase hex")
	}

	raw, err := hex.DecodeString(value)
	if err != nil {
		return nil, err
	}
	return raw, nil
}
