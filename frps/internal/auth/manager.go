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
	DefaultPath          = "./data/auth.json"
	defaultChallengeTTL  = 2 * time.Minute
	defaultSessionTTL    = 12 * time.Hour
	defaultWatchInterval = time.Second
)

var (
	ErrAlreadyInitialized = errors.New("management secret is already initialized")
	ErrNotInitialized     = errors.New("management secret is not initialized")
	ErrInvalidKeyHash     = errors.New("management key hash must be 64 lowercase hex characters")
	ErrInvalidProof       = errors.New("challenge proof is invalid")
	ErrChallengeExpired   = errors.New("challenge is missing or expired")
	ErrChallengeReplayed  = errors.New("challenge has already been used")
	ErrSessionRequired    = errors.New("management session is required")
	ErrSessionExpired     = errors.New("management session is invalid or expired")
)

type Options struct {
	Path          string
	ChallengeTTL  time.Duration
	SessionTTL    time.Duration
	WatchInterval time.Duration
}

type Manager struct {
	path          string
	challengeTTL  time.Duration
	sessionTTL    time.Duration
	watchInterval time.Duration

	mu          sync.RWMutex
	keyHash     string
	initialized bool
	challenges  map[string]*challenge
	sessions    map[string]*session

	stopCh    chan struct{}
	doneCh    chan struct{}
	closeOnce sync.Once
}

type Challenge struct {
	ID        string
	Salt      string
	ExpiresAt time.Time
}

type Session struct {
	ExpiresAt time.Time
}

type challenge struct {
	Salt      string
	ExpiresAt time.Time
	Used      bool
}

type session struct {
	ExpiresAt time.Time
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

	sessionTTL := options.SessionTTL
	if sessionTTL <= 0 {
		sessionTTL = defaultSessionTTL
	}

	watchInterval := options.WatchInterval
	if watchInterval == 0 {
		watchInterval = defaultWatchInterval
	}

	manager := &Manager{
		path:          path,
		challengeTTL:  ttl,
		sessionTTL:    sessionTTL,
		watchInterval: watchInterval,
		challenges:    make(map[string]*challenge),
		sessions:      make(map[string]*session),
		stopCh:        make(chan struct{}),
		doneCh:        make(chan struct{}),
	}

	if err := manager.load(); err != nil {
		return nil, err
	}

	if manager.watchInterval > 0 {
		go manager.watchLoop()
	} else {
		close(manager.doneCh)
	}

	return manager, nil
}

func (m *Manager) Path() string {
	return m.path
}

func (m *Manager) Initialized() bool {
	_ = m.syncDeletedState()

	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.initialized
}

func (m *Manager) Initialize(keyHash string) error {
	if err := m.syncDeletedState(); err != nil {
		return err
	}

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
	m.challenges = make(map[string]*challenge)
	m.sessions = make(map[string]*session)
	return nil
}

func (m *Manager) IssueChallenge() (Challenge, error) {
	if err := m.syncDeletedState(); err != nil {
		return Challenge{}, err
	}

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

func (m *Manager) Login(challengeID, proof string) (Session, string, error) {
	if err := m.syncDeletedState(); err != nil {
		return Session{}, "", err
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	now := time.Now().UTC()
	if err := m.verifyChallengeLocked(now, challengeID, proof); err != nil {
		return Session{}, "", err
	}

	token, err := randomHex(32)
	if err != nil {
		return Session{}, "", fmt.Errorf("generate session token: %w", err)
	}

	expiresAt := now.Add(m.sessionTTL)
	m.sessions[token] = &session{ExpiresAt: expiresAt}
	return Session{ExpiresAt: expiresAt}, token, nil
}

func (m *Manager) VerifyChallenge(challengeID, proof string) error {
	if err := m.syncDeletedState(); err != nil {
		return err
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	now := time.Now().UTC()
	return m.verifyChallengeLocked(now, challengeID, proof)
}

func (m *Manager) ValidateSession(token string) (Session, error) {
	if err := m.syncDeletedState(); err != nil {
		return Session{}, err
	}

	token = normalizeSessionToken(token)
	if token == "" {
		return Session{}, ErrSessionRequired
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	if !m.initialized {
		return Session{}, ErrNotInitialized
	}

	now := time.Now().UTC()
	m.cleanupExpiredLocked(now)

	item, ok := m.sessions[token]
	if !ok {
		return Session{}, ErrSessionExpired
	}
	if now.After(item.ExpiresAt) {
		delete(m.sessions, token)
		return Session{}, ErrSessionExpired
	}

	return Session{ExpiresAt: item.ExpiresAt}, nil
}

func (m *Manager) Logout(token string) {
	_ = m.syncDeletedState()

	token = normalizeSessionToken(token)
	if token == "" {
		return
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.sessions, token)
}

func (m *Manager) Close() error {
	m.closeOnce.Do(func() {
		close(m.stopCh)
		<-m.doneCh
	})
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
	for token, item := range m.sessions {
		if now.After(item.ExpiresAt) {
			delete(m.sessions, token)
		}
	}
}

func (m *Manager) watchLoop() {
	ticker := time.NewTicker(m.watchInterval)
	defer ticker.Stop()
	defer close(m.doneCh)

	for {
		select {
		case <-ticker.C:
			_ = m.syncDeletedState()
		case <-m.stopCh:
			return
		}
	}
}

func (m *Manager) syncDeletedState() error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if _, err := os.Stat(m.path); err == nil {
		return nil
	} else if errors.Is(err, os.ErrNotExist) {
		m.resetLocked()
		return nil
	} else {
		return fmt.Errorf("stat auth.json: %w", err)
	}
}

func (m *Manager) resetLocked() {
	m.keyHash = ""
	m.initialized = false
	m.challenges = make(map[string]*challenge)
	m.sessions = make(map[string]*session)
}

func (m *Manager) verifyChallengeLocked(now time.Time, challengeID, proof string) error {
	proof, err := normalizeHexDigest(proof)
	if err != nil {
		return ErrInvalidProof
	}
	if !m.initialized {
		return ErrNotInitialized
	}

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

func normalizeSessionToken(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	if len(value) != 64 {
		return ""
	}
	if _, err := hex.DecodeString(value); err != nil {
		return ""
	}
	return value
}
