// Package llm provides LLM provider clients, encrypted provider settings and
// non-mutating email assistance such as summaries and reply drafts.
package llm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/ph0nkj/mailcontroller/internal/domain"
	"github.com/ph0nkj/mailcontroller/internal/store"
)

const (
	// ProviderOpenAI covers OpenAI and OpenAI-compatible chat completion APIs.
	ProviderOpenAI = "openai"
	// ProviderAnthropic covers the Anthropic Messages API.
	ProviderAnthropic = "anthropic"

	// DefaultOpenAIBaseURL and DefaultAnthropicBaseURL are used when no custom
	// endpoint is configured.
	DefaultOpenAIBaseURL    = "https://api.openai.com/v1"
	DefaultAnthropicBaseURL = "https://api.anthropic.com/v1"

	settingConfig = "llm.config_enc"

	maxBaseURLLength = 2048
	maxModelLength   = 200
	maxAPIKeyLength  = 4096
	maxResponseBytes = 1 << 20

	defaultMaxMessages = 200
	defaultBodyChars   = 2000
	defaultHTTPTimeout = 90 * time.Second
	auditTimeout       = 5 * time.Second

	defaultMaxOutputTokens = 1024
	testMaxOutputTokens    = 16
	summaryMaxTokens       = 800
	draftMaxTokens         = 800
)

var (
	// ErrNotConfigured is returned when no provider, model or API key exists.
	ErrNotConfigured = errors.New("llm: provider is not configured")
	// ErrInvalidSettings is returned when provider settings fail validation.
	ErrInvalidSettings = errors.New("llm: invalid settings")
	// ErrInvalidRequest is returned when an assistant request is malformed.
	ErrInvalidRequest = errors.New("llm: invalid request")
	// ErrProvider is returned when the upstream LLM request fails.
	ErrProvider = errors.New("llm: provider request failed")
	// ErrNoMailbox is returned when assistant work needs a mailbox but none is
	// available.
	ErrNoMailbox = errors.New("llm: no mailbox is available")
)

// Settings is the complete provider configuration. APIKey is only present in
// memory and in the encrypted settings row; it is never returned by Status.
type Settings struct {
	Provider string `json:"provider"`
	BaseURL  string `json:"base_url"`
	Model    string `json:"model"`
	APIKey   string `json:"api_key,omitempty"`
}

// Status is the safe, public view of the provider configuration.
type Status struct {
	Configured bool   `json:"configured"`
	Provider   string `json:"provider"`
	BaseURL    string `json:"base_url"`
	Model      string `json:"model"`
	HasAPIKey  bool   `json:"has_api_key"`
}

// SettingsStore persists opaque settings.
type SettingsStore interface {
	Get(ctx context.Context, key string) ([]byte, error)
	Set(ctx context.Context, key string, value []byte) error
	Delete(ctx context.Context, key string) error
}

// Cipher encrypts and decrypts secrets at rest.
type Cipher interface {
	Encrypt(plaintext []byte) ([]byte, error)
	Decrypt(sealed []byte) ([]byte, error)
}

// MessageSource loads Gmail messages for assistant prompts.
type MessageSource interface {
	GetMessage(ctx context.Context, id string) (domain.Message, error)
}

// AuditStore records security-relevant actions.
type AuditStore interface {
	Add(ctx context.Context, entry domain.AuditEntry) error
}

// Options tunes assistant limits and HTTP behavior.
type Options struct {
	// HTTPClient is used for provider calls. A 90 second timeout client is used
	// when omitted.
	HTTPClient *http.Client
	// MaxMessages caps how many messages one summary request may inspect.
	MaxMessages int
	// BodyChars caps how many body characters per message are sent to a model.
	BodyChars int
}

// Service owns provider configuration and non-mutating assistant operations.
type Service struct {
	settings    SettingsStore
	cipher      Cipher
	mailbox     MessageSource
	audit       AuditStore
	log         *slog.Logger
	httpClient  *http.Client
	maxMessages int
	bodyChars   int
}

// NewService creates an LLM service. Settings, a cipher and a mailbox source
// are required.
func NewService(
	settings SettingsStore,
	cipher Cipher,
	mailbox MessageSource,
	audit AuditStore,
	log *slog.Logger,
	opts Options,
) (*Service, error) {
	if settings == nil || cipher == nil {
		return nil, errors.New("llm: settings and cipher are required")
	}
	if mailbox == nil {
		return nil, errors.New("llm: mailbox source is required")
	}
	if log == nil {
		log = slog.Default()
	}
	if opts.HTTPClient == nil {
		opts.HTTPClient = &http.Client{Timeout: defaultHTTPTimeout}
	}
	if opts.MaxMessages <= 0 {
		opts.MaxMessages = defaultMaxMessages
	}
	if opts.BodyChars <= 0 {
		opts.BodyChars = defaultBodyChars
	}
	return &Service{
		settings:    settings,
		cipher:      cipher,
		mailbox:     mailbox,
		audit:       audit,
		log:         log,
		httpClient:  opts.HTTPClient,
		maxMessages: opts.MaxMessages,
		bodyChars:   opts.BodyChars,
	}, nil
}

// Status returns the safe provider configuration.
func (s *Service) Status(ctx context.Context) (Status, error) {
	cfg, err := s.load(ctx)
	if err != nil {
		return Status{}, err
	}
	return statusFrom(cfg), nil
}

// Save validates and stores provider settings. An empty API key keeps the
// previously saved key, so other fields can be edited without re-entering it.
func (s *Service) Save(ctx context.Context, in Settings) (Status, error) {
	existing, err := s.load(ctx)
	if err != nil {
		return Status{}, err
	}
	cfg, err := normalizeSettings(in, existing)
	if err != nil {
		return Status{}, err
	}
	raw, err := json.Marshal(cfg)
	if err != nil {
		return Status{}, fmt.Errorf("llm: encode settings: %w", err)
	}
	enc, err := s.cipher.Encrypt(raw)
	if err != nil {
		return Status{}, fmt.Errorf("llm: encrypt settings: %w", err)
	}
	if err := s.settings.Set(ctx, settingConfig, enc); err != nil {
		return Status{}, fmt.Errorf("llm: store settings: %w", err)
	}
	return statusFrom(cfg), nil
}

// Clear removes the stored provider configuration.
func (s *Service) Clear(ctx context.Context) error {
	if err := s.settings.Delete(ctx, settingConfig); err != nil {
		return fmt.Errorf("llm: clear settings: %w", err)
	}
	return nil
}

// Test performs a minimal provider round trip.
func (s *Service) Test(ctx context.Context) error {
	client, err := s.client(ctx)
	if err != nil {
		return err
	}
	if _, err := client.complete(ctx, "You are a connectivity check.",
		"Reply with the single word OK.", testMaxOutputTokens); err != nil {
		return err
	}
	s.auditEvent(ctx, "llm.test", map[string]any{
		"provider": client.provider,
		"model":    client.model,
	})
	return nil
}

func (s *Service) load(ctx context.Context) (Settings, error) {
	raw, err := s.settings.Get(ctx, settingConfig)
	if errors.Is(err, store.ErrNotFound) {
		return Settings{Provider: ProviderOpenAI, BaseURL: DefaultOpenAIBaseURL}, nil
	}
	if err != nil {
		return Settings{}, fmt.Errorf("llm: load settings: %w", err)
	}
	plain, err := s.cipher.Decrypt(raw)
	if err != nil {
		return Settings{}, fmt.Errorf("llm: decrypt settings: %w", err)
	}
	var cfg Settings
	if err := json.Unmarshal(plain, &cfg); err != nil {
		return Settings{}, fmt.Errorf("llm: decode settings: %w", err)
	}
	if cfg.Provider == "" {
		cfg.Provider = ProviderOpenAI
	}
	if cfg.BaseURL == "" {
		cfg.BaseURL = defaultBaseURL(cfg.Provider)
	}
	return cfg, nil
}

func (s *Service) client(ctx context.Context) (*Client, error) {
	cfg, err := s.load(ctx)
	if err != nil {
		return nil, err
	}
	if cfg.APIKey == "" || strings.TrimSpace(cfg.Model) == "" || !validProvider(cfg.Provider) {
		return nil, ErrNotConfigured
	}
	return newClient(cfg, s.httpClient), nil
}

func (s *Service) auditEvent(ctx context.Context, action string, details map[string]any) {
	if s.audit == nil {
		return
	}
	raw, err := json.Marshal(details)
	if err != nil {
		raw = []byte("{}")
	}
	auditCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), auditTimeout)
	defer cancel()
	entry := domain.AuditEntry{Actor: "admin", Action: action, Details: string(raw)}
	if err := s.audit.Add(auditCtx, entry); err != nil {
		s.log.Warn("write llm audit entry", "action", action, "error", err)
	}
}

func statusFrom(cfg Settings) Status {
	return Status{
		Configured: validProvider(cfg.Provider) && cfg.Model != "" && cfg.APIKey != "",
		Provider:   cfg.Provider,
		BaseURL:    cfg.BaseURL,
		Model:      cfg.Model,
		HasAPIKey:  cfg.APIKey != "",
	}
}

func normalizeSettings(in, existing Settings) (Settings, error) {
	cfg := Settings{
		Provider: strings.ToLower(strings.TrimSpace(in.Provider)),
		BaseURL:  strings.TrimSpace(in.BaseURL),
		Model:    strings.TrimSpace(in.Model),
		APIKey:   strings.TrimSpace(in.APIKey),
	}
	if cfg.Provider == "" {
		cfg.Provider = ProviderOpenAI
	}
	if !validProvider(cfg.Provider) {
		return Settings{}, fmt.Errorf("%w: provider must be %q or %q",
			ErrInvalidSettings, ProviderOpenAI, ProviderAnthropic)
	}
	if cfg.BaseURL == "" {
		cfg.BaseURL = defaultBaseURL(cfg.Provider)
	}
	if err := validateBaseURL(cfg.BaseURL); err != nil {
		return Settings{}, err
	}
	if cfg.Model == "" {
		return Settings{}, fmt.Errorf("%w: model is required", ErrInvalidSettings)
	}
	if len(cfg.Model) > maxModelLength || containsControl(cfg.Model) {
		return Settings{}, fmt.Errorf("%w: model must be at most %d printable characters",
			ErrInvalidSettings, maxModelLength)
	}
	if cfg.APIKey == "" {
		cfg.APIKey = existing.APIKey
	}
	if cfg.APIKey == "" {
		return Settings{}, fmt.Errorf("%w: api key is required", ErrInvalidSettings)
	}
	if len(cfg.APIKey) > maxAPIKeyLength || containsControl(cfg.APIKey) {
		return Settings{}, fmt.Errorf("%w: api key is invalid", ErrInvalidSettings)
	}
	return cfg, nil
}

func validateBaseURL(raw string) error {
	if len(raw) > maxBaseURLLength || containsControl(raw) || strings.ContainsAny(raw, " \t") {
		return fmt.Errorf("%w: base URL is invalid", ErrInvalidSettings)
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" ||
		(parsed.Scheme != "http" && parsed.Scheme != "https") {
		return fmt.Errorf("%w: base URL must be an absolute http or https URL", ErrInvalidSettings)
	}
	if parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return fmt.Errorf("%w: base URL must not contain credentials, a query or a fragment", ErrInvalidSettings)
	}
	return nil
}

func validProvider(provider string) bool {
	return provider == ProviderOpenAI || provider == ProviderAnthropic
}

func defaultBaseURL(provider string) string {
	if provider == ProviderAnthropic {
		return DefaultAnthropicBaseURL
	}
	return DefaultOpenAIBaseURL
}

func containsControl(value string) bool {
	for _, r := range value {
		if r < 0x20 || r == 0x7f {
			return true
		}
	}
	return false
}
