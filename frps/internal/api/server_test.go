package api

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/zightch/frp/frps/internal/storage"
	_ "github.com/zightch/frp/frps/internal/storage/drivers"
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

func TestManagementUIAndCRUD(t *testing.T) {
	store := newTestStore(t)
	server := NewServer(
		Options{
			Addr:              "127.0.0.1:7500",
			ReadHeaderTimeout: 5 * time.Second,
			Store:             store,
		},
		slog.New(slog.NewTextHandler(io.Discard, nil)),
		"test",
	)

	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("unexpected index status: %d", recorder.Code)
	}
	if body := recorder.Body.String(); !bytes.Contains([]byte(body), []byte("frps 极简管理页")) {
		t.Fatalf("unexpected index body: %s", body)
	}
	if body := recorder.Body.String(); bytes.Contains([]byte(body), []byte("最大客户端数")) ||
		bytes.Contains([]byte(body), []byte("max_clients")) ||
		bytes.Contains([]byte(body), []byte("maxclient")) {
		t.Fatalf("group max clients must not be configurable in webui: %s", body)
	}

	createGroup := performJSONRequest(
		t,
		server.Handler(),
		http.MethodPost,
		"/api/v1/proxy-groups",
		map[string]any{
			"name":        "group-a",
			"enabled":     true,
			"max_clients": 2,
		},
		http.StatusCreated,
	)

	groupItem := createGroup["item"].(map[string]any)
	groupID := int64(groupItem["id"].(float64))
	if _, ok := groupItem["max_clients"]; ok {
		t.Fatalf("proxy group response must not expose max_clients, got %#v", groupItem)
	}
	firstToken := createGroup["token"].(string)
	if len(firstToken) != 96 {
		t.Fatalf("unexpected token length: %d", len(firstToken))
	}

	groupRow, err := store.QueryOne(
		"SELECT token_id, token_hash FROM proxy_groups WHERE id = ?",
		groupID,
	)
	if err != nil {
		t.Fatalf("query proxy group: %v", err)
	}
	tokenID := rowString(groupRow, "token_id")
	tokenHash := rowString(groupRow, "token_hash")
	if tokenID == "" || tokenHash == "" {
		t.Fatalf("expected token columns, got %#v", groupRow)
	}
	if firstToken[:32] != tokenID {
		t.Fatalf("token prefix does not match token_id: %s vs %s", firstToken[:32], tokenID)
	}
	if tokenHash == firstToken {
		t.Fatal("database must not store plaintext token")
	}

	listGroups := performJSONRequest(
		t,
		server.Handler(),
		http.MethodGet,
		"/api/v1/proxy-groups",
		nil,
		http.StatusOK,
	)
	groupItems := listGroups["items"].([]any)
	if len(groupItems) != 1 {
		t.Fatalf("unexpected group count: %d", len(groupItems))
	}
	if _, ok := groupItems[0].(map[string]any)["token"]; ok {
		t.Fatal("group list must not expose plaintext token")
	}

	updateGroup := performJSONRequest(
		t,
		server.Handler(),
		http.MethodPatch,
		"/api/v1/proxy-groups/1",
		map[string]any{
			"name":        "group-b",
			"enabled":     false,
			"max_clients": 9,
		},
		http.StatusOK,
	)
	if _, ok := updateGroup["item"].(map[string]any)["max_clients"]; ok {
		t.Fatalf("updated proxy group response must not expose max_clients, got %#v", updateGroup["item"])
	}

	resetToken := performJSONRequest(
		t,
		server.Handler(),
		http.MethodPost,
		"/api/v1/proxy-groups/1/token",
		nil,
		http.StatusOK,
	)
	secondToken := resetToken["token"].(string)
	if secondToken == firstToken {
		t.Fatal("expected token reset to issue a new token")
	}

	createTunnel := performJSONRequest(
		t,
		server.Handler(),
		http.MethodPost,
		"/api/v1/tunnels",
		map[string]any{
			"group_id":     groupID,
			"name":         "ssh",
			"protocol":     "tcp",
			"remote_type":  "single",
			"remote_start": 20000,
			"remote_end":   20000,
			"local_host":   "127.0.0.1",
			"local_start":  22,
			"local_end":    22,
			"enabled":      true,
		},
		http.StatusCreated,
	)
	tunnelItem := createTunnel["item"].(map[string]any)
	tunnelID := int64(tunnelItem["id"].(float64))

	performJSONRequest(
		t,
		server.Handler(),
		http.MethodPatch,
		"/api/v1/tunnels/1",
		map[string]any{
			"group_id":     groupID,
			"name":         "ssh-updated",
			"protocol":     "tcp",
			"remote_type":  "single",
			"remote_start": 21000,
			"remote_end":   21000,
			"local_host":   "127.0.0.1",
			"local_start":  2222,
			"local_end":    2222,
			"enabled":      false,
		},
		http.StatusOK,
	)

	listTunnels := performJSONRequest(
		t,
		server.Handler(),
		http.MethodGet,
		"/api/v1/tunnels",
		nil,
		http.StatusOK,
	)
	tunnelItems := listTunnels["items"].([]any)
	if len(tunnelItems) != 1 {
		t.Fatalf("unexpected tunnel count: %d", len(tunnelItems))
	}
	if tunnelItems[0].(map[string]any)["group_name"] != "group-b" {
		t.Fatalf("unexpected tunnel group name: %#v", tunnelItems[0].(map[string]any)["group_name"])
	}

	performJSONRequest(
		t,
		server.Handler(),
		http.MethodDelete,
		"/api/v1/tunnels/1",
		nil,
		http.StatusOK,
	)
	performJSONRequest(
		t,
		server.Handler(),
		http.MethodDelete,
		"/api/v1/proxy-groups/1",
		nil,
		http.StatusOK,
	)

	if tunnelID == 0 {
		t.Fatal("expected tunnel id")
	}
	if _, err := store.QueryOne("SELECT id FROM tunnels WHERE id = ?", tunnelID); err == nil {
		t.Fatal("expected tunnel to be deleted")
	}
	if _, err := store.QueryOne("SELECT id FROM proxy_groups WHERE id = ?", groupID); err == nil {
		t.Fatal("expected proxy group to be deleted")
	}
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
