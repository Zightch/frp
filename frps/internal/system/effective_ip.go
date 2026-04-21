package system

import (
	"fmt"
	"net"
	"strings"
)

const (
	AnyIPv4 = "0.0.0.0"
	AnyIPv6 = "::"
)

func NormalizeListenIP(raw string) (string, error) {
	value := strings.TrimSpace(raw)
	if value == "" {
		return "", fmt.Errorf("ip literal is required")
	}

	normalized, ok := normalizeIP(net.ParseIP(value))
	if !ok {
		return "", fmt.Errorf("must be a valid ip literal")
	}

	return normalized.Addr, nil
}

func IsSpecialListenIP(addr string) bool {
	switch strings.TrimSpace(addr) {
	case AnyIPv4, AnyIPv6:
		return true
	default:
		return false
	}
}
