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
	manager, err := NewManager(Options{Path: path})
	if err != nil {
		t.Fatalf("new manager: %v", err)
	}

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
	if !reloaded.Initialized() {
		t.Fatal("reloaded manager should detect initialized auth.json")
	}
}

func TestManagerIssuesAndVerifiesChallenges(t *testing.T) {
	t.Parallel()

	manager, err := NewManager(Options{
		Path: filepath.Join(t.TempDir(), "auth.json"),
	})
	if err != nil {
		t.Fatalf("new manager: %v", err)
	}

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

	manager, err := NewManager(Options{
		Path: filepath.Join(t.TempDir(), "auth.json"),
	})
	if err != nil {
		t.Fatalf("new manager: %v", err)
	}

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

	manager, err := NewManager(Options{
		Path:         filepath.Join(t.TempDir(), "auth.json"),
		ChallengeTTL: time.Millisecond,
	})
	if err != nil {
		t.Fatalf("new manager: %v", err)
	}

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

	manager, err := NewManager(Options{
		Path:       filepath.Join(t.TempDir(), "auth.json"),
		SessionTTL: time.Millisecond,
	})
	if err != nil {
		t.Fatalf("new manager: %v", err)
	}

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
