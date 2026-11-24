package gmail

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"golang.org/x/oauth2"

	mcrypto "github.com/ph0nkj/mailcontroller/internal/crypto"
	"github.com/ph0nkj/mailcontroller/internal/domain"
	"github.com/ph0nkj/mailcontroller/internal/store"
)

type oauthTestEnv struct {
	service  *Service
	settings *store.SettingsRepo
	accounts *store.GoogleAccountRepo
	cipher   *mcrypto.Cipher
	server   *httptest.Server
	revokes  *atomic.Int32
	tokens   *atomic.Int32
}

func newOAuthTestEnv(t *testing.T) *oauthTestEnv {
	t.Helper()
	var revokes, tokens atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/token":
			tokens.Add(1)
			if err := r.ParseForm(); err != nil {
				t.Errorf("parse token form: %v", err)
			}
			w.Header().Set("Content-Type", "application/json")
			if r.Form.Get("grant_type") == "refresh_token" {
				_, _ = io.WriteString(w, `{"access_token":"refreshed-access","token_type":"Bearer","expires_in":3600}`)
				return
			}
			_, _ = io.WriteString(w, `{"access_token":"access-token","token_type":"Bearer","refresh_token":"refresh-token","expires_in":3600}`)
		case strings.HasSuffix(r.URL.Path, "/gmail/v1/users/me/profile"):
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"emailAddress":"user@example.com"}`)
		case r.URL.Path == "/revoke":
			revokes.Add(1)
			w.WriteHeader(http.StatusOK)
		case r.URL.Path == "/auth":
			w.WriteHeader(http.StatusOK)
		default:
			t.Errorf("unexpected path: %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(server.Close)

	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "gmail.db"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, st.Close()) })

	cipher, err := mcrypto.New(bytes.Repeat([]byte{7}, mcrypto.KeySize))
	require.NoError(t, err)

	service, err := NewService(st.Settings(), st.GoogleAccount(), cipher, slog.New(slog.NewTextHandler(io.Discard, nil)), Options{
		BaseURL:       "https://mail.example.com/",
		AuthEndpoint:  oauth2.Endpoint{AuthURL: server.URL + "/auth", TokenURL: server.URL + "/token"},
		GmailEndpoint: server.URL + "/",
		RevokeURL:     server.URL + "/revoke",
		HTTPClient:    server.Client(),
	})
	require.NoError(t, err)
	return &oauthTestEnv{
		service:  service,
		settings: st.Settings(),
		accounts: st.GoogleAccount(),
		cipher:   cipher,
		server:   server,
		revokes:  &revokes,
		tokens:   &tokens,
	}
}

func TestNewServiceValidation(t *testing.T) {
	_, err := NewService(nil, nil, nil, nil, Options{})
	require.Error(t, err)
}

func TestStatusNotConfigured(t *testing.T) {
	env := newOAuthTestEnv(t)
	status, err := env.service.Status(context.Background())
	require.NoError(t, err)
	require.False(t, status.Configured)
	require.False(t, status.Connected)
	require.Equal(t, "https://mail.example.com/api/v1/google/callback", status.RedirectURL)
}

func TestSaveClientEncryptsSecret(t *testing.T) {
	env := newOAuthTestEnv(t)
	ctx := context.Background()

	require.Error(t, env.service.SaveClient(ctx, "", "secret"))
	require.Error(t, env.service.SaveClient(ctx, "client", ""))

	require.NoError(t, env.service.SaveClient(ctx, "client-id", "super-secret"))
	raw, err := env.settings.Get(ctx, settingOAuthClient)
	require.NoError(t, err)
	require.NotContains(t, string(raw), "super-secret")
	require.NotContains(t, string(raw), "client-id")

	status, err := env.service.Status(ctx)
	require.NoError(t, err)
	require.True(t, status.Configured)
	require.True(t, status.HasClientSecret)
	require.Equal(t, "client-id", status.ClientID)
	require.False(t, status.Connected)
}

func TestSaveClientTooLong(t *testing.T) {
	env := newOAuthTestEnv(t)
	require.Error(t, env.service.SaveClient(context.Background(), strings.Repeat("a", 513), "secret"))
}

func TestConnectURL(t *testing.T) {
	env := newOAuthTestEnv(t)
	ctx := context.Background()

	_, err := env.service.ConnectURL(ctx, "state")
	require.ErrorIs(t, err, ErrNotConfigured)
	_, err = env.service.ConnectURL(ctx, "")
	require.Error(t, err)

	require.NoError(t, env.service.SaveClient(ctx, "client-id", "client-secret"))
	connectURL, err := env.service.ConnectURL(ctx, "state-token")
	require.NoError(t, err)

	parsed, err := url.Parse(connectURL)
	require.NoError(t, err)
	query := parsed.Query()
	require.Equal(t, "state-token", query.Get("state"))
	require.Equal(t, "client-id", query.Get("client_id"))
	require.Equal(t, "https://mail.example.com/api/v1/google/callback", query.Get("redirect_uri"))
	require.Equal(t, "offline", query.Get("access_type"))
	require.Equal(t, "consent", query.Get("prompt"))
	require.Contains(t, query.Get("scope"), "gmail.modify")
	require.Contains(t, query.Get("scope"), "gmail.send")
}

func TestExchangeAndDisconnect(t *testing.T) {
	env := newOAuthTestEnv(t)
	ctx := context.Background()
	require.NoError(t, env.service.SaveClient(ctx, "client-id", "client-secret"))

	email, err := env.service.Exchange(ctx, "auth-code")
	require.NoError(t, err)
	require.Equal(t, "user@example.com", email)
	require.Equal(t, int32(1), env.tokens.Load())

	account, err := env.accounts.Get(ctx)
	require.NoError(t, err)
	require.Equal(t, "user@example.com", account.Email)
	require.NotContains(t, string(account.TokenEnc), "refresh-token")

	raw, err := env.cipher.Decrypt(account.TokenEnc)
	require.NoError(t, err)
	var token oauth2.Token
	require.NoError(t, json.Unmarshal(raw, &token))
	require.Equal(t, "refresh-token", token.RefreshToken)
	require.Equal(t, "access-token", token.AccessToken)

	status, err := env.service.Status(ctx)
	require.NoError(t, err)
	require.True(t, status.Connected)
	require.Equal(t, "user@example.com", status.Email)

	client, err := env.service.Client(ctx)
	require.NoError(t, err)
	require.Equal(t, "user@example.com", client.Email())

	require.NoError(t, env.service.Disconnect(ctx))
	require.Equal(t, int32(1), env.revokes.Load())
	_, err = env.accounts.Get(ctx)
	require.ErrorIs(t, err, store.ErrNotFound)

	status, err = env.service.Status(ctx)
	require.NoError(t, err)
	require.False(t, status.Connected)

	// A second disconnect is a no-op.
	require.NoError(t, env.service.Disconnect(ctx))
	require.Equal(t, int32(1), env.revokes.Load())
}

func TestTokenRefreshIsPersisted(t *testing.T) {
	env := newOAuthTestEnv(t)
	ctx := context.Background()
	require.NoError(t, env.service.SaveClient(ctx, "client-id", "client-secret"))

	expired := &oauth2.Token{
		AccessToken:  "old-access",
		RefreshToken: "refresh-token",
		Expiry:       time.Now().Add(-time.Hour),
	}
	enc, err := env.service.encryptToken(expired)
	require.NoError(t, err)
	require.NoError(t, env.accounts.Save(ctx, domain.GoogleAccount{
		Email:     "user@example.com",
		TokenEnc:  enc,
		CreatedAt: time.Now(),
	}))

	source, _, err := env.service.tokenSource(ctx)
	require.NoError(t, err)
	token, err := source.Token()
	require.NoError(t, err)
	require.Equal(t, "refreshed-access", token.AccessToken)
	require.Equal(t, "refresh-token", token.RefreshToken)

	account, err := env.accounts.Get(ctx)
	require.NoError(t, err)
	raw, err := env.cipher.Decrypt(account.TokenEnc)
	require.NoError(t, err)
	var persisted oauth2.Token
	require.NoError(t, json.Unmarshal(raw, &persisted))
	require.Equal(t, "refreshed-access", persisted.AccessToken)
	require.Equal(t, "refresh-token", persisted.RefreshToken)
}

func TestExchangeRequiresConfiguration(t *testing.T) {
	env := newOAuthTestEnv(t)
	_, err := env.service.Exchange(context.Background(), "code")
	require.ErrorIs(t, err, ErrNotConfigured)
	_, err = env.service.Exchange(context.Background(), "")
	require.Error(t, err)
}

func TestClientWithoutAccount(t *testing.T) {
	env := newOAuthTestEnv(t)
	require.NoError(t, env.service.SaveClient(context.Background(), "client-id", "client-secret"))
	_, err := env.service.Client(context.Background())
	require.ErrorIs(t, err, ErrNotConnected)
}

func TestCorruptToken(t *testing.T) {
	env := newOAuthTestEnv(t)
	ctx := context.Background()
	require.NoError(t, env.service.SaveClient(ctx, "client-id", "client-secret"))
	require.NoError(t, env.accounts.Save(ctx, domain.GoogleAccount{
		Email:     "user@example.com",
		TokenEnc:  []byte("not-encrypted"),
		CreatedAt: time.Now(),
	}))
	_, err := env.service.Client(ctx)
	require.Error(t, err)
}
