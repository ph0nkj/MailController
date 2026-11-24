// Package gmail wraps the Gmail API with OAuth account management.
package gmail

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"golang.org/x/oauth2"
	gmailapi "google.golang.org/api/gmail/v1"
	"google.golang.org/api/option"

	"github.com/ph0nkj/mailcontroller/internal/domain"
	"github.com/ph0nkj/mailcontroller/internal/store"
)

// CallbackPath is the OAuth redirect path registered with Google.
const CallbackPath = "/api/v1/google/callback"

const settingOAuthClient = "google.oauth_client_enc"

// OAuth scopes requested by MailController. Permanent delete is not supported.
var scopes = []string{
	gmailapi.GmailModifyScope,
	gmailapi.GmailSendScope,
}

var (
	// ErrNotConfigured is returned when no OAuth client credentials exist.
	ErrNotConfigured = errors.New("gmail: oauth client is not configured")
	// ErrNotConnected is returned when no Google account is connected.
	ErrNotConnected = errors.New("gmail: no google account is connected")
)

var googleEndpoint = oauth2.Endpoint{ //nolint:gochecknoglobals // immutable constant
	AuthURL:  "https://accounts.google.com/o/oauth2/v2/auth",
	TokenURL: "https://oauth2.googleapis.com/token",
}

// SettingsStore is the persistence surface used for OAuth client credentials.
type SettingsStore interface {
	Get(ctx context.Context, key string) ([]byte, error)
	Set(ctx context.Context, key string, value []byte) error
}

// AccountStore persists the connected Google account.
type AccountStore interface {
	Get(ctx context.Context) (domain.GoogleAccount, error)
	Save(ctx context.Context, account domain.GoogleAccount) error
	Delete(ctx context.Context) error
}

// Cipher encrypts and decrypts secrets at rest.
type Cipher interface {
	Encrypt(plaintext []byte) ([]byte, error)
	Decrypt(sealed []byte) ([]byte, error)
}

// Status describes Google configuration and connection state.
type Status struct {
	Configured      bool   `json:"configured"`
	Connected       bool   `json:"connected"`
	Email           string `json:"email,omitempty"`
	ClientID        string `json:"client_id,omitempty"`
	HasClientSecret bool   `json:"has_client_secret"`
	RedirectURL     string `json:"redirect_url"`
}

// Options configures a Service.
type Options struct {
	// BaseURL is the public origin, used to build the OAuth redirect URL.
	BaseURL string
	// ClientID and ClientSecret are fallback credentials from the environment.
	ClientID     string
	ClientSecret string
	// AuthEndpoint overrides the Google OAuth endpoints (for tests).
	AuthEndpoint oauth2.Endpoint
	// GmailEndpoint overrides the Gmail API endpoint (for tests).
	GmailEndpoint string
	// RevokeURL overrides the token revocation endpoint (for tests).
	RevokeURL string
	// HTTPClient is used for token revocation.
	HTTPClient *http.Client
}

type oauthClient struct {
	ClientID     string `json:"client_id"`
	ClientSecret string `json:"client_secret"`
}

// Service manages the Google OAuth client, account token and Gmail clients.
type Service struct {
	settings      SettingsStore
	accounts      AccountStore
	cipher        Cipher
	log           *slog.Logger
	baseURL       string
	envID         string
	envSecret     string
	endpoint      oauth2.Endpoint
	gmailEndpoint string
	revokeURL     string
	httpClient    *http.Client
}

// NewService creates a Gmail service. It requires persistence, a cipher and a
// logger.
func NewService(
	settings SettingsStore,
	accounts AccountStore,
	cipher Cipher,
	log *slog.Logger,
	opts Options,
) (*Service, error) {
	if settings == nil || accounts == nil || cipher == nil {
		return nil, errors.New("gmail: settings, accounts and cipher are required")
	}
	if log == nil {
		log = slog.Default()
	}
	if opts.AuthEndpoint.AuthURL == "" || opts.AuthEndpoint.TokenURL == "" {
		opts.AuthEndpoint = googleEndpoint
	}
	if opts.BaseURL == "" {
		opts.BaseURL = "http://localhost:8080"
	}
	if opts.HTTPClient == nil {
		opts.HTTPClient = &http.Client{Timeout: 15 * time.Second}
	}
	if opts.RevokeURL == "" {
		opts.RevokeURL = "https://oauth2.googleapis.com/revoke"
	}
	return &Service{
		settings:      settings,
		accounts:      accounts,
		cipher:        cipher,
		log:           log,
		baseURL:       strings.TrimRight(opts.BaseURL, "/"),
		envID:         strings.TrimSpace(opts.ClientID),
		envSecret:     strings.TrimSpace(opts.ClientSecret),
		endpoint:      opts.AuthEndpoint,
		gmailEndpoint: strings.TrimSpace(opts.GmailEndpoint),
		revokeURL:     opts.RevokeURL,
		httpClient:    opts.HTTPClient,
	}, nil
}

// Status returns the current Google configuration and connection state.
func (s *Service) Status(ctx context.Context) (Status, error) {
	cfg, configured, err := s.clientConfig(ctx)
	if err != nil {
		return Status{}, err
	}
	status := Status{
		Configured:  configured,
		RedirectURL: s.baseURL + CallbackPath,
	}
	if configured {
		status.ClientID = cfg.ClientID
		status.HasClientSecret = cfg.ClientSecret != ""
	}
	account, err := s.accounts.Get(ctx)
	switch {
	case errors.Is(err, store.ErrNotFound):
		return status, nil
	case err != nil:
		return Status{}, fmt.Errorf("gmail: load account: %w", err)
	}
	status.Connected = true
	status.Email = account.Email
	return status, nil
}

// SaveClient stores OAuth client credentials encrypted at rest.
func (s *Service) SaveClient(ctx context.Context, clientID, clientSecret string) error {
	clientID = strings.TrimSpace(clientID)
	clientSecret = strings.TrimSpace(clientSecret)
	if clientID == "" || clientSecret == "" {
		return errors.New("gmail: client id and client secret are required")
	}
	if len(clientID) > 512 || len(clientSecret) > 512 {
		return errors.New("gmail: oauth client credentials are too long")
	}
	raw, err := json.Marshal(oauthClient{ClientID: clientID, ClientSecret: clientSecret})
	if err != nil {
		return fmt.Errorf("gmail: encode oauth client: %w", err)
	}
	enc, err := s.cipher.Encrypt(raw)
	if err != nil {
		return fmt.Errorf("gmail: encrypt oauth client: %w", err)
	}
	if err := s.settings.Set(ctx, settingOAuthClient, enc); err != nil {
		return fmt.Errorf("gmail: store oauth client: %w", err)
	}
	return nil
}

// ConnectURL returns the Google consent URL for the given state value.
func (s *Service) ConnectURL(ctx context.Context, state string) (string, error) {
	if strings.TrimSpace(state) == "" {
		return "", errors.New("gmail: oauth state is required")
	}
	cfg, configured, err := s.clientConfig(ctx)
	if err != nil {
		return "", err
	}
	if !configured {
		return "", ErrNotConfigured
	}
	return cfg.AuthCodeURL(state, oauth2.AccessTypeOffline, oauth2.ApprovalForce), nil
}

// Exchange completes the OAuth flow, stores the token and returns the account
// email address.
func (s *Service) Exchange(ctx context.Context, code string) (string, error) {
	if strings.TrimSpace(code) == "" {
		return "", errors.New("gmail: authorization code is required")
	}
	cfg, configured, err := s.clientConfig(ctx)
	if err != nil {
		return "", err
	}
	if !configured {
		return "", ErrNotConfigured
	}
	token, err := cfg.Exchange(ctx, code)
	if err != nil {
		return "", fmt.Errorf("gmail: exchange authorization code: %w", err)
	}
	email, err := s.fetchProfileEmail(ctx, cfg, token)
	if err != nil {
		return "", err
	}
	enc, err := s.encryptToken(token)
	if err != nil {
		return "", err
	}
	if err := s.accounts.Save(ctx, domain.GoogleAccount{Email: email, TokenEnc: enc, CreatedAt: time.Now()}); err != nil {
		return "", fmt.Errorf("gmail: save account: %w", err)
	}
	s.log.Info("google account connected", "email", email)
	return email, nil
}

// Disconnect revokes the stored token (best effort) and removes the account.
func (s *Service) Disconnect(ctx context.Context) error {
	account, err := s.accounts.Get(ctx)
	if errors.Is(err, store.ErrNotFound) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("gmail: load account for disconnect: %w", err)
	}
	if token, tokenErr := s.accountToken(account); tokenErr == nil && token.RefreshToken != "" {
		s.revoke(ctx, token.RefreshToken)
	} else if tokenErr != nil {
		s.log.Warn("could not decrypt token for revocation", "error", tokenErr)
	}
	if err := s.accounts.Delete(ctx); err != nil {
		return fmt.Errorf("gmail: delete account: %w", err)
	}
	s.log.Info("google account disconnected")
	return nil
}

func (s *Service) clientConfig(ctx context.Context) (*oauth2.Config, bool, error) {
	client, err := s.oauthClient(ctx)
	if err != nil {
		return nil, false, err
	}
	if client.ClientID == "" || client.ClientSecret == "" {
		return nil, false, nil
	}
	return &oauth2.Config{
		ClientID:     client.ClientID,
		ClientSecret: client.ClientSecret,
		RedirectURL:  s.baseURL + CallbackPath,
		Scopes:       scopes,
		Endpoint:     s.endpoint,
	}, true, nil
}

func (s *Service) oauthClient(ctx context.Context) (oauthClient, error) {
	enc, err := s.settings.Get(ctx, settingOAuthClient)
	switch {
	case errors.Is(err, store.ErrNotFound):
		return oauthClient{ClientID: s.envID, ClientSecret: s.envSecret}, nil
	case err != nil:
		return oauthClient{}, fmt.Errorf("gmail: load oauth client: %w", err)
	}
	raw, err := s.cipher.Decrypt(enc)
	if err != nil {
		return oauthClient{}, fmt.Errorf("gmail: decrypt oauth client: %w", err)
	}
	var client oauthClient
	if err := json.Unmarshal(raw, &client); err != nil {
		return oauthClient{}, fmt.Errorf("gmail: decode oauth client: %w", err)
	}
	return client, nil
}

func (s *Service) fetchProfileEmail(ctx context.Context, cfg *oauth2.Config, token *oauth2.Token) (string, error) {
	opts := []option.ClientOption{option.WithHTTPClient(cfg.Client(ctx, token))}
	if s.gmailEndpoint != "" {
		opts = append(opts, option.WithEndpoint(s.gmailEndpoint))
	}
	service, err := gmailapi.NewService(ctx, opts...)
	if err != nil {
		return "", fmt.Errorf("gmail: create profile client: %w", err)
	}
	profile, err := service.Users.GetProfile("me").Context(ctx).Do()
	if err != nil {
		return "", fmt.Errorf("gmail: fetch profile: %w", err)
	}
	if profile.EmailAddress == "" {
		return "", errors.New("gmail: profile has no email address")
	}
	return profile.EmailAddress, nil
}

func (s *Service) encryptToken(token *oauth2.Token) ([]byte, error) {
	raw, err := json.Marshal(token)
	if err != nil {
		return nil, fmt.Errorf("gmail: encode token: %w", err)
	}
	enc, err := s.cipher.Encrypt(raw)
	if err != nil {
		return nil, fmt.Errorf("gmail: encrypt token: %w", err)
	}
	return enc, nil
}

func (s *Service) accountToken(account domain.GoogleAccount) (*oauth2.Token, error) {
	raw, err := s.cipher.Decrypt(account.TokenEnc)
	if err != nil {
		return nil, fmt.Errorf("gmail: decrypt token: %w", err)
	}
	var token oauth2.Token
	if err := json.Unmarshal(raw, &token); err != nil {
		return nil, fmt.Errorf("gmail: decode token: %w", err)
	}
	return &token, nil
}

func (s *Service) revoke(ctx context.Context, refreshToken string) {
	form := url.Values{"token": {refreshToken}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.revokeURL, strings.NewReader(form.Encode()))
	if err != nil {
		s.log.Warn("could not build token revocation request", "error", err)
		return
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := s.httpClient.Do(req)
	if err != nil {
		s.log.Warn("token revocation failed", "error", err)
		return
	}
	defer func() {
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
	}()
	if resp.StatusCode >= http.StatusBadRequest {
		s.log.Warn("token revocation rejected", "status", resp.StatusCode)
	}
}
