// Package httpapi exposes the MailController HTTP API and the embedded web UI.
package httpapi

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"net/http"
	"runtime/debug"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/ph0nkj/mailcontroller/internal/auth"
	"github.com/ph0nkj/mailcontroller/internal/config"
)

type contextKey string

const requestIDKey contextKey = "request_id"

// AuthService is the authentication surface required by the HTTP layer.
type AuthService interface {
	Login(ctx context.Context, password, ip string) (auth.Session, error)
	Session(token string) (auth.Session, bool)
	Logout(token string)
	Secure() bool
	SessionTTL() time.Duration
}

// Deps contains external services used by HTTP handlers.
type Deps struct {
	Auth AuthService
}

// API wires HTTP handlers to application services.
type API struct {
	log  *slog.Logger
	cfg  config.Config
	auth AuthService
}

// New returns an API with the supplied logger, configuration and dependencies.
func New(log *slog.Logger, cfg config.Config, deps Deps) *API {
	return &API{log: log, cfg: cfg, auth: deps.Auth}
}

// Router builds the full HTTP handler, including middleware and API routes.
func (a *API) Router() http.Handler {
	r := chi.NewRouter()
	r.Use(a.requestID)
	r.Use(a.recoverer)
	r.Use(a.logger)
	r.Use(securityHeaders)

	r.Get("/healthz", a.handleHealth)

	r.Route("/api/v1", func(r chi.Router) {
		r.Get("/healthz", a.handleHealth)
		r.Post("/auth/login", a.handleLogin)
		r.Group(func(r chi.Router) {
			r.Use(a.authenticate)
			r.Get("/auth/me", a.handleMe)
			r.With(a.requireCSRF).Post("/auth/logout", a.handleLogout)
		})
		r.NotFound(func(w http.ResponseWriter, _ *http.Request) {
			writeError(w, http.StatusNotFound, "not_found", "resource not found")
		})
	})

	r.NotFound(a.serveWeb)
	r.MethodNotAllowed(func(w http.ResponseWriter, _ *http.Request) {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
	})
	return r
}

func (a *API) handleHealth(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "same-origin")
		next.ServeHTTP(w, r)
	})
}

func (a *API) requestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get("X-Request-ID")
		if id == "" || len(id) > 64 {
			b := make([]byte, 8)
			if _, err := rand.Read(b); err != nil {
				id = "unknown"
			} else {
				id = hex.EncodeToString(b)
			}
		}
		w.Header().Set("X-Request-ID", id)
		ctx := r.Context()
		next.ServeHTTP(w, r.WithContext(contextWithRequestID(ctx, id)))
	})
}

func (a *API) recoverer(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				a.log.Error("panic recovered",
					"request_id", RequestID(r.Context()),
					"method", r.Method,
					"path", r.URL.Path,
					"panic", rec,
					"stack", string(debug.Stack()),
				)
				writeError(w, http.StatusInternalServerError, "internal_error", "internal server error")
			}
		}()
		next.ServeHTTP(w, r)
	})
}

type statusWriter struct {
	http.ResponseWriter
	status int
}

func (s *statusWriter) WriteHeader(status int) {
	if s.status == 0 {
		s.status = status
	}
	s.ResponseWriter.WriteHeader(status)
}

func (s *statusWriter) Write(b []byte) (int, error) {
	if s.status == 0 {
		s.status = http.StatusOK
	}
	return s.ResponseWriter.Write(b)
}

func (a *API) logger(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		sw := &statusWriter{ResponseWriter: w}
		next.ServeHTTP(sw, r)
		if sw.status == 0 {
			sw.status = http.StatusOK
		}
		a.log.Info("request",
			"request_id", RequestID(r.Context()),
			"method", r.Method,
			"path", r.URL.Path,
			"status", sw.status,
			"duration_ms", time.Since(start).Milliseconds(),
			"remote", r.RemoteAddr,
		)
	})
}

// RequestID returns the request identifier stored in ctx, if any.
func RequestID(ctx context.Context) string {
	if id, ok := ctx.Value(requestIDKey).(string); ok {
		return id
	}
	return ""
}

func contextWithRequestID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, requestIDKey, id)
}

// ErrorResponse is the canonical JSON error envelope.
type ErrorResponse struct {
	Error ErrorBody `json:"error"`
}

// ErrorBody describes an API error.
type ErrorBody struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, ErrorResponse{Error: ErrorBody{Code: code, Message: message}})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		slog.Error("encode response", "error", err)
	}
}
