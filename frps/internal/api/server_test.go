package api

import (
	"bytes"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	authn "github.com/zightch/frp/frps/internal/auth"
	"github.com/zightch/frp/frps/internal/storage"
	_ "github.com/zightch/frp/frps/internal/storage/drivers"
	"github.com/zightch/frp/frps/internal/system"
)

func TestHealthEndpoint(t *testing.T) {
	server, err := NewServer(
		Options{
			Addr:              "127.0.0.1:7500",
			ReadHeaderTimeout: 5 * time.Second,
			Auth:              newTestAuthManager(t, false),
		},
		slog.New(slog.NewTextHandler(io.Discard, nil)),
		"test",
	)
	if err != nil {
		t.Fatalf("new server: %v", err)
	}

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

func TestAuthInitializationLoginAndSessionEndpoints(t *testing.T) {
	store := newTestStore(t)
	authPath := filepath.Join(t.TempDir(), "auth.json")
	manager, err := authn.NewManager(authn.Options{
		Path:          authPath,
		WatchInterval: 5 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("new auth manager: %v", err)
	}
	t.Cleanup(func() {
		_ = manager.Close()
	})

	server, err := NewServer(
		Options{
			Addr:              "127.0.0.1:7500",
			ReadHeaderTimeout: 5 * time.Second,
			Store:             store,
			Auth:              manager,
		},
		slog.New(slog.NewTextHandler(io.Discard, nil)),
		"test",
	)
	if err != nil {
		t.Fatalf("new server: %v", err)
	}

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
	if state["authenticated"] != false {
		t.Fatalf("unexpected authentication state before init: %#v", state)
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
	if stateAfterInit["authenticated"] != false {
		t.Fatalf("unexpected authentication state after init: %#v", stateAfterInit)
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

	login := performRequest(
		t,
		server.Handler(),
		http.MethodPost,
		"/api/v1/auth/login",
		map[string]any{
			"challenge_id": challenge["challenge_id"],
			"proof":        buildManagementProof("0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef", challenge["salt"].(string)),
		},
		http.StatusOK,
	)
	if login.JSON["authenticated"] != true {
		t.Fatalf("unexpected login response: %#v", login.JSON)
	}
	sessionCookie := findCookie(login.Cookies, managementSessionCookieName)
	if sessionCookie == nil {
		t.Fatal("login must set a management session cookie")
	}

	stateWithSession := performRequest(
		t,
		server.Handler(),
		http.MethodGet,
		"/api/v1/auth/state",
		nil,
		http.StatusOK,
		sessionCookie,
	)
	if stateWithSession.JSON["authenticated"] != true {
		t.Fatalf("unexpected state with session: %#v", stateWithSession.JSON)
	}

	sessionState := performRequest(
		t,
		server.Handler(),
		http.MethodGet,
		"/api/v1/auth/session",
		nil,
		http.StatusOK,
		sessionCookie,
	)
	if sessionState.JSON["authenticated"] != true {
		t.Fatalf("unexpected session payload: %#v", sessionState.JSON)
	}

	protected := performRequest(
		t,
		server.Handler(),
		http.MethodGet,
		"/api/v1/proxy-groups",
		nil,
		http.StatusOK,
		sessionCookie,
	)
	items, ok := protected.JSON["items"].([]any)
	if !ok || len(items) != 0 {
		t.Fatalf("unexpected protected payload: %#v", protected.JSON)
	}

	performJSONRequest(
		t,
		server.Handler(),
		http.MethodGet,
		"/api/v1/proxy-groups",
		nil,
		http.StatusUnauthorized,
	)

	logout := performRequest(
		t,
		server.Handler(),
		http.MethodPost,
		"/api/v1/auth/logout",
		nil,
		http.StatusOK,
		sessionCookie,
	)
	if logout.JSON["logged_out"] != true {
		t.Fatalf("unexpected logout payload: %#v", logout.JSON)
	}
	clearedCookie := findCookie(logout.Cookies, managementSessionCookieName)
	if clearedCookie == nil || clearedCookie.MaxAge != -1 {
		t.Fatalf("logout must clear the session cookie: %#v", logout.Cookies)
	}

	performRequest(
		t,
		server.Handler(),
		http.MethodGet,
		"/api/v1/auth/session",
		nil,
		http.StatusUnauthorized,
		sessionCookie,
	)
}

func TestAuthStateResetsAfterAuthFileDeletion(t *testing.T) {
	store := newTestStore(t)
	authPath := filepath.Join(t.TempDir(), "auth.json")
	manager, err := authn.NewManager(authn.Options{
		Path:          authPath,
		WatchInterval: 5 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("new auth manager: %v", err)
	}
	t.Cleanup(func() {
		_ = manager.Close()
	})

	server, err := NewServer(
		Options{
			Addr:              "127.0.0.1:7500",
			ReadHeaderTimeout: 5 * time.Second,
			Store:             store,
			Auth:              manager,
		},
		slog.New(slog.NewTextHandler(io.Discard, nil)),
		"test",
	)
	if err != nil {
		t.Fatalf("new server: %v", err)
	}

	keyHash := "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	if err := manager.Initialize(keyHash); err != nil {
		t.Fatalf("initialize auth manager: %v", err)
	}

	challenge, err := manager.IssueChallenge()
	if err != nil {
		t.Fatalf("issue challenge: %v", err)
	}

	_, token, err := manager.Login(challenge.ID, buildManagementProof(keyHash, challenge.Salt))
	if err != nil {
		t.Fatalf("login: %v", err)
	}

	sessionCookie := &http.Cookie{
		Name:  managementSessionCookieName,
		Value: token,
		Path:  "/",
	}

	if err := os.Remove(authPath); err != nil {
		t.Fatalf("remove auth file: %v", err)
	}

	resetObserved := false
	deadline := time.Now().Add(250 * time.Millisecond)
	for time.Now().Before(deadline) {
		state := performRequest(
			t,
			server.Handler(),
			http.MethodGet,
			"/api/v1/auth/state",
			nil,
			http.StatusOK,
			sessionCookie,
		)
		if state.JSON["initialized"] == false {
			if state.JSON["authenticated"] != false {
				t.Fatalf("unexpected auth state after deletion: %#v", state.JSON)
			}
			clearedCookie := findCookie(state.Cookies, managementSessionCookieName)
			if clearedCookie == nil || clearedCookie.MaxAge != -1 {
				t.Fatalf("state endpoint must clear stale management cookie after auth reset: %#v", state.Cookies)
			}
			resetObserved = true
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if !resetObserved {
		t.Fatal("auth state did not reset after auth.json deletion")
	}

	protected := performRequest(
		t,
		server.Handler(),
		http.MethodGet,
		"/api/v1/proxy-groups",
		nil,
		http.StatusConflict,
		sessionCookie,
	)
	clearedCookie := findCookie(protected.Cookies, managementSessionCookieName)
	if clearedCookie == nil || clearedCookie.MaxAge != -1 {
		t.Fatalf("protected endpoint must clear stale management cookie after auth reset: %#v", protected.Cookies)
	}

	reinit := performJSONRequest(
		t,
		server.Handler(),
		http.MethodPost,
		"/api/v1/auth/init",
		map[string]any{
			"key_hash": "fedcba9876543210fedcba9876543210fedcba9876543210fedcba9876543210",
		},
		http.StatusCreated,
	)
	if reinit["initialized"] != true {
		t.Fatalf("unexpected re-init response: %#v", reinit)
	}
}

func TestManagementKeySmokeFlow(t *testing.T) {
	store := newTestStore(t)
	manager, err := authn.NewManager(authn.Options{
		Path:          filepath.Join(t.TempDir(), "auth.json"),
		WatchInterval: 5 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("new auth manager: %v", err)
	}
	t.Cleanup(func() {
		_ = manager.Close()
	})

	server, err := NewServer(
		Options{
			Addr:              "127.0.0.1:7500",
			ReadHeaderTimeout: 5 * time.Second,
			Store:             store,
			Auth:              manager,
		},
		slog.New(slog.NewTextHandler(io.Discard, nil)),
		"test",
	)
	if err != nil {
		t.Fatalf("new server: %v", err)
	}

	httpServer := httptest.NewServer(server.Handler())
	defer httpServer.Close()

	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatalf("create cookie jar: %v", err)
	}
	client := &http.Client{Jar: jar}

	state := performClientRequest(
		t,
		client,
		http.MethodGet,
		httpServer.URL+"/api/v1/auth/state",
		nil,
		http.StatusOK,
	)
	if state.JSON["initialized"] != false || state.JSON["authenticated"] != false {
		t.Fatalf("unexpected pre-init state: %#v", state.JSON)
	}

	performClientRequest(
		t,
		client,
		http.MethodGet,
		httpServer.URL+"/api/v1/proxy-groups",
		nil,
		http.StatusConflict,
	)

	managementSecret := "smoke-test-management-secret"
	keyHash := sha256Hex(managementSecret)

	initResult := performClientRequest(
		t,
		client,
		http.MethodPost,
		httpServer.URL+"/api/v1/auth/init",
		map[string]any{"key_hash": keyHash},
		http.StatusCreated,
	)
	if initResult.JSON["initialized"] != true {
		t.Fatalf("unexpected init response: %#v", initResult.JSON)
	}

	challenge := performClientRequest(
		t,
		client,
		http.MethodPost,
		httpServer.URL+"/api/v1/auth/challenge",
		nil,
		http.StatusOK,
	)
	challengeID, _ := challenge.JSON["challenge_id"].(string)
	salt, _ := challenge.JSON["salt"].(string)
	if challengeID == "" || salt == "" {
		t.Fatalf("challenge payload must include challenge_id and salt: %#v", challenge.JSON)
	}

	login := performClientRequest(
		t,
		client,
		http.MethodPost,
		httpServer.URL+"/api/v1/auth/login",
		map[string]any{
			"challenge_id": challengeID,
			"proof":        buildManagementProof(keyHash, salt),
		},
		http.StatusOK,
	)
	if login.JSON["authenticated"] != true {
		t.Fatalf("unexpected login response: %#v", login.JSON)
	}
	if findCookie(login.Cookies, managementSessionCookieName) == nil {
		t.Fatal("login must set a management session cookie")
	}

	session := performClientRequest(
		t,
		client,
		http.MethodGet,
		httpServer.URL+"/api/v1/auth/session",
		nil,
		http.StatusOK,
	)
	if session.JSON["authenticated"] != true {
		t.Fatalf("unexpected session response: %#v", session.JSON)
	}

	createdGroup := performClientRequest(
		t,
		client,
		http.MethodPost,
		httpServer.URL+"/api/v1/proxy-groups",
		map[string]any{
			"name":         "smoke-group",
			"effective_ip": "0.0.0.0",
			"enabled":      true,
		},
		http.StatusCreated,
	)
	item, ok := createdGroup.JSON["item"].(map[string]any)
	if !ok {
		t.Fatalf("unexpected proxy group payload: %#v", createdGroup.JSON)
	}
	groupID, ok := item["id"].(float64)
	if !ok || int64(groupID) <= 0 {
		t.Fatalf("unexpected proxy group id: %#v", item)
	}
	if _, ok := createdGroup.JSON["token"].(string); !ok {
		t.Fatalf("expected proxy group token in response: %#v", createdGroup.JSON)
	}
	if item["effective_ip"] != "0.0.0.0" {
		t.Fatalf("unexpected effective_ip in create response: %#v", item)
	}

	protected := performClientRequest(
		t,
		client,
		http.MethodGet,
		httpServer.URL+"/api/v1/proxy-groups",
		nil,
		http.StatusOK,
	)
	items, ok := protected.JSON["items"].([]any)
	if !ok || len(items) != 1 {
		t.Fatalf("unexpected proxy groups payload after create: %#v", protected.JSON)
	}

	logout := performClientRequest(
		t,
		client,
		http.MethodPost,
		httpServer.URL+"/api/v1/auth/logout",
		nil,
		http.StatusOK,
	)
	if logout.JSON["logged_out"] != true {
		t.Fatalf("unexpected logout response: %#v", logout.JSON)
	}

	performClientRequest(
		t,
		client,
		http.MethodGet,
		httpServer.URL+"/api/v1/auth/session",
		nil,
		http.StatusUnauthorized,
	)
}

func TestProxyGroupEffectiveIPCRUDValidation(t *testing.T) {
	store := newTestStore(t)
	manager := newTestAuthManager(t, true)

	server, err := NewServer(
		Options{
			Addr:              "127.0.0.1:7500",
			ReadHeaderTimeout: 5 * time.Second,
			Store:             store,
			Network: staticSnapshotReader{
				snapshot: system.Snapshot{
					AvailableIPs: []system.IPAddress{
						{Addr: "127.0.0.1", Family: system.FamilyIPv4},
					},
				},
			},
			Auth: manager,
		},
		slog.New(slog.NewTextHandler(io.Discard, nil)),
		"test",
	)
	if err != nil {
		t.Fatalf("new server: %v", err)
	}

	sessionCookie := authenticatedManagementCookie(t, manager)

	created := performRequest(
		t,
		server.Handler(),
		http.MethodPost,
		"/api/v1/proxy-groups",
		map[string]any{
			"name":         "group-a",
			"effective_ip": "127.0.0.1",
		},
		http.StatusCreated,
		sessionCookie,
	)
	item, ok := created.JSON["item"].(map[string]any)
	if !ok {
		t.Fatalf("unexpected create payload: %#v", created.JSON)
	}
	groupID, ok := item["id"].(float64)
	if !ok || int64(groupID) <= 0 {
		t.Fatalf("unexpected create group id: %#v", item)
	}
	if item["effective_ip"] != "127.0.0.1" {
		t.Fatalf("unexpected create effective_ip: %#v", item)
	}

	performRequest(
		t,
		server.Handler(),
		http.MethodPost,
		"/api/v1/proxy-groups",
		map[string]any{
			"name":         "group-b",
			"effective_ip": "10.0.0.9",
		},
		http.StatusBadRequest,
		sessionCookie,
	)

	updated := performRequest(
		t,
		server.Handler(),
		http.MethodPatch,
		"/api/v1/proxy-groups/"+jsonNumberString(groupID),
		map[string]any{
			"name":         "group-a-renamed",
			"effective_ip": "0.0.0.0",
			"enabled":      false,
		},
		http.StatusOK,
		sessionCookie,
	)
	updatedItem, ok := updated.JSON["item"].(map[string]any)
	if !ok {
		t.Fatalf("unexpected update payload: %#v", updated.JSON)
	}
	if updatedItem["effective_ip"] != "0.0.0.0" {
		t.Fatalf("unexpected update effective_ip: %#v", updatedItem)
	}
	if updatedItem["enabled"] != false {
		t.Fatalf("unexpected update enabled flag: %#v", updatedItem)
	}

	listed := performRequest(
		t,
		server.Handler(),
		http.MethodGet,
		"/api/v1/proxy-groups",
		nil,
		http.StatusOK,
		sessionCookie,
	)
	items, ok := listed.JSON["items"].([]any)
	if !ok || len(items) != 1 {
		t.Fatalf("unexpected list payload: %#v", listed.JSON)
	}
	listItem, ok := items[0].(map[string]any)
	if !ok {
		t.Fatalf("unexpected list item payload: %#v", items[0])
	}
	if listItem["effective_ip"] != "0.0.0.0" {
		t.Fatalf("unexpected list effective_ip: %#v", listItem)
	}

	reset := performRequest(
		t,
		server.Handler(),
		http.MethodPost,
		"/api/v1/proxy-groups/"+jsonNumberString(groupID)+"/token",
		nil,
		http.StatusOK,
		sessionCookie,
	)
	resetItem, ok := reset.JSON["item"].(map[string]any)
	if !ok {
		t.Fatalf("unexpected token reset payload: %#v", reset.JSON)
	}
	if resetItem["effective_ip"] != "0.0.0.0" {
		t.Fatalf("unexpected token reset effective_ip: %#v", resetItem)
	}
}

func TestWebUIHandlerServesStaticFilesAndSPAFallback(t *testing.T) {
	server, err := NewServer(
		Options{
			Addr:              "127.0.0.1:7500",
			ReadHeaderTimeout: 5 * time.Second,
			Auth:              newTestAuthManager(t, false),
			WebUIDistDir:      newTestWebUIDist(t),
		},
		slog.New(slog.NewTextHandler(io.Discard, nil)),
		"test",
	)
	if err != nil {
		t.Fatalf("new server: %v", err)
	}

	indexRecorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(indexRecorder, httptest.NewRequest(http.MethodGet, "/", nil))
	if indexRecorder.Code != http.StatusOK {
		t.Fatalf("unexpected index status: %d body=%s", indexRecorder.Code, indexRecorder.Body.String())
	}
	if !bytes.Contains(indexRecorder.Body.Bytes(), []byte(`<div id="app"></div>`)) {
		t.Fatalf("unexpected index body: %s", indexRecorder.Body.String())
	}

	assetRecorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(assetRecorder, httptest.NewRequest(http.MethodGet, "/assets/app.js", nil))
	if assetRecorder.Code != http.StatusOK {
		t.Fatalf("unexpected asset status: %d body=%s", assetRecorder.Code, assetRecorder.Body.String())
	}
	if assetRecorder.Body.String() != "console.log('webui-ok');" {
		t.Fatalf("unexpected asset body: %s", assetRecorder.Body.String())
	}

	spaRecorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(spaRecorder, httptest.NewRequest(http.MethodGet, "/login", nil))
	if spaRecorder.Code != http.StatusOK {
		t.Fatalf("unexpected spa status: %d body=%s", spaRecorder.Code, spaRecorder.Body.String())
	}
	if !bytes.Contains(spaRecorder.Body.Bytes(), []byte(`<div id="app"></div>`)) {
		t.Fatalf("unexpected spa body: %s", spaRecorder.Body.String())
	}

	missingAssetRecorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(missingAssetRecorder, httptest.NewRequest(http.MethodGet, "/assets/missing.js", nil))
	if missingAssetRecorder.Code != http.StatusNotFound {
		t.Fatalf("unexpected missing asset status: %d body=%s", missingAssetRecorder.Code, missingAssetRecorder.Body.String())
	}
}

func TestNewServerRejectsMissingWebUIDistDir(t *testing.T) {
	_, err := NewServer(
		Options{
			Addr:              "127.0.0.1:7500",
			ReadHeaderTimeout: 5 * time.Second,
			Auth:              newTestAuthManager(t, false),
			WebUIDistDir:      filepath.Join(t.TempDir(), "missing-dist"),
		},
		slog.New(slog.NewTextHandler(io.Discard, nil)),
		"test",
	)
	if err == nil {
		t.Fatal("expected missing webui dist dir to fail")
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
	effective_ip TEXT NOT NULL,
	enabled INTEGER NOT NULL DEFAULT 1,
	rate_limit INTEGER NOT NULL DEFAULT 0,
	created_at TEXT NOT NULL,
	updated_at TEXT NOT NULL
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

func newTestWebUIDist(t *testing.T) string {
	t.Helper()

	distDir := filepath.Join(t.TempDir(), "dist")
	assetsDir := filepath.Join(distDir, "assets")
	if err := os.MkdirAll(assetsDir, 0o755); err != nil {
		t.Fatalf("create webui dist dir: %v", err)
	}

	indexHTML := `<!doctype html><html lang="zh-CN"><body><div id="app"></div><script type="module" src="/assets/app.js"></script></body></html>`
	if err := os.WriteFile(filepath.Join(distDir, "index.html"), []byte(indexHTML), 0o644); err != nil {
		t.Fatalf("write webui index: %v", err)
	}
	if err := os.WriteFile(filepath.Join(assetsDir, "app.js"), []byte("console.log('webui-ok');"), 0o644); err != nil {
		t.Fatalf("write webui asset: %v", err)
	}

	return distDir
}

func newTestAuthManager(t *testing.T, initialize bool) *authn.Manager {
	t.Helper()

	manager, err := authn.NewManager(authn.Options{
		Path:          filepath.Join(t.TempDir(), "auth.json"),
		WatchInterval: 5 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("new auth manager: %v", err)
	}
	t.Cleanup(func() {
		_ = manager.Close()
	})
	if initialize {
		if err := manager.Initialize("0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"); err != nil {
			t.Fatalf("initialize auth manager: %v", err)
		}
	}
	return manager
}

func authenticatedManagementCookie(t *testing.T, manager *authn.Manager) *http.Cookie {
	t.Helper()

	challenge, err := manager.IssueChallenge()
	if err != nil {
		t.Fatalf("issue challenge: %v", err)
	}

	_, token, err := manager.Login(
		challenge.ID,
		buildManagementProof(
			"0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
			challenge.Salt,
		),
	)
	if err != nil {
		t.Fatalf("login manager: %v", err)
	}

	return &http.Cookie{
		Name:  managementSessionCookieName,
		Value: token,
		Path:  "/",
	}
}

func jsonNumberString(value float64) string {
	return strconv.FormatInt(int64(value), 10)
}

type testResponse struct {
	JSON    map[string]any
	Cookies []*http.Cookie
}

func performRequest(
	t *testing.T,
	handler http.Handler,
	method string,
	target string,
	body any,
	wantStatus int,
	cookies ...*http.Cookie,
) testResponse {
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
	for _, cookie := range cookies {
		request.AddCookie(cookie)
	}

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)

	if recorder.Code != wantStatus {
		t.Fatalf("unexpected status for %s %s: got %d want %d body=%s", method, target, recorder.Code, wantStatus, recorder.Body.String())
	}

	response := testResponse{
		JSON:    map[string]any{},
		Cookies: recorder.Result().Cookies(),
	}
	if recorder.Body.Len() == 0 {
		return response
	}

	if err := json.Unmarshal(recorder.Body.Bytes(), &response.JSON); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	return response
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
	return performRequest(t, handler, method, target, body, wantStatus).JSON
}

func performClientRequest(
	t *testing.T,
	client *http.Client,
	method string,
	target string,
	body any,
	wantStatus int,
) testResponse {
	t.Helper()

	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal request body: %v", err)
		}
		reader = bytes.NewReader(raw)
	}

	request, err := http.NewRequest(method, target, reader)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}

	response, err := client.Do(request)
	if err != nil {
		t.Fatalf("perform request: %v", err)
	}
	defer response.Body.Close()

	rawBody, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("read response body: %v", err)
	}

	if response.StatusCode != wantStatus {
		t.Fatalf("unexpected status for %s %s: got %d want %d body=%s", method, target, response.StatusCode, wantStatus, string(rawBody))
	}

	result := testResponse{
		JSON:    map[string]any{},
		Cookies: response.Cookies(),
	}
	if len(rawBody) == 0 {
		return result
	}

	if err := json.Unmarshal(rawBody, &result.JSON); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	return result
}

func buildManagementProof(keyHash, salt string) string {
	sum := sha256.Sum256([]byte(keyHash + salt))
	return hex.EncodeToString(sum[:])
}

func sha256Hex(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

func findCookie(cookies []*http.Cookie, name string) *http.Cookie {
	for _, cookie := range cookies {
		if cookie.Name == name {
			return cookie
		}
	}
	return nil
}

type staticSnapshotReader struct {
	snapshot system.Snapshot
}

func (r staticSnapshotReader) Current() system.Snapshot {
	return r.snapshot
}
