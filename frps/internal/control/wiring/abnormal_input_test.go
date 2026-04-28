package wiring

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"log/slog"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/zightch/frp/frps/internal/dbschema"
	"github.com/zightch/frp/frps/internal/storage"
	"github.com/zightch/frp/frps/internal/system"
	"github.com/zightch/frp/frps/internal/testdb"
	"github.com/zightch/frp/frps/pkg/protocol"
)

func TestServerRejectsInvalidConfigAckMatrix(t *testing.T) {
	cases := []struct {
		name        string
		buildFrame  func(t *testing.T, configFrame protocol.Frame, version uint64) protocol.Frame
		wantCode    uint16
		wantMessage string
	}{
		{
			name: "request id zero",
			buildFrame: func(t *testing.T, _ protocol.Frame, version uint64) protocol.Frame {
				return buildConfigAckFrame(t, 0, 0, version, protocol.StatusOK, "")
			},
			wantCode:    protocol.ErrorCodeProtocolBadBody,
			wantMessage: "config.ack requestId must be non-zero",
		},
		{
			name: "stream id non-zero",
			buildFrame: func(t *testing.T, configFrame protocol.Frame, version uint64) protocol.Frame {
				return buildConfigAckFrame(t, configFrame.RequestID, 9, version, protocol.StatusOK, "")
			},
			wantCode:    protocol.ErrorCodeProtocolBadBody,
			wantMessage: "config.ack streamId must be zero",
		},
		{
			name: "request id mismatch",
			buildFrame: func(t *testing.T, configFrame protocol.Frame, version uint64) protocol.Frame {
				return buildConfigAckFrame(t, configFrame.RequestID+1, 0, version, protocol.StatusOK, "")
			},
			wantCode:    protocol.ErrorCodeProtocolBadBody,
			wantMessage: "unexpected config.ack requestId",
		},
		{
			name: "version mismatch",
			buildFrame: func(t *testing.T, configFrame protocol.Frame, version uint64) protocol.Frame {
				return buildConfigAckFrame(t, configFrame.RequestID, 0, version+1, protocol.StatusOK, "")
			},
			wantCode:    protocol.ErrorCodeProtocolBadBody,
			wantMessage: "config.ack version mismatch",
		},
		{
			name: "unknown status",
			buildFrame: func(t *testing.T, configFrame protocol.Frame, version uint64) protocol.Frame {
				return buildConfigAckFrame(t, configFrame.RequestID, 0, version, 99, "")
			},
			wantCode:    protocol.ErrorCodeProtocolBadBody,
			wantMessage: "unsupported config.ack status 99",
		},
		{
			name: "client rejected config",
			buildFrame: func(t *testing.T, configFrame protocol.Frame, version uint64) protocol.Frame {
				return buildConfigAckFrame(t, configFrame.RequestID, 0, version, protocol.StatusError, "client reject")
			},
			wantCode:    protocol.ErrorCodeConfigApplyFailed,
			wantMessage: "client rejected config version",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			server, tokenID, tokenHash := newConfigAckMatrixServer(t)
			clientConn, done, configFrame := authenticateServerSession(t, server, tokenID, tokenHash)
			defer clientConn.Close()

			configPush, err := protocol.UnmarshalConfigPush(configFrame.Body)
			if err != nil {
				t.Fatalf("unmarshal config.push: %v", err)
			}

			writeMessage(t, clientConn, tc.buildFrame(t, configFrame, configPush.ConfigVersion))

			errorFrame := readMessage(t, clientConn)
			if errorFrame.Type != protocol.TypeError {
				t.Fatalf("expected error frame, got %s", errorFrame.Type.String())
			}
			errorBody, err := protocol.UnmarshalErrorBody(errorFrame.Body)
			if err != nil {
				t.Fatalf("unmarshal error body: %v", err)
			}
			if errorBody.ErrorCode != tc.wantCode {
				t.Fatalf("unexpected error code: got %d want %d body=%#v", errorBody.ErrorCode, tc.wantCode, errorBody)
			}
			if !strings.Contains(errorBody.Message, tc.wantMessage) {
				t.Fatalf("unexpected error message: %q", errorBody.Message)
			}

			if _, err := readMessageWithin(clientConn, time.Second); err == nil {
				t.Fatal("expected invalid config.ack to close the session")
			} else if !errorsIsEOFOrClosed(err) {
				t.Fatalf("unexpected read error after invalid config.ack: %v", err)
			}

			select {
			case <-done:
			case <-time.After(2 * time.Second):
				t.Fatal("server connection did not exit")
			}

			if len(server.ObserveState().Sessions) != 0 {
				t.Fatalf("expected no active session after invalid config.ack, got %#v", server.ObserveState().Sessions)
			}
		})
	}
}

func TestServerRejectsDuplicateConfigAckAfterStartupAcceptance(t *testing.T) {
	server, tokenID, tokenHash := newConfigAckMatrixServer(t)
	clientConn, done, configFrame := authenticateServerSession(t, server, tokenID, tokenHash)
	defer clientConn.Close()

	configPush, err := protocol.UnmarshalConfigPush(configFrame.Body)
	if err != nil {
		t.Fatalf("unmarshal config.push: %v", err)
	}

	writeConfigAck(t, clientConn, configFrame.RequestID, configPush.ConfigVersion)
	waitForActiveGroupSession(t, server, 1)

	active, ok := server.activeSession(1)
	if !ok || active == nil || active.session == nil {
		t.Fatalf("expected active session after startup ack")
	}
	waitForIdleConfig(t, active.session)
	if active.session.LastAckedConfigVersion() != configPush.ConfigVersion {
		t.Fatalf("expected startup ack to advance version, got %d", active.session.LastAckedConfigVersion())
	}

	writeConfigAck(t, clientConn, configFrame.RequestID, configPush.ConfigVersion)

	errorFrame := readMessage(t, clientConn)
	if errorFrame.Type != protocol.TypeError {
		t.Fatalf("expected error frame, got %s", errorFrame.Type.String())
	}
	errorBody, err := protocol.UnmarshalErrorBody(errorFrame.Body)
	if err != nil {
		t.Fatalf("unmarshal error body: %v", err)
	}
	if errorBody.ErrorCode != protocol.ErrorCodeProtocolBadBody {
		t.Fatalf("unexpected duplicate-ack error code: %d", errorBody.ErrorCode)
	}
	if !strings.Contains(errorBody.Message, "unexpected config.ack requestId") {
		t.Fatalf("unexpected duplicate-ack error message: %q", errorBody.Message)
	}

	if _, err := readMessageWithin(clientConn, time.Second); err == nil {
		t.Fatal("expected duplicate config.ack to close the session")
	} else if !errorsIsEOFOrClosed(err) {
		t.Fatalf("unexpected read error after duplicate config.ack: %v", err)
	}

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("server connection did not exit")
	}
}

func TestServerDirtyEffectiveIPScenariosMarkInitialScanAndRejectInitialLogin(t *testing.T) {
	cases := []struct {
		name           string
		effectiveIP    string
		snapshot       system.Snapshot
		wantScanText   string
		wantRejectText string
	}{
		{
			name:           "empty effective ip",
			effectiveIP:    "",
			snapshot:       system.Snapshot{AvailableIPs: []system.IPAddress{{Addr: "127.0.0.1", Family: system.FamilyIPv4}}},
			wantScanText:   "无效",
			wantRejectText: "无效",
		},
		{
			name:           "invalid effective ip literal",
			effectiveIP:    "not-an-ip",
			snapshot:       system.Snapshot{AvailableIPs: []system.IPAddress{{Addr: "127.0.0.1", Family: system.FamilyIPv4}}},
			wantScanText:   "无效",
			wantRejectText: "无效",
		},
		{
			name:        "address family mismatch",
			effectiveIP: "::1",
			snapshot: system.Snapshot{
				AvailableIPs: []system.IPAddress{
					{Addr: "127.0.0.1", Family: system.FamilyIPv4},
				},
			},
			wantScanText:   "当前不存在于本机",
			wantRejectText: "当前不存在于本机",
		},
		{
			name:        "not current local ip",
			effectiveIP: "127.0.0.2",
			snapshot: system.Snapshot{
				AvailableIPs: []system.IPAddress{
					{Addr: "127.0.0.1", Family: system.FamilyIPv4},
				},
			},
			wantScanText:   "当前不存在于本机",
			wantRejectText: "当前不存在于本机",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store, tokenID, tokenHash := newDirtyEffectiveIPStore(t, tc.effectiveIP)
			server := NewServer(
				Options{
					Store:             store,
					Network:           staticSnapshotReader{snapshot: tc.snapshot},
					ReadTimeout:       time.Second,
					WriteTimeout:      time.Second,
					ChallengeTTL:      time.Second,
					HeartbeatInterval: time.Second,
				},
				slog.New(slog.NewTextHandler(io.Discard, nil)),
				"test-server",
			)

			if err := server.EnsureInitialRuntimeScan(context.Background()); err != nil {
				t.Fatalf("ensure initial runtime scan: %v", err)
			}
			reason := server.TunnelRuntimeIssues()[7]
			if !strings.Contains(reason, tc.wantScanText) {
				t.Fatalf("unexpected initial runtime issue: %#v", server.TunnelRuntimeIssues())
			}

			clientConn, done, configFrame := authenticateServerSession(t, server, tokenID, tokenHash)
			defer clientConn.Close()

			configPush, err := protocol.UnmarshalConfigPush(configFrame.Body)
			if err != nil {
				t.Fatalf("unmarshal config.push: %v", err)
			}
			if len(configPush.Tunnels) != 1 {
				t.Fatalf("expected dirty effective_ip login to still receive config snapshot, got %#v", configPush)
			}

			writeConfigAck(t, clientConn, configFrame.RequestID, configPush.ConfigVersion)

			errorFrame := readMessage(t, clientConn)
			if errorFrame.Type != protocol.TypeError {
				t.Fatalf("expected error frame, got %s", errorFrame.Type.String())
			}
			errorBody, err := protocol.UnmarshalErrorBody(errorFrame.Body)
			if err != nil {
				t.Fatalf("unmarshal error body: %v", err)
			}
			if errorBody.ErrorCode != protocol.ErrorCodeConfigApplyFailed {
				t.Fatalf("unexpected error code: %d", errorBody.ErrorCode)
			}
			if !strings.Contains(errorBody.Message, tc.wantRejectText) || !strings.Contains(errorBody.Message, "请联系管理员解决") {
				t.Fatalf("unexpected login rejection message: %q", errorBody.Message)
			}

			if _, err := readMessageWithin(clientConn, time.Second); err == nil {
				t.Fatal("expected startup rejection to close the session")
			} else if !errorsIsEOFOrClosed(err) {
				t.Fatalf("unexpected read error after startup rejection: %v", err)
			}

			select {
			case <-done:
			case <-time.After(2 * time.Second):
				t.Fatal("server connection did not exit")
			}
		})
	}
}

func newConfigAckMatrixServer(t *testing.T) (*Server, [16]byte, [32]byte) {
	t.Helper()

	tokenID := mustHex16(t, "00112233445566778899aabbccddeeff")
	tokenSecret := mustBytes32(t, "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	tokenHash := sha256.Sum256(tokenSecret[:])

	server := NewServer(
		Options{
			Repository: stubRepository{
				group: GroupRuntime{
					ID:               1,
					Name:             "group-a",
					Enabled:          true,
					EffectiveIP:      system.AnyIPv4,
					ClientSecretHash: tokenHash,
					Snapshot: ConfigSnapshot{
						Version:       99,
						GeneratedAtMs: 1234,
					},
				},
			},
			ReadTimeout:       time.Second,
			WriteTimeout:      time.Second,
			ChallengeTTL:      time.Second,
			HeartbeatInterval: time.Second,
		},
		slog.New(slog.NewTextHandler(io.Discard, nil)),
		"test-server",
	)

	return server, tokenID, tokenHash
}

func buildConfigAckFrame(t *testing.T, requestID uint32, streamID uint32, version uint64, status uint8, message string) protocol.Frame {
	t.Helper()

	body, err := protocol.MarshalConfigAck(protocol.ConfigAck{
		ConfigVersion: version,
		AppliedAtMs:   uint64(time.Now().UTC().UnixMilli()),
		Status:        status,
		Message:       message,
	})
	if err != nil {
		t.Fatalf("marshal config.ack: %v", err)
	}
	return protocol.Frame{
		Type:      protocol.TypeConfigAck,
		RequestID: requestID,
		StreamID:  streamID,
		Body:      body,
	}
}

func newDirtyEffectiveIPStore(t *testing.T, effectiveIP string) (*storage.SQL, [16]byte, [32]byte) {
	t.Helper()

	store, dbType := testdb.NewStore(t, t.Name(), "dirty-effective-ip.sqlite")
	testdb.ResetTables(t, store, dbType, testdb.FRPSTableNames...)
	t.Cleanup(func() {
		testdb.ResetTables(t, store, dbType, testdb.FRPSTableNames...)
	})
	if err := dbschema.Ensure(context.Background(), store, dbType); err != nil {
		t.Fatalf("bootstrap test schema: %v", err)
	}

	tokenID := mustHex16(t, "00112233445566778899aabbccddeeff")
	tokenSecret := mustBytes32(t, "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb")
	tokenHash := sha256.Sum256(tokenSecret[:])
	updatedAt := "2026-04-22 10:00:00.000001"

	if _, err := store.Exec(
		`
INSERT INTO proxy_groups (id, name, client_id, client_secret_hash, effective_ip, enabled, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?)
`,
		1,
		"group-a",
		hex.EncodeToString(tokenID[:]),
		hex.EncodeToString(tokenHash[:]),
		effectiveIP,
		1,
		updatedAt,
		updatedAt,
	); err != nil {
		t.Fatalf("insert proxy group: %v", err)
	}

	if _, err := store.Exec(
		`
INSERT INTO tunnels (
	id, group_id, name, protocol, remote_type, remote_start, remote_end, local_host, local_start, local_end,
	backend_tls_mode, backend_tls_server_name, backend_tls_load_system_ca, backend_tls_insecure_skip_verify,
	enabled, created_at, updated_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
`,
		7,
		1,
		"ssh",
		"tcp",
		"single",
		21001,
		21001,
		"127.0.0.1",
		22,
		22,
		"off",
		"",
		0,
		0,
		1,
		updatedAt,
		updatedAt,
	); err != nil {
		t.Fatalf("insert tunnel: %v", err)
	}

	return store, tokenID, tokenHash
}

func errorsIsEOFOrClosed(err error) bool {
	return err == io.EOF || err == net.ErrClosed || strings.Contains(strings.ToLower(err.Error()), "closed pipe")
}
