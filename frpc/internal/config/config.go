package config

import (
	"encoding/hex"
	"fmt"
	"net"
	"strings"
)

const (
	tokenIDHexLen     = 32
	tokenSecretHexLen = 64
	tokenHexLen       = tokenIDHexLen + tokenSecretHexLen
)

type Config struct {
	Server string
	Token  string
}

type Token struct {
	ID     [16]byte
	Secret [32]byte
}

func (c *Config) Validate() error {
	c.Server = strings.TrimSpace(c.Server)
	c.Token = strings.TrimSpace(c.Token)

	if c.Server == "" {
		return fmt.Errorf("server is required")
	}
	if c.Token == "" {
		return fmt.Errorf("token is required")
	}
	if _, _, err := net.SplitHostPort(c.Server); err != nil {
		return fmt.Errorf("server: %w", err)
	}
	if _, err := ParseToken(c.Token); err != nil {
		return fmt.Errorf("token: %w", err)
	}

	return nil
}

func ParseToken(value string) (Token, error) {
	var token Token

	value = strings.TrimSpace(value)
	if len(value) != tokenHexLen {
		return token, fmt.Errorf("expected %d hex characters, got %d", tokenHexLen, len(value))
	}
	if strings.ToLower(value) != value {
		return token, fmt.Errorf("must use lowercase hex")
	}

	raw, err := hex.DecodeString(value)
	if err != nil {
		return token, err
	}
	if len(raw) != len(token.ID)+len(token.Secret) {
		return token, fmt.Errorf("expected %d token bytes, got %d", len(token.ID)+len(token.Secret), len(raw))
	}

	copy(token.ID[:], raw[:len(token.ID)])
	copy(token.Secret[:], raw[len(token.ID):])
	return token, nil
}
