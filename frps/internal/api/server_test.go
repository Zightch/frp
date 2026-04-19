package api

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	authn "github.com/zightch/frp/frps/internal/auth"
	"github.com/zightch/frp/frps/internal/storage"
	_ "github.com/zightch/frp/frps/internal/storage/drivers"
)

func TestHealthEndpoint(t *testing.T) {
	server := NewServer(
		Options{
			Addr:              "127.0.0.1:7500",
			ReadHeaderTimeout: 5 * time.Second,
			Auth:              newTestAuthManager(t, false),
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

func TestAuthInitializationAndChallengeEndpoints(t *testing.T) {
	store := newTestStore(t)
	authPath := filepath.Join(t.TempDir(), "auth.json")
	manager, err := authn.NewManager(authn.Options{Path: authPath})
	if err != nil {
		t.Fatalf("new auth manager: %v", err)
	}

	server := NewServer(
		Options{
			Addr:              "127.0.0.1:7500",
			ReadHeaderTimeout: 5 * time.Second,
			Store:             store,
			Auth:              manager,
		},
		slog.New(slog.NewTextHandler(io.Discard, nil)),
		"test",
	)

	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("unexpected index status: %d", recorder.Code)
	}
	if body := recorder.Body.String(); !bytes.Contains([]byte(body), []byte("frps 管理面改造中")) {
		t.Fatalf("unexpected index body: %s", body)
	}

	state := performJSONRequest(
		t,
		server.Handler(),
		http.MethodGet,
		"/api/v1/auth/state",
		nil,
		http.StatusOK,
	)
	if state["initialized"] != false {
		t.Fatalf("unexpected initialization state: %#v", state)
	}

	performJSONRequest(
		t,
		server.Handler(),
		http.MethodGet,
		"/api/v1/proxy-groups",
		nil,
		http.StatusConflict,
	)

	initResponse := performJSONRequest(
		t,
		server.Handler(),
		http.MethodPost,
		"/api/v1/auth/init",
		map[string]any{
			"key_hash": "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
		},
		http.StatusCreated,
	)
	if initResponse["initialized"] != true {
		t.Fatalf("unexpected init response: %#v", initResponse)
	}

	rawAuth, err := os.ReadFile(authPath)
	if err != nil {
		t.Fatalf("read auth file: %v", err)
	}
	if string(rawAuth) != `{"key_hash":"0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"}` {
		t.Fatalf("unexpected auth.json content: %s", rawAuth)
	}

	stateAfterInit := performJSONRequest(
		t,
		server.Handler(),
		http.MethodGet,
		"/api/v1/auth/state",
		nil,
		http.StatusOK,
	)
	if stateAfterInit["initialized"] != true {
		t.Fatalf("unexpected state after init: %#v", stateAfterInit)
	}

	performJSONRequest(
		t,
		server.Handler(),
		http.MethodPost,
		"/api/v1/auth/init",
		map[string]any{
			"key_hash": "fedcba9876543210fedcba9876543210fedcba9876543210fedcba9876543210",
		},
		http.StatusConflict,
	)

	challenge := performJSONRequest(
		t,
		server.Handler(),
		http.MethodPost,
		"/api/v1/auth/challenge",
		nil,
		http.StatusOK,
	)
	if len(challenge["challenge_id"].(string)) == 0 || len(challenge["salt"].(string)) == 0 {
		t.Fatalf("challenge payload must include identifiers: %#v", challenge)
	}
	if len(challenge["expires_at"].(string)) == 0 {
		t.Fatalf("challenge payload must include expires_at: %#v", challenge)
	}

	performJSONRequest(
		t,
		server.Handler(),
		http.MethodGet,
		"/api/v1/proxy-groups",
		nil,
		http.StatusUnauthorized,
	)
}

func newTestStore(t *testing.T) *storage.SQL {
	t.Helper()

	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "api.sqlite"))
	if err != nil {
		t.Fatalf("open sqlite database: %v", err)
	}

	store, err := storage.NewSQLWithConn(t.Name(), db)
	if err != nil {
		t.Fatalf("create store: %v", err)
	}
	t.Cleanup(func() {
		store.Close()
	})

	for _, statement := range []string{
		`
CREATE TABLE proxy_groups (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	name TEXT NOT NULL UNIQUE,
	token_id TEXT NOT NULL UNIQUE,
	token_hash TEXT NOT NULL,
	enabled INTEGER NOT NULL DEFAULT 1,
	rate_limit INTEGER NOT NULL DEFAULT 0,
	client_access_mode TEXT NOT NULL DEFAULT 'disabled',
	tunnel_access_mode TEXT NOT NULL DEFAULT 'disabled',
	created_at TEXT NOT NULL,
	updated_at TEXT NOT NULL
)`,
		`
CREATE TABLE group_client_ip_rules (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	group_id INTEGER NOT NULL,
	action TEXT NOT NULL,
	cidr TEXT NOT NULL,
	comment TEXT NOT NULL DEFAULT '',
	created_at TEXT NOT NULL
)`,
		`
CREATE TABLE group_tunnel_ip_rules (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	group_id INTEGER NOT NULL,
	action TEXT NOT NULL,
	cidr TEXT NOT NULL,
	comment TEXT NOT NULL DEFAULT '',
	created_at TEXT NOT NULL
)`,
		`
CREATE TABLE tunnels (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	group_id INTEGER NOT NULL,
	name TEXT NOT NULL,
	protocol TEXT NOT NULL,
	remote_type TEXT NOT NULL,
	remote_start INTEGER NOT NULL,
	remote_end INTEGER NOT NULL,
	local_host TEXT NOT NULL,
	local_start INTEGER NOT NULL,
	local_end INTEGER NOT NULL,
	enabled INTEGER NOT NULL DEFAULT 1,
	rate_limit INTEGER NOT NULL DEFAULT 0,
	capture_enabled INTEGER NOT NULL DEFAULT 0,
	created_at TEXT NOT NULL,
	updated_at TEXT NOT NULL,
	UNIQUE(group_id, name)
)`,
	} {
		if _, err := store.Exec(statement); err != nil {
			t.Fatalf("bootstrap api test schema: %v", err)
		}
	}

	return store
}

func newTestAuthManager(t *testing.T, initialize bool) *authn.Manager {
	t.Helper()

	manager, err := authn.NewManager(authn.Options{
		Path: filepath.Join(t.TempDir(), "auth.json"),
	})
	if err != nil {
		t.Fatalf("new auth manager: %v", err)
	}
	if initialize {
		if err := manager.Initialize("0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"); err != nil {
			t.Fatalf("initialize auth manager: %v", err)
		}
	}
	return manager
}

func performJSONRequest(
	t *testing.T,
	handler http.Handler,
	method string,
	target string,
	body any,
	wantStatus int,
) map[string]any {
	t.Helper()

	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal request body: %v", err)
		}
		reader = bytes.NewReader(raw)
	}

	request := httptest.NewRequest(method, target, reader)
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)

	if recorder.Code != wantStatus {
		t.Fatalf("unexpected status for %s %s: got %d want %d body=%s", method, target, recorder.Code, wantStatus, recorder.Body.String())
	}

	if recorder.Body.Len() == 0 {
		return map[string]any{}
	}

	var payload map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	return payload
}
