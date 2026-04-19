package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const (
	DefaultPath         = "./data/auth.json"
	defaultChallengeTTL = 2 * time.Minute
)

var (
	ErrAlreadyInitialized = errors.New("management secret is already initialized")
	ErrNotInitialized     = errors.New("management secret is not initialized")
	ErrInvalidKeyHash     = errors.New("management key hash must be 64 lowercase hex characters")
	ErrInvalidProof       = errors.New("challenge proof is invalid")
	ErrChallengeExpired   = errors.New("challenge is missing or expired")
	ErrChallengeReplayed  = errors.New("challenge has already been used")
)

type Options struct {
	Path         string
	ChallengeTTL time.Duration
}

type Manager struct {
	path         string
	challengeTTL time.Duration

	mu          sync.RWMutex
	keyHash     string
	initialized bool
	challenges  map[string]*challenge
}

type Challenge struct {
	ID        string
	Salt      string
	ExpiresAt time.Time
}

type challenge struct {
	Salt      string
	ExpiresAt time.Time
	Used      bool
}

type filePayload struct {
	KeyHash string `json:"key_hash"`
}

func NewManager(options Options) (*Manager, error) {
	path := strings.TrimSpace(options.Path)
	if path == "" {
		path = DefaultPath
	}

	ttl := options.ChallengeTTL
	if ttl <= 0 {
		ttl = defaultChallengeTTL
	}

	manager := &Manager{
		path:         path,
		challengeTTL: ttl,
		challenges:   make(map[string]*challenge),
	}

	if err := manager.load(); err != nil {
		return nil, err
	}

	return manager, nil
}

func (m *Manager) Path() string {
	return m.path
}

func (m *Manager) Initialized() bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.initialized
}

func (m *Manager) Initialize(keyHash string) error {
	normalized, err := normalizeHexDigest(keyHash)
	if err != nil {
		return err
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	if m.initialized {
		return ErrAlreadyInitialized
	}

	if err := os.MkdirAll(filepath.Dir(m.path), 0o755); err != nil {
		return fmt.Errorf("create auth directory: %w", err)
	}

	payload, err := json.Marshal(filePayload{KeyHash: normalized})
	if err != nil {
		return fmt.Errorf("encode auth.json: %w", err)
	}

	tempPath := m.path + ".tmp"
	if err := os.WriteFile(tempPath, payload, 0o600); err != nil {
		return fmt.Errorf("write auth.json temp file: %w", err)
	}
	if err := os.Rename(tempPath, m.path); err != nil {
		_ = os.Remove(tempPath)
		return fmt.Errorf("replace auth.json: %w", err)
	}

	m.keyHash = normalized
	m.initialized = true
	return nil
}

func (m *Manager) IssueChallenge() (Challenge, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if !m.initialized {
		return Challenge{}, ErrNotInitialized
	}

	now := time.Now().UTC()
	m.cleanupExpiredLocked(now)

	id, err := randomHex(16)
	if err != nil {
		return Challenge{}, fmt.Errorf("generate challenge id: %w", err)
	}

	salt, err := randomHex(16)
	if err != nil {
		return Challenge{}, fmt.Errorf("generate challenge salt: %w", err)
	}

	expiresAt := now.Add(m.challengeTTL)
	m.challenges[id] = &challenge{
		Salt:      salt,
		ExpiresAt: expiresAt,
	}

	return Challenge{
		ID:        id,
		Salt:      salt,
		ExpiresAt: expiresAt,
	}, nil
}

func (m *Manager) VerifyChallenge(challengeID, proof string) error {
	proof, err := normalizeHexDigest(proof)
	if err != nil {
		return ErrInvalidProof
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	if !m.initialized {
		return ErrNotInitialized
	}

	now := time.Now().UTC()
	m.cleanupExpiredLocked(now)

	item, ok := m.challenges[strings.TrimSpace(challengeID)]
	if !ok {
		return ErrChallengeExpired
	}
	if item.Used {
		return ErrChallengeReplayed
	}
	if now.After(item.ExpiresAt) {
		item.Used = true
		return ErrChallengeExpired
	}

	expected := buildProof(m.keyHash, item.Salt)
	item.Used = true
	if subtle.ConstantTimeCompare([]byte(expected), []byte(proof)) != 1 {
		return ErrInvalidProof
	}

	return nil
}

func (m *Manager) load() error {
	raw, err := os.ReadFile(m.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read auth.json: %w", err)
	}

	var payload filePayload
	if err := json.Unmarshal(raw, &payload); err != nil {
		return fmt.Errorf("decode auth.json: %w", err)
	}

	normalized, err := normalizeHexDigest(payload.KeyHash)
	if err != nil {
		return fmt.Errorf("decode auth.json: %w", err)
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	m.keyHash = normalized
	m.initialized = true
	return nil
}

func (m *Manager) cleanupExpiredLocked(now time.Time) {
	for id, item := range m.challenges {
		if now.After(item.ExpiresAt) {
			delete(m.challenges, id)
		}
	}
}

func normalizeHexDigest(value string) (string, error) {
	value = strings.ToLower(strings.TrimSpace(value))
	if len(value) != sha256.Size*2 {
		return "", ErrInvalidKeyHash
	}
	if _, err := hex.DecodeString(value); err != nil {
		return "", ErrInvalidKeyHash
	}
	return value, nil
}

func randomHex(size int) (string, error) {
	buf := make([]byte, size)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}

func buildProof(keyHash, salt string) string {
	sum := sha256.Sum256([]byte(keyHash + salt))
	return hex.EncodeToString(sum[:])
}
