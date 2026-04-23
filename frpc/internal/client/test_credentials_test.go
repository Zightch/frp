package client

import (
	"testing"

	appconfig "github.com/zightch/frp/frpc/internal/config"
)

const (
	testClientIDValue     = "00112233445566778899aabbccddeeff"
	testClientSecretValue = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	testKeyValue          = testClientIDValue + testClientSecretValue
)

func testConfig() appconfig.Config {
	return appconfig.Config{
		Server: "127.0.0.1:7000",
		Key:    testKeyValue,
	}
}

func testCredentials(t *testing.T) appconfig.Credentials {
	t.Helper()

	credentials, err := appconfig.ParseKey(testKeyValue)
	if err != nil {
		t.Fatalf("parse test credentials: %v", err)
	}
	return credentials
}
