package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/ph0nkj/mailcontroller/internal/auth"
)

const sessionKey contextKey = "session"

type loginRequest struct {
	Password string `json:"password"`
}

type loginResponse struct {
	CSRFToken string    `json:"csrf_token"`
	ExpiresAt time.Time `json:"expires_at"`
}

type meResponse struct {
	Authenticated bool      `json:"authenticated"`
	CSRFToken     string    `json:"csrf_token"`
	ExpiresAt     time.Time `json:"expires_at"`
}

func (a *API) handleLogin(w http.ResponseWriter, r *http.Request) {
	if a.auth == nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "authentication is not configured")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	r.Body = http.MaxBytesReader(w, r.Body, 4<<10)
	var req loginRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid JSON body")
		return
	}
	if req.Password == "" {
		writeError(w, http.StatusBadRequest, "invalid_request", "password is required")
		return
	}

	session, err := a.auth.Login(r.Context(), req.Password, remoteIP(r))
	switch {
	case errors.Is(err, auth.ErrRateLimited):
		a.log.Warn("login rate limited", "remote", remoteIP(r))
		writeError(w, http.StatusTooManyRequests, "rate_limited", "too many failed login attempts, try again later")
		return
	case errors.Is(err, auth.ErrInvalidCredentials):
		a.log.Warn("login failed", "remote", remoteIP(r))
		writeError(w, http.StatusUnauthorized, "invalid_credentials", "invalid password")
		return
	case err != nil:
		a.log.Error("login error", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "could not sign in")
		return
	}

	http.SetCookie(w, &http.Cookie{
		Name:     auth.SessionCookieName,
		Value:    session.Token,
		Path:     "/",
		HttpOnly: true,
		Secure:   a.cookieSecure(),
		SameSite: http.SameSiteLaxMode,
		Expires:  session.ExpiresAt,
		MaxAge:   int(time.Until(session.ExpiresAt).Seconds()),
	})
	writeJSON(w, http.StatusOK, loginResponse{CSRFToken: session.CSRF, ExpiresAt: session.ExpiresAt})
}

func (a *API) handleMe(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	session, ok := sessionFrom(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized", "not signed in")
		return
	}
	writeJSON(w, http.StatusOK, meResponse{
		Authenticated: true,
		CSRFToken:     session.CSRF,
		ExpiresAt:     session.ExpiresAt,
	})
}

func (a *API) handleLogout(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if session, ok := sessionFrom(r.Context()); ok {
		a.auth.Logout(session.Token)
	}
	http.SetCookie(w, &http.Cookie{
		Name:     auth.SessionCookieName,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		Secure:   a.cookieSecure(),
		SameSite: http.SameSiteLaxMode,
		MaxAge:   -1,
		Expires:  time.Unix(1, 0),
	})
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (a *API) authenticate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if a.auth == nil {
			writeError(w, http.StatusInternalServerError, "internal_error", "authentication is not configured")
			return
		}
		cookie, err := r.Cookie(auth.SessionCookieName)
		if err != nil {
			writeError(w, http.StatusUnauthorized, "unauthorized", "not signed in")
			return
		}
		session, ok := a.auth.Session(cookie.Value)
		if !ok {
			writeError(w, http.StatusUnauthorized, "unauthorized", "session expired")
			return
		}
		ctx := context.WithValue(r.Context(), sessionKey, session)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func (a *API) requireCSRF(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet, http.MethodHead, http.MethodOptions:
			next.ServeHTTP(w, r)
			return
		}
		session, ok := sessionFrom(r.Context())
		if !ok {
			writeError(w, http.StatusUnauthorized, "unauthorized", "not signed in")
			return
		}
		if err := auth.ValidateCSRF(session, r.Header.Get("X-CSRF-Token")); err != nil {
			writeError(w, http.StatusForbidden, "csrf_failed", "invalid or missing CSRF token")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (a *API) cookieSecure() bool {
	if a.auth != nil {
		return a.auth.Secure()
	}
	return a.cfg.CookieSecure
}

func sessionFrom(ctx context.Context) (auth.Session, bool) {
	session, ok := ctx.Value(sessionKey).(auth.Session)
	return session, ok
}

func remoteIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err == nil {
		return host
	}
	return strings.TrimSpace(r.RemoteAddr)
}
