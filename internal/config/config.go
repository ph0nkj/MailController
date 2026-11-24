// Package config loads and validates MailController configuration from the
// environment.
package config

import (
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"net/mail"
	"net/url"
	"os"
	"strconv"
	"strings"
)

// Default values for optional settings.
const (
	DefaultAddr             = ":8080"
	DefaultDBPath           = "/data/mc.db"
	DefaultBaseURL          = "http://localhost:8080"
	DefaultSendRatePerMin   = 60
	DefaultDailySendLimit   = 500
	DefaultLLMMaxMessages   = 200
	DefaultAgentMaxMessages = 10000
	DefaultAgentMaxSteps    = 5
	DefaultLLMBodyChars     = 2000
)

// Config holds every runtime setting required by the service.
type Config struct {
	// Addr is the TCP address the HTTP server listens on.
	Addr string
	// DBPath is the filesystem path of the SQLite database.
	DBPath string
	// SecretKey is the 32-byte AES-256 key used to encrypt secrets at rest.
	SecretKey []byte
	// AdminPassword is the initial administrator password.
	AdminPassword string
	// BaseURL is the externally visible base URL of the service.
	BaseURL string
	// SendRatePerMin caps outgoing campaign messages per minute.
	SendRatePerMin int
	// DailySendLimit caps outgoing campaign messages per calendar day.
	DailySendLimit int
	// LLMMaxMessages caps how many messages a single LLM request may inspect.
	LLMMaxMessages int
	// AgentMaxMessages caps how many messages one mutating agent tool may touch.
	AgentMaxMessages int
	// AgentMaxSteps caps the number of planning/tool loops per agent run.
	AgentMaxSteps int
	// LLMBodyChars caps message body characters sent to an LLM.
	LLMBodyChars int
	// UnsubscribeEnabled controls whether campaign mail includes unsubscribe
	// headers and a footer.
	UnsubscribeEnabled bool
	// UnsubscribeEmail is the mailbox used for unsubscribe requests.
	UnsubscribeEmail string
	// GoogleClientID is an optional OAuth client ID supplied via environment.
	GoogleClientID string
	// GoogleClientSecret is an optional OAuth client secret supplied via
	// environment.
	GoogleClientSecret string
	// LogLevel is the minimum structured log level.
	LogLevel slog.Level
	// CookieSecure forces the Secure flag on session cookies.
	CookieSecure bool
}

// Load reads configuration from the process environment, applies defaults and
// validates the result.
func Load() (Config, error) {
	cfg := Config{
		Addr:               env("MC_ADDR", DefaultAddr),
		DBPath:             env("MC_DB_PATH", DefaultDBPath),
		AdminPassword:      os.Getenv("MC_ADMIN_PASSWORD"),
		BaseURL:            strings.TrimRight(env("MC_BASE_URL", DefaultBaseURL), "/"),
		UnsubscribeEmail:   strings.TrimSpace(os.Getenv("MC_UNSUBSCRIBE_EMAIL")),
		UnsubscribeEnabled: envBool("MC_UNSUBSCRIBE_ENABLED", true),
		GoogleClientID:     strings.TrimSpace(os.Getenv("GOOGLE_CLIENT_ID")),
		GoogleClientSecret: strings.TrimSpace(os.Getenv("GOOGLE_CLIENT_SECRET")),
	}

	var err error
	if cfg.SecretKey, err = parseSecretKey(os.Getenv("MC_SECRET_KEY")); err != nil {
		return Config{}, err
	}
	if cfg.SendRatePerMin, err = envInt("MC_SEND_RATE_PER_MIN", DefaultSendRatePerMin); err != nil {
		return Config{}, err
	}
	if cfg.DailySendLimit, err = envInt("MC_DAILY_SEND_LIMIT", DefaultDailySendLimit); err != nil {
		return Config{}, err
	}
	if cfg.LLMMaxMessages, err = envInt("MC_LLM_MAX_MESSAGES", DefaultLLMMaxMessages); err != nil {
		return Config{}, err
	}
	if cfg.AgentMaxMessages, err = envInt("MC_AGENT_MAX_MESSAGES", DefaultAgentMaxMessages); err != nil {
		return Config{}, err
	}
	if cfg.AgentMaxSteps, err = envInt("MC_AGENT_MAX_STEPS", DefaultAgentMaxSteps); err != nil {
		return Config{}, err
	}
	if cfg.LLMBodyChars, err = envInt("MC_LLM_BODY_CHARS", DefaultLLMBodyChars); err != nil {
		return Config{}, err
	}
	if cfg.LogLevel, err = parseLogLevel(env("MC_LOG_LEVEL", "info")); err != nil {
		return Config{}, err
	}
	cfg.CookieSecure = envBool("MC_COOKIE_SECURE", strings.HasPrefix(cfg.BaseURL, "https://"))

	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

// Validate checks that all configuration values are present and sane.
func (c Config) Validate() error {
	if strings.TrimSpace(c.Addr) == "" {
		return errors.New("config: MC_ADDR must not be empty")
	}
	if strings.TrimSpace(c.DBPath) == "" {
		return errors.New("config: MC_DB_PATH must not be empty")
	}
	if len(c.SecretKey) != 32 {
		return fmt.Errorf("config: MC_SECRET_KEY must decode to 32 bytes, got %d", len(c.SecretKey))
	}
	if len(c.AdminPassword) < 8 {
		return errors.New("config: MC_ADMIN_PASSWORD must be at least 8 characters")
	}
	u, err := url.Parse(c.BaseURL)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return fmt.Errorf("config: MC_BASE_URL %q is not an absolute URL", c.BaseURL)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("config: MC_BASE_URL must use http or https, got %q", u.Scheme)
	}
	if c.SendRatePerMin < 1 || c.SendRatePerMin > 6000 {
		return fmt.Errorf("config: MC_SEND_RATE_PER_MIN must be between 1 and 6000, got %d", c.SendRatePerMin)
	}
	if c.DailySendLimit < 1 || c.DailySendLimit > 100000 {
		return fmt.Errorf("config: MC_DAILY_SEND_LIMIT must be between 1 and 100000, got %d", c.DailySendLimit)
	}
	if c.LLMMaxMessages < 1 || c.LLMMaxMessages > 1000 {
		return fmt.Errorf("config: MC_LLM_MAX_MESSAGES must be between 1 and 1000, got %d", c.LLMMaxMessages)
	}
	if c.AgentMaxMessages < 1 || c.AgentMaxMessages > 100000 {
		return fmt.Errorf("config: MC_AGENT_MAX_MESSAGES must be between 1 and 100000, got %d", c.AgentMaxMessages)
	}
	if c.AgentMaxSteps < 1 || c.AgentMaxSteps > 20 {
		return fmt.Errorf("config: MC_AGENT_MAX_STEPS must be between 1 and 20, got %d", c.AgentMaxSteps)
	}
	if c.LLMBodyChars < 100 || c.LLMBodyChars > 100000 {
		return fmt.Errorf("config: MC_LLM_BODY_CHARS must be between 100 and 100000, got %d", c.LLMBodyChars)
	}
	if c.UnsubscribeEmail != "" {
		parsed, err := mail.ParseAddress(c.UnsubscribeEmail)
		if err != nil || parsed.Name != "" || !strings.EqualFold(parsed.Address, c.UnsubscribeEmail) {
			return fmt.Errorf("config: MC_UNSUBSCRIBE_EMAIL must be a plain email address, got %q", c.UnsubscribeEmail)
		}
	}
	return nil
}

func env(key, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return fallback
}

func envInt(key string, fallback int) (int, error) {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return fallback, nil
	}
	v, err := strconv.Atoi(raw)
	if err != nil {
		return 0, fmt.Errorf("config: %s must be an integer: %w", key, err)
	}
	return v, nil
}

func envBool(key string, fallback bool) bool {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return fallback
	}
	v, err := strconv.ParseBool(raw)
	if err != nil {
		return fallback
	}
	return v
}

func parseSecretKey(raw string) ([]byte, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, errors.New("config: MC_SECRET_KEY is required (base64-encoded 32 bytes)")
	}
	key, err := base64.StdEncoding.DecodeString(strings.TrimSpace(raw))
	if err != nil {
		return nil, fmt.Errorf("config: MC_SECRET_KEY is not valid base64: %w", err)
	}
	if len(key) != 32 {
		return nil, fmt.Errorf("config: MC_SECRET_KEY must decode to 32 bytes, got %d", len(key))
	}
	return key, nil
}

func parseLogLevel(raw string) (slog.Level, error) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "debug":
		return slog.LevelDebug, nil
	case "info":
		return slog.LevelInfo, nil
	case "warn", "warning":
		return slog.LevelWarn, nil
	case "error":
		return slog.LevelError, nil
	default:
		return 0, fmt.Errorf("config: MC_LOG_LEVEL %q is invalid", raw)
	}
}
