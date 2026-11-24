// Package auth implements single-administrator authentication with bcrypt
// password hashes, in-memory sessions and CSRF tokens.
package auth

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/ph0nkj/mailcontroller/internal/store"
)

const (
	// SettingPasswordHash is the settings key holding the bcrypt admin hash.
	SettingPasswordHash = "admin.password_hash"
	// SessionCookieName is the cookie carrying the session token.
	SessionCookieName = "mc_session"
	// SessionTTL is the lifetime of an authenticated session.
	SessionTTL = 12 * time.Hour

	maxLoginAttempts = 5
	attemptWindow    = 15 * time.Minute
)

var (
	// ErrInvalidCredentials is returned for an unknown password.
	ErrInvalidCredentials = errors.New("auth: invalid credentials")
	// ErrRateLimited is returned when too many failed logins occurred.
	ErrRateLimited = errors.New("auth: too many login attempts")
	// ErrNoSession is returned when a session token is missing or expired.
	ErrNoSession = errors.New("auth: no valid session")
	// ErrCSRF is returned when a CSRF token does not match.
	ErrCSRF = errors.New("auth: invalid csrf token")
)

// SettingsStore is the small persistence surface required by the auth service.
type SettingsStore interface {
	Get(ctx context.Context, key string) ([]byte, error)
	Set(ctx context.Context, key string, value []byte) error
}

// Session is an authenticated browser session.
type Session struct {
	Token     string
	CSRF      string
	ExpiresAt time.Time
}

type attemptState struct {
	count int
	first time.Time
}

// Service authenticates the single administrator and tracks sessions.
type Service struct {
	settings   SettingsStore
	log        *slog.Logger
	hash       []byte
	secure     bool
	sessionTTL time.Duration
	now        func() time.Time

	mu       sync.Mutex
	sessions map[string]Session
	attempts map[string]*attemptState
}

// New loads or creates the administrator password hash and returns a service.
// The password is hashed only on first start; later changes to the environment
// variable do not rotate the stored hash.
func New(ctx context.Context, settings SettingsStore, adminPassword string, secure bool, log *slog.Logger) (*Service, error) {
	if settings == nil {
		return nil, errors.New("auth: settings store is nil")
	}
	if log == nil {
		log = slog.Default()
	}
	svc := &Service{
		settings:   settings,
		log:        log,
		secure:     secure,
		sessionTTL: SessionTTL,
		now:        time.Now,
		sessions:   make(map[string]Session),
		attempts:   make(map[string]*attemptState),
	}
	hash, err := settings.Get(ctx, SettingPasswordHash)
	switch {
	case errors.Is(err, store.ErrNotFound) || len(hash) == 0:
		if len(adminPassword) < 8 {
			return nil, errors.New("auth: MC_ADMIN_PASSWORD must be at least 8 characters on first start")
		}
		generated, hashErr := bcrypt.GenerateFromPassword([]byte(adminPassword), bcrypt.DefaultCost)
		if hashErr != nil {
			return nil, fmt.Errorf("auth: hash admin password: %w", hashErr)
		}
		if setErr := settings.Set(ctx, SettingPasswordHash, generated); setErr != nil {
			return nil, fmt.Errorf("auth: store admin password hash: %w", setErr)
		}
		hash = generated
		log.Info("initial administrator password stored")
	case err != nil:
		return nil, fmt.Errorf("auth: load admin password hash: %w", err)
	}
	svc.hash = hash
	return svc, nil
}

// Login validates a password and creates a session. ip is used for rate
// limiting failed attempts.
func (s *Service) Login(_ context.Context, password, ip string) (Session, error) {
	ip = normalizeIP(ip)
	if !s.allowAttempt(ip) {
		return Session{}, ErrRateLimited
	}
	if err := bcrypt.CompareHashAndPassword(s.hash, []byte(password)); err != nil {
		s.recordFailure(ip)
		return Session{}, ErrInvalidCredentials
	}
	s.resetAttempts(ip)

	token, err := randomToken()
	if err != nil {
		return Session{}, fmt.Errorf("auth: create session token: %w", err)
	}
	csrf, err := randomToken()
	if err != nil {
		return Session{}, fmt.Errorf("auth: create csrf token: %w", err)
	}
	session := Session{Token: token, CSRF: csrf, ExpiresAt: s.now().Add(s.sessionTTL)}
	s.mu.Lock()
	s.sessions[token] = session
	s.pruneLocked()
	s.mu.Unlock()
	return session, nil
}

// Session returns a live session for token.
func (s *Service) Session(token string) (Session, bool) {
	if token == "" {
		return Session{}, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	session, ok := s.sessions[token]
	if !ok {
		return Session{}, false
	}
	if s.now().After(session.ExpiresAt) {
		delete(s.sessions, token)
		return Session{}, false
	}
	return session, true
}

// Logout invalidates a session token. Unknown tokens are ignored.
func (s *Service) Logout(token string) {
	s.mu.Lock()
	delete(s.sessions, token)
	s.mu.Unlock()
}

// ValidateCSRF checks a request token against the session token in constant
// time.
func ValidateCSRF(session Session, token string) error {
	if token == "" || subtle.ConstantTimeCompare([]byte(session.CSRF), []byte(token)) != 1 {
		return ErrCSRF
	}
	return nil
}

// Secure reports whether session cookies must set the Secure attribute.
func (s *Service) Secure() bool { return s.secure }

// SessionTTL returns the configured session lifetime.
func (s *Service) SessionTTL() time.Duration { return s.sessionTTL }

func (s *Service) allowAttempt(ip string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	state, ok := s.attempts[ip]
	if !ok {
		return true
	}
	if s.now().Sub(state.first) > attemptWindow {
		delete(s.attempts, ip)
		return true
	}
	return state.count < maxLoginAttempts
}

func (s *Service) recordFailure(ip string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	state, ok := s.attempts[ip]
	now := s.now()
	if !ok || now.Sub(state.first) > attemptWindow {
		s.attempts[ip] = &attemptState{count: 1, first: now}
		return
	}
	state.count++
}

func (s *Service) resetAttempts(ip string) {
	s.mu.Lock()
	delete(s.attempts, ip)
	s.mu.Unlock()
}

func (s *Service) pruneLocked() {
	now := s.now()
	for token, session := range s.sessions {
		if now.After(session.ExpiresAt) {
			delete(s.sessions, token)
		}
	}
}

func randomToken() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}

func normalizeIP(remote string) string {
	host, _, err := net.SplitHostPort(remote)
	if err == nil {
		return host
	}
	return strings.TrimSpace(remote)
}
