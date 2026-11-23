package httpapi

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/ph0nkj/mailcontroller/internal/gmail"
)

const oauthStateCookie = "mc_oauth_state"

// GoogleService is the Google integration surface required by the HTTP layer.
type GoogleService interface {
	Status(ctx context.Context) (gmail.Status, error)
	SaveClient(ctx context.Context, clientID, clientSecret string) error
	ConnectURL(ctx context.Context, state string) (string, error)
	Exchange(ctx context.Context, code string) (string, error)
	Disconnect(ctx context.Context) error
}

type googleClientRequest struct {
	ClientID     string `json:"client_id"`
	ClientSecret string `json:"client_secret"`
}

func (a *API) handleGoogleStatus(w http.ResponseWriter, r *http.Request) {
	if a.google == nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Google integration is not configured")
		return
	}
	status, err := a.google.Status(r.Context())
	if err != nil {
		a.log.Error("google status", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "could not read Google status")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, status)
}

func (a *API) handleGoogleSaveClient(w http.ResponseWriter, r *http.Request) {
	if a.google == nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Google integration is not configured")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 8<<10)
	var req googleClientRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid JSON body")
		return
	}
	if req.ClientID == "" || req.ClientSecret == "" {
		writeError(w, http.StatusBadRequest, "invalid_request", "client_id and client_secret are required")
		return
	}
	if err := a.google.SaveClient(r.Context(), req.ClientID, req.ClientSecret); err != nil {
		a.log.Error("save google client", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "could not save Google credentials")
		return
	}
	status, err := a.google.Status(r.Context())
	if err != nil {
		a.log.Error("google status after save", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "credentials saved but status could not be read")
		return
	}
	writeJSON(w, http.StatusOK, status)
}

func (a *API) handleGoogleConnect(w http.ResponseWriter, r *http.Request) {
	if a.google == nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Google integration is not configured")
		return
	}
	state, err := randomHex(32)
	if err != nil {
		a.log.Error("generate oauth state", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "could not start Google sign-in")
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name:     oauthStateCookie,
		Value:    state,
		Path:     "/api/v1/google",
		HttpOnly: true,
		Secure:   a.cookieSecure(),
		SameSite: http.SameSiteLaxMode,
		MaxAge:   600,
		Expires:  time.Now().Add(10 * time.Minute),
	})
	connectURL, err := a.google.ConnectURL(r.Context(), state)
	switch {
	case errors.Is(err, gmail.ErrNotConfigured):
		writeError(w, http.StatusBadRequest, "google_not_configured", "save Google OAuth client credentials first")
		return
	case err != nil:
		a.log.Error("build google connect URL", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "could not start Google sign-in")
		return
	}
	http.Redirect(w, r, connectURL, http.StatusFound)
}

func (a *API) handleGoogleCallback(w http.ResponseWriter, r *http.Request) {
	clearOAuthStateCookie(w, a.cookieSecure())
	if a.google == nil {
		http.Redirect(w, r, "/settings?google=error", http.StatusFound)
		return
	}
	cookie, err := r.Cookie(oauthStateCookie)
	state := r.URL.Query().Get("state")
	if err != nil || state == "" || state != cookie.Value {
		a.log.Warn("google oauth state mismatch")
		http.Redirect(w, r, "/settings?google=error", http.StatusFound)
		return
	}
	if oauthErr := r.URL.Query().Get("error"); oauthErr != "" {
		a.log.Warn("google oauth denied", "error", oauthErr)
		http.Redirect(w, r, "/settings?google=error", http.StatusFound)
		return
	}
	code := r.URL.Query().Get("code")
	if code == "" {
		a.log.Warn("google oauth callback missing code")
		http.Redirect(w, r, "/settings?google=error", http.StatusFound)
		return
	}
	email, err := a.google.Exchange(r.Context(), code)
	if err != nil {
		a.log.Error("google oauth exchange", "error", err)
		http.Redirect(w, r, "/settings?google=error", http.StatusFound)
		return
	}
	a.log.Info("google account connected", "email", email)
	http.Redirect(w, r, "/settings?google=connected", http.StatusFound)
}

func (a *API) handleGoogleDisconnect(w http.ResponseWriter, r *http.Request) {
	if a.google == nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Google integration is not configured")
		return
	}
	if err := a.google.Disconnect(r.Context()); err != nil {
		a.log.Error("google disconnect", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "could not disconnect Google account")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (a *API) googleRoutes(r chi.Router) {
	if a.google == nil {
		return
	}
	r.Get("/google/status", a.handleGoogleStatus)
	r.With(a.requireCSRF).Put("/google/client", a.handleGoogleSaveClient)
	r.Get("/google/connect", a.handleGoogleConnect)
	r.With(a.requireCSRF).Delete("/google/disconnect", a.handleGoogleDisconnect)
}

func clearOAuthStateCookie(w http.ResponseWriter, secure bool) {
	http.SetCookie(w, &http.Cookie{
		Name:     oauthStateCookie,
		Value:    "",
		Path:     "/api/v1/google",
		HttpOnly: true,
		Secure:   secure,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   -1,
		Expires:  time.Unix(1, 0),
	})
}

func randomHex(n int) (string, error) {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}
