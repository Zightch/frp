package api

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestHealthEndpoint(t *testing.T) {
	server := NewServer(
		Options{
			Addr:              "127.0.0.1:7500",
			ReadHeaderTimeout: 5 * time.Second,
		},
		slog.New(slog.NewTextHandler(io.Discard, nil)),
		"test",
	)

	request := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d", recorder.Code)
	}

	var payload map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	if payload["status"] != "ok" {
		t.Fatalf("unexpected status payload: %#v", payload["status"])
	}
	if payload["service"] != "frps" {
		t.Fatalf("unexpected service payload: %#v", payload["service"])
	}
}
