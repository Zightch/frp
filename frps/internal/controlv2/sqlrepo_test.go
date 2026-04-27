package controlv2

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"testing"

	"github.com/zightch/frp/frps/internal/dbschema"
	"github.com/zightch/frp/frps/internal/storage"
	"github.com/zightch/frp/frps/internal/testdb"
)

func TestRepositoryAdapterLoadDesiredRuntimeByClientID(t *testing.T) {
	store := newControlV2TestStore(t)

	clientID := mustHex16(t, "00112233445566778899aabbccddeeff")
	secretHash := sha256.Sum256([]byte("controlv2-repo-secret"))

	if _, err := store.Exec(
		`
INSERT INTO proxy_groups (name, client_id, client_secret_hash, effective_ip, enabled, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?)
`,
		"group-a",
		hex.EncodeToString(clientID[:]),
		hex.EncodeToString(secretHash[:]),
		"127.0.0.1",
		1,
		"2026-04-27 10:00:00.000001",
		"2026-04-27 10:00:00.000001",
	); err != nil {
		t.Fatalf("insert proxy group: %v", err)
	}

	if _, err := store.Exec(
		`
INSERT INTO tunnels (
	group_id, name, protocol, remote_type, remote_start, remote_end, local_host, local_start, local_end, enabled, created_at, updated_at
) VALUES
	(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?),
	(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
`,
		1, "ssh", "tcp", "single", 20000, 20000, "127.0.0.1", 22, 22, 1, "2026-04-27 10:00:00.000002", "2026-04-27 10:00:00.000002",
		1, "dns", "udp", "range", 30000, 30009, "localhost", 53, 62, 0, "2026-04-27 10:00:00.000003", "2026-04-27 10:00:00.000003",
	); err != nil {
		t.Fatalf("insert tunnels: %v", err)
	}

	repo := NewSQLRepository(store)
	record, err := repo.LoadDesiredRuntimeByClientID(context.Background(), clientID)
	if err != nil {
		t.Fatalf("load desired runtime by client id: %v", err)
	}

	if record.GroupID != 1 {
		t.Fatalf("unexpected group id: %d", record.GroupID)
	}
	if record.Snapshot.EffectiveIP != "127.0.0.1" {
		t.Fatalf("unexpected effective ip: %q", record.Snapshot.EffectiveIP)
	}
	if len(record.Snapshot.Tunnels) != 2 {
		t.Fatalf("unexpected tunnel count: %d", len(record.Snapshot.Tunnels))
	}
	if record.Snapshot.Tunnels[0].Protocol != "tcp" {
		t.Fatalf("unexpected first tunnel protocol: %q", record.Snapshot.Tunnels[0].Protocol)
	}
	if !record.Snapshot.Tunnels[0].Enabled {
		t.Fatalf("expected first tunnel to be enabled: %#v", record.Snapshot.Tunnels[0])
	}
	if record.Snapshot.Tunnels[1].Protocol != "udp" {
		t.Fatalf("unexpected second tunnel protocol: %q", record.Snapshot.Tunnels[1].Protocol)
	}
	if record.Snapshot.Tunnels[1].Enabled {
		t.Fatalf("expected second tunnel to be disabled: %#v", record.Snapshot.Tunnels[1])
	}
	if record.Snapshot.Tunnels[1].LocalHost != "localhost" {
		t.Fatalf("unexpected second tunnel local host: %q", record.Snapshot.Tunnels[1].LocalHost)
	}
}

func newControlV2TestStore(t *testing.T) *storage.SQL {
	t.Helper()

	store, dbType := testdb.NewStore(t, t.Name(), "controlv2.sqlite")
	testdb.ResetTables(t, store, dbType, testdb.FRPSTableNames...)
	t.Cleanup(func() {
		testdb.ResetTables(t, store, dbType, testdb.FRPSTableNames...)
	})
	if err := dbschema.Ensure(context.Background(), store, dbType); err != nil {
		t.Fatalf("bootstrap controlv2 test schema: %v", err)
	}
	return store
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
