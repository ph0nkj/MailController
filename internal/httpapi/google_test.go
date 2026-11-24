package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/ph0nkj/mailcontroller/internal/gmail"
)

type fakeGoogle struct {
	status        gmail.Status
	statusErr     error
	savedID       string
	savedSecret   string
	saveErr       error
	connectURL    string
	connectErr    error
	connectState  string
	exchangeCode  string
	exchangeMail  string
	exchangeErr   error
	disconnected  bool
	disconnectErr error
}

func (f *fakeGoogle) Status(context.Context) (gmail.Status, error) {
	return f.status, f.statusErr
}

func (f *fakeGoogle) SaveClient(_ context.Context, clientID, clientSecret string) error {
	f.savedID, f.savedSecret = clientID, clientSecret
	return f.saveErr
}

func (f *fakeGoogle) ConnectURL(_ context.Context, state string) (string, error) {
	f.connectState = state
	if f.connectURL != "" {
		return f.connectURL, f.connectErr
	}
	return "https://accounts.example.com/auth?state=" + url.QueryEscape(state), f.connectErr
}

func (f *fakeGoogle) Exchange(_ context.Context, code string) (string, error) {
	f.exchangeCode = code
	return f.exchangeMail, f.exchangeErr
}

func (f *fakeGoogle) Disconnect(context.Context) error {
	f.disconnected = true
	return f.disconnectErr
}

func TestGoogleStatusEndpoint(t *testing.T) {
	fake := &fakeGoogle{status: gmail.Status{
		Configured:      true,
		Connected:       true,
		Email:           "user@example.com",
		ClientID:        "client",
		HasClientSecret: true,
		RedirectURL:     "https://mail.example.com/api/v1/google/callback",
	}}
	_, handler := newTestAPIWithGoogle(t, fake)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/google/status", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	require.Equal(t, http.StatusUnauthorized, rec.Code)

	cookie, _ := loginSession(t, handler)
	req = httptest.NewRequest(http.MethodGet, "/api/v1/google/status", nil)
	req.AddCookie(cookie)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)
	var status gmail.Status
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &status))
	require.True(t, status.Connected)
	require.Equal(t, "user@example.com", status.Email)
}

func TestGoogleSaveClient(t *testing.T) {
	fake := &fakeGoogle{status: gmail.Status{Configured: true}}
	_, handler := newTestAPIWithGoogle(t, fake)
	cookie, csrf := loginSession(t, handler)

	body := `{"client_id":"id.apps.googleusercontent.com","client_secret":"secret"}`
	req := httptest.NewRequest(http.MethodPut, "/api/v1/google/client", strings.NewReader(body))
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	require.Equal(t, http.StatusForbidden, rec.Code)

	req = httptest.NewRequest(http.MethodPut, "/api/v1/google/client", strings.NewReader(body))
	req.AddCookie(cookie)
	req.Header.Set("X-CSRF-Token", csrf)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "id.apps.googleusercontent.com", fake.savedID)
	require.Equal(t, "secret", fake.savedSecret)

	req = httptest.NewRequest(http.MethodPut, "/api/v1/google/client", strings.NewReader(`{"client_id":"","client_secret":""}`))
	req.AddCookie(cookie)
	req.Header.Set("X-CSRF-Token", csrf)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	require.Equal(t, http.StatusBadRequest, rec.Code)

	req = httptest.NewRequest(http.MethodPut, "/api/v1/google/client", strings.NewReader(`not-json`))
	req.AddCookie(cookie)
	req.Header.Set("X-CSRF-Token", csrf)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	require.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestGoogleConnectRedirect(t *testing.T) {
	fake := &fakeGoogle{}
	_, handler := newTestAPIWithGoogle(t, fake)
	cookie, _ := loginSession(t, handler)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/google/connect", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	require.Equal(t, http.StatusFound, rec.Code)

	result := rec.Result()
	defer func() { require.NoError(t, result.Body.Close()) }()
	location, err := url.Parse(result.Header.Get("Location"))
	require.NoError(t, err)
	require.Equal(t, "accounts.example.com", location.Host)

	var stateCookie *http.Cookie
	for _, c := range result.Cookies() {
		if c.Name == oauthStateCookie {
			stateCookie = c
		}
	}
	require.NotNil(t, stateCookie)
	require.Equal(t, fake.connectState, stateCookie.Value)
	require.Equal(t, fake.connectState, location.Query().Get("state"))
	require.True(t, stateCookie.HttpOnly)
}

func TestGoogleConnectNotConfigured(t *testing.T) {
	fake := &fakeGoogle{connectErr: gmail.ErrNotConfigured}
	_, handler := newTestAPIWithGoogle(t, fake)
	cookie, _ := loginSession(t, handler)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/google/connect", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	require.Equal(t, http.StatusBadRequest, rec.Code)
	require.Contains(t, rec.Body.String(), "google_not_configured")
}

func TestGoogleCallback(t *testing.T) {
	fake := &fakeGoogle{exchangeMail: "user@example.com"}
	_, handler := newTestAPIWithGoogle(t, fake)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/google/callback?state=state-1&code=auth-code", nil)
	req.AddCookie(&http.Cookie{Name: oauthStateCookie, Value: "state-1"})
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	require.Equal(t, http.StatusFound, rec.Code)
	require.Equal(t, "/settings?google=connected", rec.Header().Get("Location"))
	require.Equal(t, "auth-code", fake.exchangeCode)

	// State mismatch is rejected.
	fake.exchangeCode = ""
	req = httptest.NewRequest(http.MethodGet, "/api/v1/google/callback?state=other&code=auth-code", nil)
	req.AddCookie(&http.Cookie{Name: oauthStateCookie, Value: "state-1"})
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	require.Equal(t, "/settings?google=error", rec.Header().Get("Location"))
	require.Empty(t, fake.exchangeCode)

	// Provider error is rejected.
	req = httptest.NewRequest(http.MethodGet, "/api/v1/google/callback?state=state-1&error=access_denied", nil)
	req.AddCookie(&http.Cookie{Name: oauthStateCookie, Value: "state-1"})
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	require.Equal(t, "/settings?google=error", rec.Header().Get("Location"))

	// Missing code is rejected.
	req = httptest.NewRequest(http.MethodGet, "/api/v1/google/callback?state=state-1", nil)
	req.AddCookie(&http.Cookie{Name: oauthStateCookie, Value: "state-1"})
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	require.Equal(t, "/settings?google=error", rec.Header().Get("Location"))

	// Exchange errors are rejected.
	fake.exchangeErr = errors.New("exchange failed")
	req = httptest.NewRequest(http.MethodGet, "/api/v1/google/callback?state=state-1&code=auth-code", nil)
	req.AddCookie(&http.Cookie{Name: oauthStateCookie, Value: "state-1"})
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	require.Equal(t, "/settings?google=error", rec.Header().Get("Location"))
}

func TestGoogleCallbackMissingStateCookie(t *testing.T) {
	_, handler := newTestAPIWithGoogle(t, &fakeGoogle{})
	req := httptest.NewRequest(http.MethodGet, "/api/v1/google/callback?state=state-1&code=code", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	require.Equal(t, "/settings?google=error", rec.Header().Get("Location"))
}

func TestGoogleDisconnect(t *testing.T) {
	fake := &fakeGoogle{}
	_, handler := newTestAPIWithGoogle(t, fake)
	cookie, csrf := loginSession(t, handler)

	req := httptest.NewRequest(http.MethodDelete, "/api/v1/google/disconnect", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	require.Equal(t, http.StatusForbidden, rec.Code)
	require.False(t, fake.disconnected)

	req = httptest.NewRequest(http.MethodDelete, "/api/v1/google/disconnect", nil)
	req.AddCookie(cookie)
	req.Header.Set("X-CSRF-Token", csrf)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)
	require.True(t, fake.disconnected)
}

func TestGoogleRoutesAbsentWithoutService(t *testing.T) {
	_, handler := newTestAPI(t)
	cookie, _ := loginSession(t, handler)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/google/status", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	require.Equal(t, http.StatusNotFound, rec.Code)
}
