package api

import (
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/zightch/frp/frps/internal/system"
)

func TestCreateTunnelRejectsInvalidInputMatrix(t *testing.T) {
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
	group := performRequest(
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
	groupID := int64(group.JSON["item"].(map[string]any)["id"].(float64))

	base := map[string]any{
		"group_id":     groupID,
		"name":         "valid-tunnel",
		"protocol":     "tcp",
		"remote_type":  "single",
		"remote_start": 20000,
		"remote_end":   20000,
		"local_host":   "127.0.0.1",
		"local_start":  8080,
		"local_end":    8080,
		"enabled":      true,
	}

	performRequest(t, server.Handler(), http.MethodPost, "/api/v1/tunnels", cloneBody(base), http.StatusCreated, sessionCookie)

	cases := []struct {
		name        string
		body        map[string]any
		wantStatus  int
		wantMessage string
	}{
		{
			name: "missing protocol",
			body: map[string]any{
				"group_id":     groupID,
				"name":         "missing-protocol",
				"remote_type":  "single",
				"remote_start": 20001,
				"remote_end":   20001,
				"local_host":   "127.0.0.1",
				"local_start":  8081,
				"local_end":    8081,
				"enabled":      true,
			},
			wantStatus:  http.StatusBadRequest,
			wantMessage: "protocol must be tcp or udp",
		},
		{
			name: "empty port range",
			body: map[string]any{
				"group_id":     groupID,
				"name":         "empty-range",
				"protocol":     "tcp",
				"remote_type":  "range",
				"remote_start": 0,
				"remote_end":   0,
				"local_host":   "127.0.0.1",
				"local_start":  0,
				"local_end":    0,
				"enabled":      true,
			},
			wantStatus:  http.StatusBadRequest,
			wantMessage: "ports must be between 1 and 65535",
		},
		{
			name: "reversed range",
			body: map[string]any{
				"group_id":     groupID,
				"name":         "reversed-range",
				"protocol":     "tcp",
				"remote_type":  "range",
				"remote_start": 20010,
				"remote_end":   20009,
				"local_host":   "127.0.0.1",
				"local_start":  8080,
				"local_end":    8079,
				"enabled":      true,
			},
			wantStatus:  http.StatusBadRequest,
			wantMessage: "port range end must be greater than or equal to start",
		},
		{
			name: "out of range port",
			body: map[string]any{
				"group_id":     groupID,
				"name":         "out-of-range",
				"protocol":     "tcp",
				"remote_type":  "single",
				"remote_start": 70000,
				"remote_end":   70000,
				"local_host":   "127.0.0.1",
				"local_start":  8080,
				"local_end":    8080,
				"enabled":      true,
			},
			wantStatus:  http.StatusBadRequest,
			wantMessage: "ports must be between 1 and 65535",
		},
		{
			name: "duplicate tunnel name",
			body: map[string]any{
				"group_id":     groupID,
				"name":         "valid-tunnel",
				"protocol":     "tcp",
				"remote_type":  "single",
				"remote_start": 20011,
				"remote_end":   20011,
				"local_host":   "127.0.0.1",
				"local_start":  8091,
				"local_end":    8091,
				"enabled":      true,
			},
			wantStatus:  http.StatusConflict,
			wantMessage: "tunnel name already exists in the selected proxy group",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			response := performRequest(
				t,
				server.Handler(),
				http.MethodPost,
				"/api/v1/tunnels",
				tc.body,
				tc.wantStatus,
				sessionCookie,
			)
			message, _ := response.JSON["error"].(string)
			if !strings.Contains(message, tc.wantMessage) {
				t.Fatalf("unexpected error message: %#v", response.JSON)
			}
		})
	}
}

func TestTunnelStatusesDisableCombinationsDoNotParticipateInConflicts(t *testing.T) {
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

	insertProxyGroup(t, store, 1, "group-disabled-tunnels", "token-a", "hash-a", "127.0.0.1", true)
	insertProxyGroup(t, store, 2, "group-disabled", "token-b", "hash-b", "127.0.0.1", false)
	insertProxyGroup(t, store, 3, "group-active", "token-c", "hash-c", "127.0.0.1", true)
	insertTunnel(t, store, 1, 1, "disabled-only", "tcp", 24000, 24000, false)
	insertTunnel(t, store, 2, 2, "group-disabled-enabled-tunnel", "tcp", 24000, 24000, true)
	insertTunnel(t, store, 3, 3, "healthy", "tcp", 24000, 24000, true)

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
	if !ok || len(items) != 3 {
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

	if statuses["disabled-only"]["status"] != tunnelStatusDisabled {
		t.Fatalf("unexpected disabled-only status: %#v", statuses["disabled-only"])
	}
	if statuses["group-disabled-enabled-tunnel"]["status"] != tunnelStatusDisabled {
		t.Fatalf("unexpected group-disabled-enabled-tunnel status: %#v", statuses["group-disabled-enabled-tunnel"])
	}
	if statuses["healthy"]["status"] != tunnelStatusEnabled {
		t.Fatalf("unexpected healthy status: %#v", statuses["healthy"])
	}
	if statuses["healthy"]["status_reason"] != nil {
		t.Fatalf("expected healthy tunnel to stay conflict-free, got %#v", statuses["healthy"])
	}
}

func cloneBody(source map[string]any) map[string]any {
	cloned := make(map[string]any, len(source))
	for key, value := range source {
		cloned[key] = value
	}
	return cloned
}
