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
	defaultPendingTTL    = 2 * time.Minute
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
	ErrTakeoverStale      = errors.New("management takeover page is stale")
)

type Options struct {
	Path          string
	ChallengeTTL  time.Duration
	PendingTTL    time.Duration
	SessionTTL    time.Duration
	WatchInterval time.Duration
}

type Manager struct {
	path          string
	challengeTTL  time.Duration
	pendingTTL    time.Duration
	sessionTTL    time.Duration
	watchInterval time.Duration

	mu                 sync.RWMutex
	keyHash            string
	initialized        bool
	challenges         map[string]*challenge
	pendingLogins      map[string]*pendingLogin
	sessions           map[string]*session
	activeSessionToken string
	activeGeneration   uint64

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

type LoginResult struct {
	Occupied           bool
	Session            Session
	Token              string
	PendingLoginToken  string
	ObservedGeneration uint64
}

type challenge struct {
	Salt      string
	ExpiresAt time.Time
	Used      bool
}

type session struct {
	ExpiresAt  time.Time
	Generation uint64
}

type pendingLogin struct {
	ExpiresAt          time.Time
	ObservedGeneration uint64
}

type filePayload struct {
	KeyHash string `json:"key_hash"`
}

func NewManager(options Options) (*Manager, error) {
	path := strings.TrimSpace(options.Path)
	if path == "" {
		path = DefaultPath
	}

	challengeTTL := options.ChallengeTTL
	if challengeTTL <= 0 {
		challengeTTL = defaultChallengeTTL
	}

	pendingTTL := options.PendingTTL
	if pendingTTL <= 0 {
		pendingTTL = defaultPendingTTL
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
		challengeTTL:  challengeTTL,
		pendingTTL:    pendingTTL,
		sessionTTL:    sessionTTL,
		watchInterval: watchInterval,
		challenges:    make(map[string]*challenge),
		pendingLogins: make(map[string]*pendingLogin),
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
	m.pendingLogins = make(map[string]*pendingLogin)
	m.sessions = make(map[string]*session)
	m.activeSessionToken = ""
	m.activeGeneration = 0
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

	return m.issueSessionLocked(now)
}

func (m *Manager) StartLogin(challengeID, proof string) (LoginResult, error) {
	if err := m.syncDeletedState(); err != nil {
		return LoginResult{}, err
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	now := time.Now().UTC()
	if err := m.verifyChallengeLocked(now, challengeID, proof); err != nil {
		return LoginResult{}, err
	}

	m.cleanupExpiredLocked(now)
	if m.activeSessionToken == "" {
		session, token, err := m.issueSessionLocked(now)
		if err != nil {
			return LoginResult{}, err
		}
		return LoginResult{
			Session: session,
			Token:   token,
		}, nil
	}

	token, err := randomHex(32)
	if err != nil {
		return LoginResult{}, fmt.Errorf("generate pending login token: %w", err)
	}

	m.pendingLogins[token] = &pendingLogin{
		ExpiresAt:          now.Add(m.pendingTTL),
		ObservedGeneration: m.activeGeneration,
	}
	return LoginResult{
		Occupied:           true,
		PendingLoginToken:  token,
		ObservedGeneration: m.activeGeneration,
	}, nil
}

func (m *Manager) Takeover(pendingLoginToken string, observedGeneration uint64) (Session, string, error) {
	if err := m.syncDeletedState(); err != nil {
		return Session{}, "", err
	}

	pendingLoginToken = normalizeSessionToken(pendingLoginToken)
	if pendingLoginToken == "" || observedGeneration == 0 {
		return Session{}, "", ErrTakeoverStale
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	if !m.initialized {
		return Session{}, "", ErrNotInitialized
	}

	now := time.Now().UTC()
	m.cleanupExpiredLocked(now)

	item, ok := m.pendingLogins[pendingLoginToken]
	if !ok {
		return Session{}, "", ErrTakeoverStale
	}
	delete(m.pendingLogins, pendingLoginToken)

	if item.ObservedGeneration != observedGeneration {
		return Session{}, "", ErrTakeoverStale
	}
	if m.activeSessionToken == "" || m.activeGeneration != observedGeneration {
		return Session{}, "", ErrTakeoverStale
	}

	return m.issueSessionLocked(now)
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
	if token == m.activeSessionToken {
		m.activeSessionToken = ""
		m.pendingLogins = make(map[string]*pendingLogin)
	}
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
	for token, item := range m.pendingLogins {
		if now.After(item.ExpiresAt) {
			delete(m.pendingLogins, token)
		}
	}

	activeExpired := false
	for token, item := range m.sessions {
		if now.After(item.ExpiresAt) {
			delete(m.sessions, token)
			if token == m.activeSessionToken {
				activeExpired = true
			}
		}
	}
	if activeExpired || (m.activeSessionToken != "" && m.sessions[m.activeSessionToken] == nil) {
		m.activeSessionToken = ""
		m.pendingLogins = make(map[string]*pendingLogin)
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
	m.pendingLogins = make(map[string]*pendingLogin)
	m.sessions = make(map[string]*session)
	m.activeSessionToken = ""
	m.activeGeneration = 0
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

func (m *Manager) issueSessionLocked(now time.Time) (Session, string, error) {
	token, err := randomHex(32)
	if err != nil {
		return Session{}, "", fmt.Errorf("generate session token: %w", err)
	}

	m.activeGeneration++
	expiresAt := now.Add(m.sessionTTL)
	m.sessions = map[string]*session{
		token: &session{
			ExpiresAt:  expiresAt,
			Generation: m.activeGeneration,
		},
	}
	m.activeSessionToken = token
	m.pendingLogins = make(map[string]*pendingLogin)
	return Session{ExpiresAt: expiresAt}, token, nil
}
