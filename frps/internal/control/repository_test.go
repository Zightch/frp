package control

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zightch/frp/frps/internal/storage"
	_ "github.com/zightch/frp/frps/internal/storage/drivers"
	"github.com/zightch/frp/frps/pkg/protocol"
)

func TestSQLRepositoryLoadGroupRuntimeByClientID(t *testing.T) {
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "control.sqlite"))
	if err != nil {
		t.Fatalf("open sqlite database: %v", err)
	}
	defer db.Close()

	store, err := storage.NewSQLWithConn(t.Name(), db)
	if err != nil {
		t.Fatalf("create store: %v", err)
	}
	defer store.Close()

	for _, statement := range []string{
		`
CREATE TABLE proxy_groups (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	name TEXT NOT NULL,
	client_id TEXT NOT NULL,
	client_secret_hash TEXT NOT NULL,
	effective_ip TEXT NOT NULL,
	enabled INTEGER NOT NULL,
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
	enabled INTEGER NOT NULL,
	updated_at TEXT NOT NULL
)`,
	} {
		if _, err := store.Exec(statement); err != nil {
			t.Fatalf("bootstrap test schema: %v", err)
		}
	}

	tokenID := mustHex16(t, "00112233445566778899aabbccddeeff")
	tokenSecret := mustBytes32(t, "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	tokenHash := sha256.Sum256(tokenSecret[:])

	if _, err := store.Exec(
		`
INSERT INTO proxy_groups (name, client_id, client_secret_hash, effective_ip, enabled, updated_at)
VALUES (?, ?, ?, ?, ?, ?)
`,
		"group-a",
		hex.EncodeToString(tokenID[:]),
		hex.EncodeToString(tokenHash[:]),
		"127.0.0.1",
		1,
		"2026-04-18 10:00:00.000001",
	); err != nil {
		t.Fatalf("insert proxy group: %v", err)
	}

	if _, err := store.Exec(
		`
INSERT INTO tunnels (
	group_id, name, protocol, remote_type, remote_start, remote_end, local_host, local_start, local_end, enabled, updated_at
) VALUES
	(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?),
	(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
`,
		1, "ssh", "tcp", "single", 20000, 20000, "127.0.0.1", 22, 22, 1, "2026-04-18 10:00:00.000002",
		1, "range", "udp", "range", 30000, 30009, "localhost", 40000, 40009, 0, "2026-04-18 10:00:00.000003",
	); err != nil {
		t.Fatalf("insert tunnels: %v", err)
	}

	repo := NewRepository(store)
	group, err := repo.LoadGroupRuntimeByClientID(context.Background(), tokenID)
	if err != nil {
		t.Fatalf("load group runtime: %v", err)
	}

	if !group.Enabled {
		t.Fatal("expected group to be enabled")
	}
	if group.EffectiveIP != "127.0.0.1" {
		t.Fatalf("unexpected effective ip: %q", group.EffectiveIP)
	}
	if group.ClientSecretHash != tokenHash {
		t.Fatal("unexpected client secret hash")
	}
	if group.Snapshot.Version == 0 || group.Snapshot.GeneratedAtMs == 0 {
		t.Fatalf("expected snapshot metadata, got %#v", group.Snapshot)
	}
	if len(group.Snapshot.Tunnels) != 2 {
		t.Fatalf("unexpected tunnel count: %d", len(group.Snapshot.Tunnels))
	}
	if group.Snapshot.Tunnels[0].Protocol != protocol.ProtocolTCP {
		t.Fatalf("unexpected first tunnel protocol: %d", group.Snapshot.Tunnels[0].Protocol)
	}
	if group.Snapshot.Tunnels[1].TunnelFlags&protocol.TunnelFlagRange == 0 {
		t.Fatalf("expected second tunnel to be marked as range: %#v", group.Snapshot.Tunnels[1])
	}
}

func TestSQLRepositoryListGroupRuntimes(t *testing.T) {
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "control-list.sqlite"))
	if err != nil {
		t.Fatalf("open sqlite database: %v", err)
	}
	defer db.Close()

	store, err := storage.NewSQLWithConn(t.Name(), db)
	if err != nil {
		t.Fatalf("create store: %v", err)
	}
	defer store.Close()

	for _, statement := range []string{
		`
CREATE TABLE proxy_groups (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	name TEXT NOT NULL,
	client_id TEXT NOT NULL,
	client_secret_hash TEXT NOT NULL,
	effective_ip TEXT NOT NULL,
	enabled INTEGER NOT NULL,
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
	enabled INTEGER NOT NULL,
	updated_at TEXT NOT NULL
)`,
	} {
		if _, err := store.Exec(statement); err != nil {
			t.Fatalf("bootstrap test schema: %v", err)
		}
	}

	if _, err := store.Exec(
		`
INSERT INTO proxy_groups (name, client_id, client_secret_hash, effective_ip, enabled, updated_at)
VALUES
	(?, ?, ?, ?, ?, ?),
	(?, ?, ?, ?, ?, ?)
`,
		"group-a", "00112233445566778899aabbccddeeff", strings.Repeat("aa", 32), "127.0.0.1", 1, "2026-04-18 10:00:00.000001",
		"group-b", "ffeeddccbbaa99887766554433221100", strings.Repeat("bb", 32), "0.0.0.0", 0, "2026-04-18 10:00:00.000002",
	); err != nil {
		t.Fatalf("insert proxy groups: %v", err)
	}

	if _, err := store.Exec(
		`
INSERT INTO tunnels (
	group_id, name, protocol, remote_type, remote_start, remote_end, local_host, local_start, local_end, enabled, updated_at
) VALUES
	(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?),
	(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
`,
		1, "ssh", "tcp", "single", 20000, 20000, "127.0.0.1", 22, 22, 1, "2026-04-18 10:00:00.000003",
		2, "dns", "udp", "single", 30000, 30000, "127.0.0.1", 53, 53, 1, "2026-04-18 10:00:00.000004",
	); err != nil {
		t.Fatalf("insert tunnels: %v", err)
	}

	repo := NewRepository(store)
	groups, err := repo.ListGroupRuntimes(context.Background())
	if err != nil {
		t.Fatalf("list group runtimes: %v", err)
	}

	if len(groups) != 2 {
		t.Fatalf("unexpected group count: %d", len(groups))
	}
	if groups[0].ID != 1 || groups[1].ID != 2 {
		t.Fatalf("unexpected group ordering: %#v", groups)
	}
	if len(groups[0].Snapshot.Tunnels) != 1 || groups[0].Snapshot.Tunnels[0].TunnelID != 1 {
		t.Fatalf("unexpected first group snapshot: %#v", groups[0].Snapshot)
	}
	if len(groups[1].Snapshot.Tunnels) != 1 || groups[1].Snapshot.Tunnels[0].TunnelID != 2 {
		t.Fatalf("unexpected second group snapshot: %#v", groups[1].Snapshot)
	}
}

func mustHex16(t *testing.T, value string) [16]byte {
	t.Helper()

	var decoded [16]byte
	raw, err := hex.DecodeString(value)
	if err != nil {
		t.Fatalf("decode hex16: %v", err)
	}
	copy(decoded[:], raw)
	return decoded
}

func mustBytes32(t *testing.T, value string) [32]byte {
	t.Helper()

	var decoded [32]byte
	if len(value) != len(decoded)*2 {
		t.Fatalf("expected 64 chars, got %d", len(value))
	}
	raw, err := hex.DecodeString(value)
	if err != nil {
		t.Fatalf("decode hex32: %v", err)
	}
	copy(decoded[:], raw)
	return decoded
}
