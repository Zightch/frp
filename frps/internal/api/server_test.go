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
	"strings"
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
			Addr:              "127.0.0.1:7080",
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
			Addr:              "127.0.0.1:7080",
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
			Addr:              "127.0.0.1:7080",
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
			Addr:              "127.0.0.1:7080",
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
	key, ok := createdGroup.JSON["key"].(string)
	if !ok {
		t.Fatalf("expected proxy group key in response: %#v", createdGroup.JSON)
	}
	clientID, ok := item["client_id"].(string)
	if !ok {
		t.Fatalf("expected proxy group client_id in response item: %#v", createdGroup.JSON)
	}
	if !strings.HasPrefix(key, clientID) {
		t.Fatalf("expected key to start with client_id: %#v", createdGroup.JSON)
	}
	if len(key) != len(clientID)+64 {
		t.Fatalf("expected key to include a 64-char client_secret suffix: %#v", createdGroup.JSON)
	}
	if item["effective_ip"] != "0.0.0.0" {
		t.Fatalf("unexpected effective_ip in create response: %#v", item)
	}
	if item["status"] != proxyGroupStatusEnabled {
		t.Fatalf("unexpected create status: %#v", item)
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
			Addr:              "127.0.0.1:7080",
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
	if item["status"] != proxyGroupStatusEnabled {
		t.Fatalf("unexpected create status: %#v", item)
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
	if updatedItem["status"] != proxyGroupStatusDisabled {
		t.Fatalf("unexpected update status: %#v", updatedItem)
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
	if listItem["status"] != proxyGroupStatusDisabled {
		t.Fatalf("unexpected list status: %#v", listItem)
	}

	reset := performRequest(
		t,
		server.Handler(),
		http.MethodPost,
		"/api/v1/proxy-groups/"+jsonNumberString(groupID)+"/key",
		nil,
		http.StatusOK,
		sessionCookie,
	)
	resetItem, ok := reset.JSON["item"].(map[string]any)
	if !ok {
		t.Fatalf("unexpected credential rotation payload: %#v", reset.JSON)
	}
	resetKey, ok := reset.JSON["key"].(string)
	if !ok {
		t.Fatalf("expected key in rotation payload: %#v", reset.JSON)
	}
	if !strings.HasPrefix(resetKey, resetItem["client_id"].(string)) {
		t.Fatalf("expected rotated key to start with client_id: %#v", reset.JSON)
	}
	if resetItem["effective_ip"] != "0.0.0.0" {
		t.Fatalf("unexpected credential rotation effective_ip: %#v", resetItem)
	}
	if resetItem["status"] != proxyGroupStatusDisabled {
		t.Fatalf("unexpected credential rotation status: %#v", resetItem)
	}
}

func TestProxyGroupPatchAllowsPartialUpdateAndRejectsEmptyPatch(t *testing.T) {
	store := newTestStore(t)
	manager := newTestAuthManager(t, true)

	server, err := NewServer(
		Options{
			Addr:              "127.0.0.1:7080",
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
			"enabled":      true,
		},
		http.StatusCreated,
		sessionCookie,
	)
	createdItem, ok := created.JSON["item"].(map[string]any)
	if !ok {
		t.Fatalf("unexpected create payload: %#v", created.JSON)
	}
	groupID, ok := createdItem["id"].(float64)
	if !ok || int64(groupID) <= 0 {
		t.Fatalf("unexpected create group id: %#v", createdItem)
	}

	performRequest(
		t,
		server.Handler(),
		http.MethodPatch,
		"/api/v1/proxy-groups/"+jsonNumberString(groupID),
		map[string]any{},
		http.StatusBadRequest,
		sessionCookie,
	)

	updated := performRequest(
		t,
		server.Handler(),
		http.MethodPatch,
		"/api/v1/proxy-groups/"+jsonNumberString(groupID),
		map[string]any{
			"name": "group-a-renamed",
		},
		http.StatusOK,
		sessionCookie,
	)
	updatedItem, ok := updated.JSON["item"].(map[string]any)
	if !ok {
		t.Fatalf("unexpected update payload: %#v", updated.JSON)
	}
	if updatedItem["name"] != "group-a-renamed" {
		t.Fatalf("unexpected update name: %#v", updatedItem)
	}
	if updatedItem["effective_ip"] != "127.0.0.1" {
		t.Fatalf("unexpected preserved effective_ip: %#v", updatedItem)
	}
	if updatedItem["enabled"] != true {
		t.Fatalf("unexpected preserved enabled: %#v", updatedItem)
	}
	if updatedItem["status"] != proxyGroupStatusEnabled {
		t.Fatalf("unexpected preserved status: %#v", updatedItem)
	}
}

func TestManagementMutationsRefreshAffectedGroups(t *testing.T) {
	store := newTestStore(t)
	manager := newTestAuthManager(t, true)
	refresher := &recordingGroupRefresher{}

	server, err := NewServer(
		Options{
			Addr:              "127.0.0.1:7080",
			ReadHeaderTimeout: 5 * time.Second,
			Store:             store,
			Network: staticSnapshotReader{
				snapshot: system.Snapshot{
					AvailableIPs: []system.IPAddress{
						{Addr: "127.0.0.1", Family: system.FamilyIPv4},
					},
				},
			},
			RuntimeRefresher: refresher,
			Auth:             manager,
		},
		slog.New(slog.NewTextHandler(io.Discard, nil)),
		"test",
	)
	if err != nil {
		t.Fatalf("new server: %v", err)
	}

	sessionCookie := authenticatedManagementCookie(t, manager)

	groupA := performRequest(
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
	groupAID := int64(groupA.JSON["item"].(map[string]any)["id"].(float64))

	groupB := performRequest(
		t,
		server.Handler(),
		http.MethodPost,
		"/api/v1/proxy-groups",
		map[string]any{
			"name":         "group-b",
			"effective_ip": "127.0.0.1",
		},
		http.StatusCreated,
		sessionCookie,
	)
	groupBID := int64(groupB.JSON["item"].(map[string]any)["id"].(float64))

	updated := performRequest(
		t,
		server.Handler(),
		http.MethodPatch,
		"/api/v1/proxy-groups/"+jsonNumberString(float64(groupAID)),
		map[string]any{
			"name":         "group-a-updated",
			"effective_ip": "127.0.0.1",
			"enabled":      true,
		},
		http.StatusOK,
		sessionCookie,
	)
	if updated.JSON["item"].(map[string]any)["name"] != "group-a-updated" {
		t.Fatalf("unexpected proxy group update payload: %#v", updated.JSON)
	}
	if got := refresher.calls(); len(got) != 1 || got[0] != groupAID {
		t.Fatalf("unexpected refresh calls after proxy group update: %#v", got)
	}
	refresher.reset()

	createdTunnel := performRequest(
		t,
		server.Handler(),
		http.MethodPost,
		"/api/v1/tunnels",
		map[string]any{
			"group_id":     groupAID,
			"name":         "tunnel-a",
			"protocol":     "tcp",
			"remote_type":  "single",
			"remote_start": 20000,
			"remote_end":   20000,
			"local_host":   "127.0.0.1",
			"local_start":  8080,
			"local_end":    8080,
			"enabled":      true,
		},
		http.StatusCreated,
		sessionCookie,
	)
	tunnelID := createdTunnel.JSON["item"].(map[string]any)["id"].(float64)
	if got := refresher.calls(); len(got) != 1 || got[0] != groupAID {
		t.Fatalf("unexpected refresh calls after tunnel create: %#v", got)
	}
	refresher.reset()

	performRequest(
		t,
		server.Handler(),
		http.MethodPatch,
		"/api/v1/tunnels/"+jsonNumberString(tunnelID),
		map[string]any{
			"group_id":     groupBID,
			"name":         "tunnel-a",
			"protocol":     "tcp",
			"remote_type":  "single",
			"remote_start": 20000,
			"remote_end":   20000,
			"local_host":   "127.0.0.1",
			"local_start":  8080,
			"local_end":    8080,
			"enabled":      true,
		},
		http.StatusOK,
		sessionCookie,
	)
	if got := refresher.calls(); len(got) != 2 || got[0] != groupAID || got[1] != groupBID {
		t.Fatalf("unexpected refresh calls after tunnel move: %#v", got)
	}
	refresher.reset()

	performRequest(
		t,
		server.Handler(),
		http.MethodPost,
		"/api/v1/proxy-groups/"+jsonNumberString(float64(groupBID))+"/key",
		nil,
		http.StatusOK,
		sessionCookie,
	)
	if got := refresher.calls(); len(got) != 1 || got[0] != groupBID {
		t.Fatalf("unexpected refresh calls after credential rotation: %#v", got)
	}
	refresher.reset()

	performRequest(
		t,
		server.Handler(),
		http.MethodDelete,
		"/api/v1/tunnels/"+jsonNumberString(tunnelID),
		nil,
		http.StatusOK,
		sessionCookie,
	)
	if got := refresher.calls(); len(got) != 1 || got[0] != groupBID {
		t.Fatalf("unexpected refresh calls after tunnel delete: %#v", got)
	}
	refresher.reset()

	performRequest(
		t,
		server.Handler(),
		http.MethodDelete,
		"/api/v1/proxy-groups/"+jsonNumberString(float64(groupBID)),
		nil,
		http.StatusOK,
		sessionCookie,
	)
	if got := refresher.calls(); len(got) != 1 || got[0] != groupBID {
		t.Fatalf("unexpected refresh calls after proxy group delete: %#v", got)
	}
}

func TestTunnelStatusesIncludeEnabledDisabledConflictAndAbnormal(t *testing.T) {
	store := newTestStore(t)
	manager := newTestAuthManager(t, true)
	runtime := &recordingTunnelRuntimeStatusReader{
		issues: map[int64]string{
			5: "tcp listener 127.0.0.1:21000 start failed: bind blocked",
		},
	}

	server, err := NewServer(
		Options{
			Addr:              "127.0.0.1:7080",
			ReadHeaderTimeout: 5 * time.Second,
			Store:             store,
			Network: staticSnapshotReader{
				snapshot: system.Snapshot{
					AvailableIPs: []system.IPAddress{
						{Addr: "127.0.0.1", Family: system.FamilyIPv4},
					},
				},
			},
			RuntimeStatus: runtime,
			Auth:          manager,
		},
		slog.New(slog.NewTextHandler(io.Discard, nil)),
		"test",
	)
	if err != nil {
		t.Fatalf("new server: %v", err)
	}

	insertProxyGroup(t, store, 1, "group-a", "token-a", "hash-a", "127.0.0.1", true)
	insertProxyGroup(t, store, 2, "group-b", "token-b", "hash-b", "127.0.0.1", true)
	insertTunnel(t, store, 1, 1, "conflict-a", "tcp", 20000, 20000, true)
	insertTunnel(t, store, 2, 1, "disabled-a", "tcp", 20001, 20001, false)
	insertTunnel(t, store, 3, 2, "conflict-b", "tcp", 20000, 20000, true)
	insertTunnel(t, store, 4, 1, "enabled-a", "tcp", 22000, 22000, true)
	insertTunnel(t, store, 5, 1, "abnormal-a", "tcp", 21000, 21000, true)

	sessionCookie := authenticatedManagementCookie(t, manager)
	result := performRequest(
		t,
		server.Handler(),
		http.MethodGet,
		"/api/v1/tunnels",
		nil,
		http.StatusOK,
		sessionCookie,
	)

	items, ok := result.JSON["items"].([]any)
	if !ok || len(items) != 5 {
		t.Fatalf("unexpected tunnel list payload: %#v", result.JSON)
	}

	statuses := make(map[string]map[string]any, len(items))
	for _, raw := range items {
		item, ok := raw.(map[string]any)
		if !ok {
			t.Fatalf("unexpected tunnel item: %#v", raw)
		}
		statuses[item["name"].(string)] = item
	}

	if statuses["conflict-a"]["status"] != tunnelStatusConflict {
		t.Fatalf("unexpected conflict-a status: %#v", statuses["conflict-a"])
	}
	if statuses["conflict-b"]["status"] != tunnelStatusConflict {
		t.Fatalf("unexpected conflict-b status: %#v", statuses["conflict-b"])
	}
	if !strings.Contains(statuses["conflict-a"]["status_reason"].(string), `隧道"conflict-b"`) {
		t.Fatalf("unexpected conflict-a reason: %#v", statuses["conflict-a"])
	}
	if statuses["disabled-a"]["status"] != tunnelStatusDisabled {
		t.Fatalf("unexpected disabled-a status: %#v", statuses["disabled-a"])
	}
	if statuses["enabled-a"]["status"] != tunnelStatusEnabled {
		t.Fatalf("unexpected enabled-a status: %#v", statuses["enabled-a"])
	}
	if statuses["abnormal-a"]["status"] != tunnelStatusAbnormal {
		t.Fatalf("unexpected abnormal-a status: %#v", statuses["abnormal-a"])
	}
	if statuses["abnormal-a"]["status_reason"] != runtime.issues[5] {
		t.Fatalf("unexpected abnormal-a reason: %#v", statuses["abnormal-a"])
	}
}

func TestTunnelStatusesIncludeWildcardConflict(t *testing.T) {
	store := newTestStore(t)
	manager := newTestAuthManager(t, true)

	server, err := NewServer(
		Options{
			Addr:              "127.0.0.1:7080",
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

	insertProxyGroup(t, store, 1, "group-any4", "token-a", "hash-a", "0.0.0.0", true)
	insertProxyGroup(t, store, 2, "group-any6", "token-b", "hash-b", "::", true)
	insertTunnel(t, store, 1, 1, "any4-tunnel", "tcp", 23000, 23000, true)
	insertTunnel(t, store, 2, 2, "any6-tunnel", "tcp", 23000, 23000, true)

	sessionCookie := authenticatedManagementCookie(t, manager)
	result := performRequest(
		t,
		server.Handler(),
		http.MethodGet,
		"/api/v1/tunnels",
		nil,
		http.StatusOK,
		sessionCookie,
	)

	items, ok := result.JSON["items"].([]any)
	if !ok || len(items) != 2 {
		t.Fatalf("unexpected tunnel list payload: %#v", result.JSON)
	}

	statuses := make(map[string]map[string]any, len(items))
	for _, raw := range items {
		item, ok := raw.(map[string]any)
		if !ok {
			t.Fatalf("unexpected tunnel item: %#v", raw)
		}
		statuses[item["name"].(string)] = item
	}

	if statuses["any4-tunnel"]["status"] != tunnelStatusConflict {
		t.Fatalf("unexpected any4-tunnel status: %#v", statuses["any4-tunnel"])
	}
	if statuses["any6-tunnel"]["status"] != tunnelStatusConflict {
		t.Fatalf("unexpected any6-tunnel status: %#v", statuses["any6-tunnel"])
	}
	if !strings.Contains(statuses["any4-tunnel"]["status_reason"].(string), `隧道"any6-tunnel"`) {
		t.Fatalf("unexpected any4-tunnel reason: %#v", statuses["any4-tunnel"])
	}
}

func TestCreateTunnelRejectsSpecificConflict(t *testing.T) {
	store := newTestStore(t)
	manager := newTestAuthManager(t, true)

	server, err := NewServer(
		Options{
			Addr:              "127.0.0.1:7080",
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

	groupA := performRequest(
		t,
		server.Handler(),
		http.MethodPost,
		"/api/v1/proxy-groups",
		map[string]any{
			"name":         "group-a",
			"effective_ip": "127.0.0.1",
			"enabled":      true,
		},
		http.StatusCreated,
		sessionCookie,
	)
	groupAID := int64(groupA.JSON["item"].(map[string]any)["id"].(float64))

	groupB := performRequest(
		t,
		server.Handler(),
		http.MethodPost,
		"/api/v1/proxy-groups",
		map[string]any{
			"name":         "group-b",
			"effective_ip": "127.0.0.1",
			"enabled":      true,
		},
		http.StatusCreated,
		sessionCookie,
	)
	groupBID := int64(groupB.JSON["item"].(map[string]any)["id"].(float64))

	created := performRequest(
		t,
		server.Handler(),
		http.MethodPost,
		"/api/v1/tunnels",
		map[string]any{
			"group_id":     groupAID,
			"name":         "tunnel-a",
			"protocol":     "tcp",
			"remote_type":  "single",
			"remote_start": 20000,
			"remote_end":   20000,
			"local_host":   "127.0.0.1",
			"local_start":  8080,
			"local_end":    8080,
			"enabled":      true,
		},
		http.StatusCreated,
		sessionCookie,
	)
	if created.JSON["item"].(map[string]any)["status"] != tunnelStatusEnabled {
		t.Fatalf("unexpected created tunnel status: %#v", created.JSON)
	}

	conflict := performRequest(
		t,
		server.Handler(),
		http.MethodPost,
		"/api/v1/tunnels",
		map[string]any{
			"group_id":     groupBID,
			"name":         "tunnel-b",
			"protocol":     "tcp",
			"remote_type":  "single",
			"remote_start": 20000,
			"remote_end":   20000,
			"local_host":   "127.0.0.1",
			"local_start":  8081,
			"local_end":    8081,
			"enabled":      true,
		},
		http.StatusConflict,
		sessionCookie,
	)
	if !strings.Contains(conflict.JSON["error"].(string), `隧道"tunnel-a"`) {
		t.Fatalf("unexpected conflict error: %#v", conflict.JSON)
	}
}

func TestCreateTunnelRejectsWildcardConflictAny4Any6(t *testing.T) {
	store := newTestStore(t)
	manager := newTestAuthManager(t, true)

	server, err := NewServer(
		Options{
			Addr:              "127.0.0.1:7080",
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

	sessionCookie := authenticatedManagementCookie(t, manager)

	groupAny4 := performRequest(
		t,
		server.Handler(),
		http.MethodPost,
		"/api/v1/proxy-groups",
		map[string]any{
			"name":         "group-any4",
			"effective_ip": "0.0.0.0",
			"enabled":      true,
		},
		http.StatusCreated,
		sessionCookie,
	)
	groupAny4ID := int64(groupAny4.JSON["item"].(map[string]any)["id"].(float64))

	groupAny6 := performRequest(
		t,
		server.Handler(),
		http.MethodPost,
		"/api/v1/proxy-groups",
		map[string]any{
			"name":         "group-any6",
			"effective_ip": "::",
			"enabled":      true,
		},
		http.StatusCreated,
		sessionCookie,
	)
	groupAny6ID := int64(groupAny6.JSON["item"].(map[string]any)["id"].(float64))

	performRequest(
		t,
		server.Handler(),
		http.MethodPost,
		"/api/v1/tunnels",
		map[string]any{
			"group_id":     groupAny4ID,
			"name":         "any4-tunnel",
			"protocol":     "tcp",
			"remote_type":  "single",
			"remote_start": 23010,
			"remote_end":   23010,
			"local_host":   "127.0.0.1",
			"local_start":  8080,
			"local_end":    8080,
			"enabled":      true,
		},
		http.StatusCreated,
		sessionCookie,
	)

	conflict := performRequest(
		t,
		server.Handler(),
		http.MethodPost,
		"/api/v1/tunnels",
		map[string]any{
			"group_id":     groupAny6ID,
			"name":         "any6-tunnel",
			"protocol":     "tcp",
			"remote_type":  "single",
			"remote_start": 23010,
			"remote_end":   23010,
			"local_host":   "127.0.0.1",
			"local_start":  8081,
			"local_end":    8081,
			"enabled":      true,
		},
		http.StatusConflict,
		sessionCookie,
	)
	if !strings.Contains(conflict.JSON["error"].(string), `隧道"any4-tunnel"`) {
		t.Fatalf("unexpected wildcard conflict error: %#v", conflict.JSON)
	}
}

func TestCreateTunnelAllowsSpecific4WithAny6(t *testing.T) {
	store := newTestStore(t)
	manager := newTestAuthManager(t, true)

	server, err := NewServer(
		Options{
			Addr:              "127.0.0.1:7080",
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

	groupAny6 := performRequest(
		t,
		server.Handler(),
		http.MethodPost,
		"/api/v1/proxy-groups",
		map[string]any{
			"name":         "group-any6",
			"effective_ip": "::",
			"enabled":      true,
		},
		http.StatusCreated,
		sessionCookie,
	)
	groupAny6ID := int64(groupAny6.JSON["item"].(map[string]any)["id"].(float64))

	groupSpecific4 := performRequest(
		t,
		server.Handler(),
		http.MethodPost,
		"/api/v1/proxy-groups",
		map[string]any{
			"name":         "group-v4",
			"effective_ip": "127.0.0.1",
			"enabled":      true,
		},
		http.StatusCreated,
		sessionCookie,
	)
	groupSpecific4ID := int64(groupSpecific4.JSON["item"].(map[string]any)["id"].(float64))

	performRequest(
		t,
		server.Handler(),
		http.MethodPost,
		"/api/v1/tunnels",
		map[string]any{
			"group_id":     groupAny6ID,
			"name":         "any6-tunnel",
			"protocol":     "tcp",
			"remote_type":  "single",
			"remote_start": 23020,
			"remote_end":   23020,
			"local_host":   "127.0.0.1",
			"local_start":  8080,
			"local_end":    8080,
			"enabled":      true,
		},
		http.StatusCreated,
		sessionCookie,
	)

	created := performRequest(
		t,
		server.Handler(),
		http.MethodPost,
		"/api/v1/tunnels",
		map[string]any{
			"group_id":     groupSpecific4ID,
			"name":         "v4-tunnel",
			"protocol":     "tcp",
			"remote_type":  "single",
			"remote_start": 23020,
			"remote_end":   23020,
			"local_host":   "127.0.0.1",
			"local_start":  8081,
			"local_end":    8081,
			"enabled":      true,
		},
		http.StatusCreated,
		sessionCookie,
	)
	if created.JSON["item"].(map[string]any)["status"] != tunnelStatusEnabled {
		t.Fatalf("unexpected tunnel status: %#v", created.JSON)
	}
}

func TestUpdateProxyGroupRejectsSpecificConflict(t *testing.T) {
	store := newTestStore(t)
	manager := newTestAuthManager(t, true)

	server, err := NewServer(
		Options{
			Addr:              "127.0.0.1:7080",
			ReadHeaderTimeout: 5 * time.Second,
			Store:             store,
			Network: staticSnapshotReader{
				snapshot: system.Snapshot{
					AvailableIPs: []system.IPAddress{
						{Addr: "127.0.0.1", Family: system.FamilyIPv4},
						{Addr: "127.0.0.2", Family: system.FamilyIPv4},
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

	groupA := performRequest(
		t,
		server.Handler(),
		http.MethodPost,
		"/api/v1/proxy-groups",
		map[string]any{
			"name":         "group-a",
			"effective_ip": "127.0.0.1",
			"enabled":      true,
		},
		http.StatusCreated,
		sessionCookie,
	)
	groupAID := int64(groupA.JSON["item"].(map[string]any)["id"].(float64))

	groupB := performRequest(
		t,
		server.Handler(),
		http.MethodPost,
		"/api/v1/proxy-groups",
		map[string]any{
			"name":         "group-b",
			"effective_ip": "127.0.0.2",
			"enabled":      true,
		},
		http.StatusCreated,
		sessionCookie,
	)
	groupBID := int64(groupB.JSON["item"].(map[string]any)["id"].(float64))

	performRequest(
		t,
		server.Handler(),
		http.MethodPost,
		"/api/v1/tunnels",
		map[string]any{
			"group_id":     groupAID,
			"name":         "tunnel-a",
			"protocol":     "tcp",
			"remote_type":  "single",
			"remote_start": 20010,
			"remote_end":   20010,
			"local_host":   "127.0.0.1",
			"local_start":  8080,
			"local_end":    8080,
			"enabled":      true,
		},
		http.StatusCreated,
		sessionCookie,
	)

	performRequest(
		t,
		server.Handler(),
		http.MethodPost,
		"/api/v1/tunnels",
		map[string]any{
			"group_id":     groupBID,
			"name":         "tunnel-b",
			"protocol":     "tcp",
			"remote_type":  "single",
			"remote_start": 20010,
			"remote_end":   20010,
			"local_host":   "127.0.0.1",
			"local_start":  8081,
			"local_end":    8081,
			"enabled":      true,
		},
		http.StatusCreated,
		sessionCookie,
	)

	conflict := performRequest(
		t,
		server.Handler(),
		http.MethodPatch,
		"/api/v1/proxy-groups/"+jsonNumberString(float64(groupBID)),
		map[string]any{
			"effective_ip": "127.0.0.1",
		},
		http.StatusConflict,
		sessionCookie,
	)
	if !strings.Contains(conflict.JSON["error"].(string), `隧道"tunnel-a"`) {
		t.Fatalf("unexpected proxy group conflict error: %#v", conflict.JSON)
	}
}

func TestProxyGroupCreateAllowsSpecialIPv6WithoutSnapshot(t *testing.T) {
	store := newTestStore(t)
	manager := newTestAuthManager(t, true)

	server, err := NewServer(
		Options{
			Addr:              "127.0.0.1:7080",
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

	sessionCookie := authenticatedManagementCookie(t, manager)

	created := performRequest(
		t,
		server.Handler(),
		http.MethodPost,
		"/api/v1/proxy-groups",
		map[string]any{
			"name":         "group-ipv6-any",
			"effective_ip": "::",
			"enabled":      true,
		},
		http.StatusCreated,
		sessionCookie,
	)
	item, ok := created.JSON["item"].(map[string]any)
	if !ok {
		t.Fatalf("unexpected create payload: %#v", created.JSON)
	}
	if item["effective_ip"] != "::" {
		t.Fatalf("unexpected create effective_ip: %#v", item)
	}
	if item["status"] != proxyGroupStatusEnabled {
		t.Fatalf("unexpected create status: %#v", item)
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
	if listItem["effective_ip"] != "::" {
		t.Fatalf("unexpected list effective_ip: %#v", listItem)
	}
	if listItem["status"] != proxyGroupStatusEnabled {
		t.Fatalf("unexpected list status: %#v", listItem)
	}
}

func TestProxyGroupStatusBecomesAbnormalWhenEffectiveIPLeavesSnapshot(t *testing.T) {
	store := newTestStore(t)
	manager := newTestAuthManager(t, true)
	reader := &mutableSnapshotReader{
		snapshot: system.Snapshot{
			AvailableIPs: []system.IPAddress{
				{Addr: "127.0.0.1", Family: system.FamilyIPv4},
			},
		},
	}

	server, err := NewServer(
		Options{
			Addr:              "127.0.0.1:7080",
			ReadHeaderTimeout: 5 * time.Second,
			Store:             store,
			Network:           reader,
			Auth:              manager,
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
			"enabled":      true,
		},
		http.StatusCreated,
		sessionCookie,
	)
	item, ok := created.JSON["item"].(map[string]any)
	if !ok {
		t.Fatalf("unexpected create payload: %#v", created.JSON)
	}
	if item["status"] != proxyGroupStatusEnabled {
		t.Fatalf("unexpected create status: %#v", item)
	}

	reader.snapshot = system.Snapshot{
		AvailableIPs: []system.IPAddress{
			{Addr: "10.0.0.2", Family: system.FamilyIPv4},
		},
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
	if listItem["status"] != proxyGroupStatusAbnormal {
		t.Fatalf("unexpected abnormal status: %#v", listItem)
	}
	if listItem["status_reason"] != proxyGroupStatusReasonMissingLocalIP {
		t.Fatalf("unexpected abnormal reason: %#v", listItem)
	}
}

func TestProxyGroupPatchAllowsDisableWhenStoredEffectiveIPIsStale(t *testing.T) {
	store := newTestStore(t)
	manager := newTestAuthManager(t, true)
	reader := &mutableSnapshotReader{
		snapshot: system.Snapshot{
			AvailableIPs: []system.IPAddress{
				{Addr: "127.0.0.1", Family: system.FamilyIPv4},
			},
		},
	}

	server, err := NewServer(
		Options{
			Addr:              "127.0.0.1:7080",
			ReadHeaderTimeout: 5 * time.Second,
			Store:             store,
			Network:           reader,
			Auth:              manager,
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
			"enabled":      true,
		},
		http.StatusCreated,
		sessionCookie,
	)
	createdItem, ok := created.JSON["item"].(map[string]any)
	if !ok {
		t.Fatalf("unexpected create payload: %#v", created.JSON)
	}
	groupID, ok := createdItem["id"].(float64)
	if !ok || int64(groupID) <= 0 {
		t.Fatalf("unexpected create group id: %#v", createdItem)
	}

	reader.snapshot = system.Snapshot{
		AvailableIPs: []system.IPAddress{
			{Addr: "10.0.0.2", Family: system.FamilyIPv4},
		},
	}

	updated := performRequest(
		t,
		server.Handler(),
		http.MethodPatch,
		"/api/v1/proxy-groups/"+jsonNumberString(groupID),
		map[string]any{
			"enabled": false,
		},
		http.StatusOK,
		sessionCookie,
	)
	updatedItem, ok := updated.JSON["item"].(map[string]any)
	if !ok {
		t.Fatalf("unexpected update payload: %#v", updated.JSON)
	}
	if updatedItem["effective_ip"] != "127.0.0.1" {
		t.Fatalf("unexpected preserved stale effective_ip: %#v", updatedItem)
	}
	if updatedItem["enabled"] != false {
		t.Fatalf("unexpected updated enabled: %#v", updatedItem)
	}
	if updatedItem["status"] != proxyGroupStatusDisabled {
		t.Fatalf("unexpected disabled status: %#v", updatedItem)
	}
}

func TestLocalIPsEndpoint(t *testing.T) {
	store := newTestStore(t)
	manager := newTestAuthManager(t, true)

	server, err := NewServer(
		Options{
			Addr:              "127.0.0.1:7080",
			ReadHeaderTimeout: 5 * time.Second,
			Store:             store,
			Network: staticSnapshotReader{
				snapshot: system.Snapshot{
					AvailableIPs: []system.IPAddress{
						{Addr: "127.0.0.1", Family: system.FamilyIPv4},
						{Addr: "::1", Family: system.FamilyIPv6},
						{Addr: "192.168.1.1", Family: system.FamilyIPv4},
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

	result := performRequest(
		t,
		server.Handler(),
		http.MethodGet,
		"/api/v1/local-ips",
		nil,
		http.StatusOK,
		sessionCookie,
	)
	ipItems, ok := result.JSON["items"].([]any)
	if !ok {
		t.Fatalf("unexpected local-ips payload: %#v", result.JSON)
	}

	if len(ipItems) != 5 {
		t.Fatalf("expected 5 items (2 special + 3 local), got %d: %#v", len(ipItems), ipItems)
	}

	first, ok := ipItems[0].(map[string]any)
	if !ok || first["addr"] != "0.0.0.0" || first["family"] != "ipv4" {
		t.Fatalf("expected first item to be 0.0.0.0 ipv4, got: %#v", first)
	}
	second, ok := ipItems[1].(map[string]any)
	if !ok || second["addr"] != "::" || second["family"] != "ipv6" {
		t.Fatalf("expected second item to be :: ipv6, got: %#v", second)
	}

	performRequest(
		t,
		server.Handler(),
		http.MethodPost,
		"/api/v1/local-ips",
		nil,
		http.StatusMethodNotAllowed,
		sessionCookie,
	)
}

func TestLocalIPsEndpointReturnsUnavailableWithoutSnapshot(t *testing.T) {
	store := newTestStore(t)
	manager := newTestAuthManager(t, true)

	server, err := NewServer(
		Options{
			Addr:              "127.0.0.1:7080",
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

	sessionCookie := authenticatedManagementCookie(t, manager)

	performRequest(
		t,
		server.Handler(),
		http.MethodGet,
		"/api/v1/local-ips",
		nil,
		http.StatusServiceUnavailable,
		sessionCookie,
	)
}

func TestWebUIHandlerServesStaticFilesAndSPAFallback(t *testing.T) {
	server, err := NewServer(
		Options{
			Addr:              "127.0.0.1:7080",
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

func TestNewServerFallsBackWhenWebUIDistDirMissing(t *testing.T) {
	server, err := NewServer(
		Options{
			Addr:              "127.0.0.1:7080",
			ReadHeaderTimeout: 5 * time.Second,
			Auth:              newTestAuthManager(t, false),
			WebUIDistDir:      filepath.Join(t.TempDir(), "missing-dist"),
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
		t.Fatalf("unexpected placeholder status: %d body=%s", recorder.Code, recorder.Body.String())
	}
	if !bytes.Contains(recorder.Body.Bytes(), []byte("frps 管理面改造中")) {
		t.Fatalf("unexpected placeholder body: %s", recorder.Body.String())
	}
}

func TestNewServerFallsBackWhenWebUIIndexMissing(t *testing.T) {
	distDir := filepath.Join(t.TempDir(), "dist")
	if err := os.MkdirAll(filepath.Join(distDir, "assets"), 0o755); err != nil {
		t.Fatalf("create dist dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(distDir, "assets", "app.js"), []byte("console.log('webui-ok');"), 0o644); err != nil {
		t.Fatalf("write asset: %v", err)
	}

	server, err := NewServer(
		Options{
			Addr:              "127.0.0.1:7080",
			ReadHeaderTimeout: 5 * time.Second,
			Auth:              newTestAuthManager(t, false),
			WebUIDistDir:      distDir,
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
		t.Fatalf("unexpected placeholder status: %d body=%s", recorder.Code, recorder.Body.String())
	}
	if !bytes.Contains(recorder.Body.Bytes(), []byte("frps 管理面改造中")) {
		t.Fatalf("unexpected placeholder body: %s", recorder.Body.String())
	}
}

func TestNewServerRejectsInvalidWebUIDistPath(t *testing.T) {
	distPath := filepath.Join(t.TempDir(), "webui-dist.txt")
	if err := os.WriteFile(distPath, []byte("not a directory"), 0o644); err != nil {
		t.Fatalf("write dist file: %v", err)
	}

	_, err := NewServer(
		Options{
			Addr:              "127.0.0.1:7080",
			ReadHeaderTimeout: 5 * time.Second,
			Auth:              newTestAuthManager(t, false),
			WebUIDistDir:      distPath,
		},
		slog.New(slog.NewTextHandler(io.Discard, nil)),
		"test",
	)
	if err == nil {
		t.Fatal("expected invalid webui dist path to fail")
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
	client_id TEXT NOT NULL UNIQUE,
	client_secret_hash TEXT NOT NULL,
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

type mutableSnapshotReader struct {
	snapshot system.Snapshot
}

func (r *mutableSnapshotReader) Current() system.Snapshot {
	if r == nil {
		return system.Snapshot{}
	}
	return r.snapshot
}

type recordingGroupRefresher struct {
	groupIDs []int64
}

func (r *recordingGroupRefresher) RefreshGroup(groupID int64) {
	r.groupIDs = append(r.groupIDs, groupID)
}

func (r *recordingGroupRefresher) calls() []int64 {
	return append([]int64(nil), r.groupIDs...)
}

func (r *recordingGroupRefresher) reset() {
	r.groupIDs = nil
}

type recordingTunnelRuntimeStatusReader struct {
	issues map[int64]string
}

func (r *recordingTunnelRuntimeStatusReader) TunnelRuntimeIssues() map[int64]string {
	if r == nil || len(r.issues) == 0 {
		return nil
	}
	issues := make(map[int64]string, len(r.issues))
	for tunnelID, reason := range r.issues {
		issues[tunnelID] = reason
	}
	return issues
}

func insertProxyGroup(t *testing.T, store *storage.SQL, id int64, name, tokenID, tokenHash, effectiveIP string, enabled bool) {
	t.Helper()
	now := schemaTimestamp()
	if _, err := store.Exec(
		`INSERT INTO proxy_groups (id, name, client_id, client_secret_hash, effective_ip, enabled, rate_limit, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, 0, ?, ?)`,
		id,
		name,
		tokenID,
		tokenHash,
		effectiveIP,
		boolToInt(enabled),
		now,
		now,
	); err != nil {
		t.Fatalf("insert proxy group: %v", err)
	}
}

func insertTunnel(t *testing.T, store *storage.SQL, id int64, groupID int64, name, protocol string, remoteStart, remoteEnd int64, enabled bool) {
	t.Helper()
	now := schemaTimestamp()
	remoteType := "single"
	if remoteStart != remoteEnd {
		remoteType = "range"
	}
	if _, err := store.Exec(
		`INSERT INTO tunnels (id, group_id, name, protocol, remote_type, remote_start, remote_end, local_host, local_start, local_end, enabled, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, '127.0.0.1', ?, ?, ?, ?, ?)`,
		id,
		groupID,
		name,
		protocol,
		remoteType,
		remoteStart,
		remoteEnd,
		8080,
		8080+(remoteEnd-remoteStart),
		boolToInt(enabled),
		now,
		now,
	); err != nil {
		t.Fatalf("insert tunnel: %v", err)
	}
}
