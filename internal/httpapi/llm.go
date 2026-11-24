package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/ph0nkj/mailcontroller/internal/gmail"
	"github.com/ph0nkj/mailcontroller/internal/llm"
)

const (
	// llmSettingsBodyLimit caps provider settings payloads.
	llmSettingsBodyLimit = 16 << 10
	// assistantSummarizeBodyLimit caps summary requests.
	assistantSummarizeBodyLimit = 64 << 10
	// assistantDraftBodyLimit caps reply-drafting requests.
	assistantDraftBodyLimit = 16 << 10
	// assistantMaxRequestIDs bounds IDs accepted in one summary request.
	assistantMaxRequestIDs = 500
)

// LLMService is the AI provider surface required by HTTP handlers.
type LLMService interface {
	Status(ctx context.Context) (llm.Status, error)
	Save(ctx context.Context, settings llm.Settings) (llm.Status, error)
	Clear(ctx context.Context) error
	Test(ctx context.Context) error
	Summarize(ctx context.Context, ids []string) (llm.SummaryResult, error)
	DraftReply(ctx context.Context, messageID, instructions string) (llm.Draft, error)
}

type llmSettingsRequest struct {
	Provider string `json:"provider"`
	BaseURL  string `json:"base_url"`
	Model    string `json:"model"`
	APIKey   string `json:"api_key"`
}

type assistantSummarizeRequest struct {
	MessageIDs []string `json:"message_ids"`
}

type assistantDraftRequest struct {
	MessageID    string `json:"message_id"`
	Instructions string `json:"instructions"`
}

func (a *API) handleLLMStatus(w http.ResponseWriter, r *http.Request) {
	status, err := a.llm.Status(r.Context())
	if err != nil {
		a.writeLLMError(w, "load AI provider status", err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, status)
}

func (a *API) handleLLMSettingsPut(w http.ResponseWriter, r *http.Request) {
	var req llmSettingsRequest
	if !decodeLLMJSON(w, r, llmSettingsBodyLimit, &req) {
		return
	}
	status, err := a.llm.Save(r.Context(), llm.Settings{
		Provider: req.Provider,
		BaseURL:  req.BaseURL,
		Model:    req.Model,
		APIKey:   req.APIKey,
	})
	if err != nil {
		a.writeLLMError(w, "save AI provider settings", err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, status)
}

func (a *API) handleLLMSettingsDelete(w http.ResponseWriter, r *http.Request) {
	if err := a.llm.Clear(r.Context()); err != nil {
		a.writeLLMError(w, "clear AI provider settings", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *API) handleLLMTest(w http.ResponseWriter, r *http.Request) {
	if err := a.llm.Test(r.Context()); err != nil {
		a.writeLLMError(w, "test AI provider", err)
		return
	}
	status, err := a.llm.Status(r.Context())
	if err != nil {
		a.writeLLMError(w, "load AI provider status", err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, status)
}

func (a *API) handleAssistantSummarize(w http.ResponseWriter, r *http.Request) {
	var req assistantSummarizeRequest
	if !decodeLLMJSON(w, r, assistantSummarizeBodyLimit, &req) {
		return
	}
	if len(req.MessageIDs) > assistantMaxRequestIDs {
		writeError(w, http.StatusBadRequest, "invalid_request", "too many messages in one request")
		return
	}
	result, err := a.llm.Summarize(r.Context(), req.MessageIDs)
	if err != nil {
		a.writeLLMError(w, "summarize messages", err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, result)
}

func (a *API) handleAssistantDraftReply(w http.ResponseWriter, r *http.Request) {
	var req assistantDraftRequest
	if !decodeLLMJSON(w, r, assistantDraftBodyLimit, &req) {
		return
	}
	draft, err := a.llm.DraftReply(r.Context(), req.MessageID, req.Instructions)
	if err != nil {
		a.writeLLMError(w, "draft reply", err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, draft)
}

func decodeLLMJSON(w http.ResponseWriter, r *http.Request, limit int64, dst any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, limit)
	if err := json.NewDecoder(r.Body).Decode(dst); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid JSON body")
		return false
	}
	return true
}

func (a *API) writeLLMError(w http.ResponseWriter, operation string, err error) {
	switch {
	case errors.Is(err, llm.ErrInvalidRequest), errors.Is(err, llm.ErrInvalidSettings):
		writeError(w, http.StatusBadRequest, "invalid_request", llmMessage(err))
	case errors.Is(err, llm.ErrNotConfigured):
		writeError(w, http.StatusConflict, "llm_not_configured", "configure an AI provider first")
	case errors.Is(err, gmail.ErrNotConfigured):
		writeError(w, http.StatusConflict, "google_not_configured", "save Google OAuth client credentials first")
	case errors.Is(err, gmail.ErrNotConnected):
		writeError(w, http.StatusConflict, "google_not_connected", "connect a Google account first")
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		writeError(w, http.StatusRequestTimeout, "request_cancelled", "request was cancelled")
	case errors.Is(err, llm.ErrProvider):
		a.log.Warn(operation, "error", err)
		writeError(w, http.StatusBadGateway, "llm_error", "AI provider request failed")
	default:
		a.log.Error(operation, "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "assistant request failed")
	}
}

// llmMessage strips internal error prefixes before returning validation details.
func llmMessage(err error) string {
	message := strings.TrimPrefix(err.Error(), "llm: ")
	message = strings.TrimPrefix(message, "invalid request: ")
	message = strings.TrimPrefix(message, "invalid settings: ")
	return message
}

func (a *API) llmRoutes(r chi.Router) {
	if a.llm == nil {
		return
	}
	r.Get("/llm/status", a.handleLLMStatus)
	r.With(a.requireCSRF).Put("/llm/settings", a.handleLLMSettingsPut)
	r.With(a.requireCSRF).Delete("/llm/settings", a.handleLLMSettingsDelete)
	r.With(a.requireCSRF).Post("/llm/test", a.handleLLMTest)
	r.With(a.requireCSRF).Post("/assistant/summarize", a.handleAssistantSummarize)
	r.With(a.requireCSRF).Post("/assistant/draft-reply", a.handleAssistantDraftReply)
}
