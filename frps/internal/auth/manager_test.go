package auth

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestManagerInitializesAndReloads(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "auth.json")
	manager := newTestManager(t, Options{Path: path})

	if manager.Initialized() {
		t.Fatal("manager should start uninitialized when auth.json is missing")
	}

	keyHash := "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	if err := manager.Initialize(keyHash); err != nil {
		t.Fatalf("initialize manager: %v", err)
	}
	if !manager.Initialized() {
		t.Fatal("manager should be initialized after writing auth.json")
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read auth.json: %v", err)
	}
	if string(raw) != `{"key_hash":"`+keyHash+`"}` {
		t.Fatalf("unexpected auth.json content: %s", raw)
	}

	reloaded, err := NewManager(Options{Path: path})
	if err != nil {
		t.Fatalf("reload manager: %v", err)
	}
	t.Cleanup(func() {
		_ = reloaded.Close()
	})
	if !reloaded.Initialized() {
		t.Fatal("reloaded manager should detect initialized auth.json")
	}
}

func TestManagerIssuesAndVerifiesChallenges(t *testing.T) {
	t.Parallel()

	manager := newTestManager(t, Options{
		Path: filepath.Join(t.TempDir(), "auth.json"),
	})

	keyHash := "abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789"
	if err := manager.Initialize(keyHash); err != nil {
		t.Fatalf("initialize manager: %v", err)
	}

	first, err := manager.IssueChallenge()
	if err != nil {
		t.Fatalf("issue challenge: %v", err)
	}
	if err := manager.VerifyChallenge(first.ID, buildProof(keyHash, first.Salt)); err != nil {
		t.Fatalf("verify challenge: %v", err)
	}
	if err := manager.VerifyChallenge(first.ID, buildProof(keyHash, first.Salt)); !errors.Is(err, ErrChallengeReplayed) {
		t.Fatalf("expected replay error, got %v", err)
	}

	second, err := manager.IssueChallenge()
	if err != nil {
		t.Fatalf("issue second challenge: %v", err)
	}
	if err := manager.VerifyChallenge(second.ID, first.Salt+first.Salt); !errors.Is(err, ErrInvalidProof) {
		t.Fatalf("expected invalid proof error, got %v", err)
	}
}

func TestManagerCreatesValidatesAndRevokesSessions(t *testing.T) {
	t.Parallel()

	manager := newTestManager(t, Options{
		Path: filepath.Join(t.TempDir(), "auth.json"),
	})

	keyHash := "abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789"
	if err := manager.Initialize(keyHash); err != nil {
		t.Fatalf("initialize manager: %v", err)
	}

	challenge, err := manager.IssueChallenge()
	if err != nil {
		t.Fatalf("issue challenge: %v", err)
	}

	session, token, err := manager.Login(challenge.ID, buildProof(keyHash, challenge.Salt))
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	if token == "" {
		t.Fatal("login must return a session token")
	}
	if session.ExpiresAt.IsZero() {
		t.Fatal("session expiration must be set")
	}

	validated, err := manager.ValidateSession(token)
	if err != nil {
		t.Fatalf("validate session: %v", err)
	}
	if !validated.ExpiresAt.Equal(session.ExpiresAt) {
		t.Fatalf("unexpected session expiration: got %v want %v", validated.ExpiresAt, session.ExpiresAt)
	}

	manager.Logout(token)
	if _, err := manager.ValidateSession(token); !errors.Is(err, ErrSessionExpired) {
		t.Fatalf("expected revoked session to be invalid, got %v", err)
	}
}

func TestManagerRejectsExpiredChallenge(t *testing.T) {
	t.Parallel()

	manager := newTestManager(t, Options{
		Path:         filepath.Join(t.TempDir(), "auth.json"),
		ChallengeTTL: time.Millisecond,
	})

	keyHash := "fedcba9876543210fedcba9876543210fedcba9876543210fedcba9876543210"
	if err := manager.Initialize(keyHash); err != nil {
		t.Fatalf("initialize manager: %v", err)
	}

	challenge, err := manager.IssueChallenge()
	if err != nil {
		t.Fatalf("issue challenge: %v", err)
	}

	time.Sleep(10 * time.Millisecond)
	if err := manager.VerifyChallenge(challenge.ID, buildProof(keyHash, challenge.Salt)); !errors.Is(err, ErrChallengeExpired) {
		t.Fatalf("expected expired challenge error, got %v", err)
	}
}

func TestManagerRejectsExpiredSession(t *testing.T) {
	t.Parallel()

	manager := newTestManager(t, Options{
		Path:       filepath.Join(t.TempDir(), "auth.json"),
		SessionTTL: time.Millisecond,
	})

	keyHash := "fedcba9876543210fedcba9876543210fedcba9876543210fedcba9876543210"
	if err := manager.Initialize(keyHash); err != nil {
		t.Fatalf("initialize manager: %v", err)
	}

	challenge, err := manager.IssueChallenge()
	if err != nil {
		t.Fatalf("issue challenge: %v", err)
	}

	_, token, err := manager.Login(challenge.ID, buildProof(keyHash, challenge.Salt))
	if err != nil {
		t.Fatalf("login: %v", err)
	}

	time.Sleep(10 * time.Millisecond)
	if _, err := manager.ValidateSession(token); !errors.Is(err, ErrSessionExpired) {
		t.Fatalf("expected expired session error, got %v", err)
	}
}

func TestManagerResetsAfterAuthFileDeletion(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "auth.json")
	manager := newTestManager(t, Options{
		Path:          path,
		WatchInterval: 5 * time.Millisecond,
	})

	keyHash := "00112233445566778899aabbccddeeff00112233445566778899aabbccddeeff"
	if err := manager.Initialize(keyHash); err != nil {
		t.Fatalf("initialize manager: %v", err)
	}

	challenge, err := manager.IssueChallenge()
	if err != nil {
		t.Fatalf("issue challenge: %v", err)
	}

	_, token, err := manager.Login(challenge.ID, buildProof(keyHash, challenge.Salt))
	if err != nil {
		t.Fatalf("login: %v", err)
	}

	if err := os.Remove(path); err != nil {
		t.Fatalf("remove auth file: %v", err)
	}

	deadline := time.Now().Add(250 * time.Millisecond)
	for manager.Initialized() && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if manager.Initialized() {
		t.Fatal("manager should reset after auth.json deletion")
	}

	if _, err := manager.ValidateSession(token); !errors.Is(err, ErrNotInitialized) {
		t.Fatalf("expected session to become invalid after auth reset, got %v", err)
	}
	if _, err := manager.IssueChallenge(); !errors.Is(err, ErrNotInitialized) {
		t.Fatalf("expected challenge issuance to fail after auth reset, got %v", err)
	}

	reinitializedKeyHash := "ffeeddccbbaa99887766554433221100ffeeddccbbaa99887766554433221100"
	if err := manager.Initialize(reinitializedKeyHash); err != nil {
		t.Fatalf("reinitialize manager: %v", err)
	}
	if !manager.Initialized() {
		t.Fatal("manager should allow initialization again after auth.json deletion")
	}
}

func newTestManager(t *testing.T, options Options) *Manager {
	t.Helper()

	manager, err := NewManager(options)
	if err != nil {
		t.Fatalf("new manager: %v", err)
	}
	t.Cleanup(func() {
		_ = manager.Close()
	})
	return manager
}
