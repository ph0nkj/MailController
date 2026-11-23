package httpapi

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/ph0nkj/mailcontroller/internal/auth"
	"github.com/ph0nkj/mailcontroller/internal/config"
	"github.com/ph0nkj/mailcontroller/internal/store"
)

func newTestAPI(t *testing.T) (*API, http.Handler) {
	t.Helper()
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "api.db"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, st.Close()) })
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	authService, err := auth.New(context.Background(), st.Settings(), "test-password", false, logger)
	require.NoError(t, err)
	api := New(logger, config.Config{}, Deps{Auth: authService})
	return api, api.Router()
}

func login(t *testing.T, handler http.Handler) (*httptest.ResponseRecorder, loginResponse) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login",
		strings.NewReader(`{"password":"test-password"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	var body loginResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body), rec.Body.String())
	return rec, body
}

func TestHealthz(t *testing.T) {
	_, handler := newTestAPI(t)
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)
	require.JSONEq(t, `{"status":"ok"}`, rec.Body.String())
}

func TestLoginFlow(t *testing.T) {
	_, handler := newTestAPI(t)

	rec, body := login(t, handler)
	require.Equal(t, http.StatusOK, rec.Code)
	require.NotEmpty(t, body.CSRFToken)
	require.NotEmpty(t, body.ExpiresAt)

	result := rec.Result()
	defer func() { require.NoError(t, result.Body.Close()) }()
	cookies := result.Cookies()
	require.Len(t, cookies, 1)
	require.Equal(t, auth.SessionCookieName, cookies[0].Name)
	require.True(t, cookies[0].HttpOnly)
	require.False(t, cookies[0].Secure)
	require.Equal(t, http.SameSiteLaxMode, cookies[0].SameSite)

	// Authenticated /auth/me.
	req := httptest.NewRequest(http.MethodGet, "/api/v1/auth/me", nil)
	req.AddCookie(cookies[0])
	meRec := httptest.NewRecorder()
	handler.ServeHTTP(meRec, req)
	require.Equal(t, http.StatusOK, meRec.Code)
	var me meResponse
	require.NoError(t, json.Unmarshal(meRec.Body.Bytes(), &me))
	require.True(t, me.Authenticated)
	require.Equal(t, body.CSRFToken, me.CSRFToken)

	// Logout requires the CSRF header.
	logoutReq := httptest.NewRequest(http.MethodPost, "/api/v1/auth/logout", nil)
	logoutReq.AddCookie(cookies[0])
	logoutRec := httptest.NewRecorder()
	handler.ServeHTTP(logoutRec, logoutReq)
	require.Equal(t, http.StatusForbidden, logoutRec.Code)

	logoutReq = httptest.NewRequest(http.MethodPost, "/api/v1/auth/logout", nil)
	logoutReq.AddCookie(cookies[0])
	logoutReq.Header.Set("X-CSRF-Token", body.CSRFToken)
	logoutRec = httptest.NewRecorder()
	handler.ServeHTTP(logoutRec, logoutReq)
	require.Equal(t, http.StatusOK, logoutRec.Code)

	// Session is invalid after logout.
	meReq := httptest.NewRequest(http.MethodGet, "/api/v1/auth/me", nil)
	meReq.AddCookie(cookies[0])
	meRec = httptest.NewRecorder()
	handler.ServeHTTP(meRec, meReq)
	require.Equal(t, http.StatusUnauthorized, meRec.Code)
}

func TestLoginErrors(t *testing.T) {
	_, handler := newTestAPI(t)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login",
		strings.NewReader(`{"password":"wrong-password"}`))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	require.Equal(t, http.StatusUnauthorized, rec.Code)
	require.Contains(t, rec.Body.String(), "invalid_credentials")

	req = httptest.NewRequest(http.MethodPost, "/api/v1/auth/login",
		strings.NewReader(`not-json`))
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	require.Equal(t, http.StatusBadRequest, rec.Code)

	req = httptest.NewRequest(http.MethodPost, "/api/v1/auth/login",
		strings.NewReader(`{}`))
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	require.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestUnauthenticatedRequests(t *testing.T) {
	_, handler := newTestAPI(t)
	for _, target := range []string{"/api/v1/auth/me", "/api/v1/auth/logout"} {
		method := http.MethodGet
		if strings.HasSuffix(target, "logout") {
			method = http.MethodPost
		}
		req := httptest.NewRequest(method, target, nil)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		require.Equal(t, http.StatusUnauthorized, rec.Code, target)
	}
}
