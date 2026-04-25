package api

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/zightch/frp/frps/internal/settings/entrycerts"
)

func TestEntryCertificatesSettingsPathUsesCanonicalFrpcTLSUsageType(t *testing.T) {
	server, sessionCookie, _ := newEntryCertificatesTestServer(t)
	leafID := createBindableEntryCertificateAsset(t, server, sessionCookie)

	bound := performRequest(
		t,
		server.Handler(),
		http.MethodPut,
		entryCertificatesPathSettings+"/frpc_tls",
		map[string]any{
			"asset_id": leafID,
			"enabled":  true,
		},
		http.StatusOK,
		sessionCookie,
	)
	if item := bound.JSON["item"].(map[string]any); item["usage_type"] != "frpc_tls" {
		t.Fatalf("unexpected bind response: %#v", bound.JSON)
	}

	listed := performRequest(
		t,
		server.Handler(),
		http.MethodGet,
		entryCertificatesPathSettings,
		nil,
		http.StatusOK,
		sessionCookie,
	)

	items := listed.JSON["items"].([]any)
	if len(items) != 2 {
		t.Fatalf("unexpected entry certificate count: %#v", items)
	}
	webUIItem := findEntryCertificateUsage(t, items, "webui_https")
	frpcItem := findEntryCertificateUsage(t, items, "frpc_tls")

	if webUIItem["status"] != "unbound" {
		t.Fatalf("unexpected webui https item: %#v", webUIItem)
	}
	assertEntryCertificateUsageMatchesAsset(t, frpcItem, leafID)
}

func TestEntryCertificatesLegacyPathReturnsNotFound(t *testing.T) {
	server, sessionCookie, _ := newEntryCertificatesTestServer(t)

	request := httptest.NewRequest(http.MethodGet, "/api/v1/certificate-usages", nil)
	request.AddCookie(sessionCookie)

	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, request)

	if recorder.Code != http.StatusNotFound {
		t.Fatalf("unexpected legacy path status: got %d body=%s", recorder.Code, recorder.Body.String())
	}
}

func newEntryCertificatesTestServer(t *testing.T) (*Server, *http.Cookie, *recordingControlTLSRuntime) {
	t.Helper()

	store := newTestStore(t)
	manager := newTestAuthManager(t, true)
	runtime := &recordingControlTLSRuntime{}

	server, err := NewServer(
		Options{
			Addr:              "127.0.0.1:7080",
			ReadHeaderTimeout: 5 * time.Second,
			Store:             store,
			Auth:              manager,
			ControlTLSRuntime: runtime,
		},
		slog.New(slog.NewTextHandler(io.Discard, nil)),
		"test",
	)
	if err != nil {
		t.Fatalf("new server: %v", err)
	}

	return server, authenticatedManagementCookie(t, manager), runtime
}

func createBindableEntryCertificateAsset(t *testing.T, server *Server, sessionCookie *http.Cookie) int64 {
	t.Helper()

	root := performRequest(
		t,
		server.Handler(),
		http.MethodPost,
		"/api/v1/certificate-assets/generate",
		map[string]any{
			"name":          "entry-root-ca",
			"asset_type":    "ca",
			"common_name":   "Entry Root CA",
			"validity_days": 365,
		},
		http.StatusCreated,
		sessionCookie,
	)
	rootID := int64(root.JSON["item"].(map[string]any)["id"].(float64))

	leaf := performRequest(
		t,
		server.Handler(),
		http.MethodPost,
		"/api/v1/certificate-assets/generate",
		map[string]any{
			"name":            "entry-leaf-cert",
			"asset_type":      "certificate",
			"issuer_asset_id": rootID,
			"common_name":     "entry.example.com",
			"validity_days":   90,
			"dns_names":       []string{"entry.example.com"},
		},
		http.StatusCreated,
		sessionCookie,
	)
	return int64(leaf.JSON["item"].(map[string]any)["id"].(float64))
}

func findEntryCertificateUsage(t *testing.T, items []any, usageType string) map[string]any {
	t.Helper()

	for _, raw := range items {
		item, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		if item["usage_type"] == usageType {
			return item
		}
	}
	t.Fatalf("entry certificate %q not found in %#v", usageType, items)
	return nil
}

func assertEntryCertificateUsageMatchesAsset(t *testing.T, item map[string]any, assetID int64) {
	t.Helper()

	if item["asset_id"] != float64(assetID) {
		t.Fatalf("unexpected asset binding: %#v", item)
	}
	if item["status"] != "enabled" {
		t.Fatalf("unexpected entry certificate status: %#v", item)
	}
	if item["enabled"] != true {
		t.Fatalf("unexpected enabled flag: %#v", item)
	}
}

type recordingControlTLSRuntime struct {
	configureCount int
	clearCount     int
}

func (r *recordingControlTLSRuntime) ConfigureControlTLS(binding *entrycerts.ResolvedBinding) error {
	if binding == nil {
		return nil
	}
	r.configureCount++
	return nil
}

func (r *recordingControlTLSRuntime) ClearControlTLS() error {
	r.clearCount++
	return nil
}
