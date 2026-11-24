package httpapi

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/ph0nkj/mailcontroller/internal/domain"
	"github.com/ph0nkj/mailcontroller/internal/gmail"
)

const (
	// mailCacheTTL bounds how long mailbox data is cached in memory.
	mailCacheTTL = 60 * time.Second
	// mailCacheMaxEntries caps memory use of the mailbox cache.
	mailCacheMaxEntries = 128
	// mailDefaultPageSize is the default number of messages per page.
	mailDefaultPageSize = 25
	// mailMaxPageSize is the maximum number of messages per page.
	mailMaxPageSize = 100
)

// MailboxService is the Gmail surface required by mailbox HTTP handlers.
type MailboxService interface {
	ListMessages(ctx context.Context, opts gmail.ListOptions) (gmail.ListResult, error)
	GetMessage(ctx context.Context, id string) (domain.Message, error)
	CountMessages(ctx context.Context, query string) (int64, error)
	ListMessageIDs(ctx context.Context, query string, limit int) ([]string, error)
	BatchModify(ctx context.Context, ids, addLabels, removeLabels []string) error
}

type messageDTO struct {
	ID       string    `json:"id"`
	ThreadID string    `json:"thread_id"`
	From     string    `json:"from"`
	To       string    `json:"to"`
	Cc       string    `json:"cc,omitempty"`
	Subject  string    `json:"subject"`
	Snippet  string    `json:"snippet"`
	Date     time.Time `json:"date"`
	Unread   bool      `json:"unread"`
	Labels   []string  `json:"labels"`
}

type messageDetailDTO struct {
	messageDTO
	BodyText string `json:"body_text"`
	BodyHTML string `json:"body_html"`
}

type messageListDTO struct {
	Items          []messageDTO `json:"items"`
	NextPageToken  string       `json:"next_page_token"`
	EstimatedTotal int64        `json:"estimated_total"`
}

type mailStatsDTO struct {
	InboxTotal  int64 `json:"inbox_total"`
	InboxUnread int64 `json:"inbox_unread"`
}

func toMessageDTO(message domain.Message) messageDTO {
	labels := message.Labels
	if labels == nil {
		labels = []string{}
	}
	return messageDTO{
		ID:       message.ID,
		ThreadID: message.ThreadID,
		From:     message.From,
		To:       message.To,
		Cc:       message.Cc,
		Subject:  message.Subject,
		Snippet:  message.Snippet,
		Date:     message.Date,
		Unread:   message.Unread,
		Labels:   labels,
	}
}

func (a *API) handleMailList(w http.ResponseWriter, r *http.Request) {
	if a.mailbox == nil {
		writeError(w, http.StatusConflict, "google_not_connected", "connect a Google account first")
		return
	}
	pageSize, err := parsePageSize(r.URL.Query().Get("page_size"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	opts := gmail.ListOptions{
		Query:     strings.TrimSpace(r.URL.Query().Get("q")),
		Label:     strings.TrimSpace(r.URL.Query().Get("label")),
		Unread:    r.URL.Query().Get("unread") == "true",
		PageToken: r.URL.Query().Get("page_token"),
		PageSize:  pageSize,
	}
	if len(opts.Query) > 1024 {
		writeError(w, http.StatusBadRequest, "invalid_request", "search query is too long")
		return
	}
	if len(opts.PageToken) > 4096 {
		writeError(w, http.StatusBadRequest, "invalid_request", "page token is too long")
		return
	}

	cacheKey := fmt.Sprintf("list|%s|%s|%t|%s|%d", opts.Query, opts.Label, opts.Unread, opts.PageToken, opts.PageSize)
	if cached, ok := a.mailCache.Get(cacheKey); ok {
		w.Header().Set("Cache-Control", "no-store")
		writeJSON(w, http.StatusOK, cached)
		return
	}

	result, err := a.mailbox.ListMessages(r.Context(), opts)
	if err != nil {
		a.writeMailError(w, "list messages", err)
		return
	}
	items := make([]messageDTO, len(result.Messages))
	for i, message := range result.Messages {
		items[i] = toMessageDTO(message)
	}
	response := messageListDTO{
		Items:          items,
		NextPageToken:  result.NextPageToken,
		EstimatedTotal: result.EstimatedTotal,
	}
	a.mailCache.Set(cacheKey, response)
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, response)
}

func (a *API) handleMailGet(w http.ResponseWriter, r *http.Request) {
	if a.mailbox == nil {
		writeError(w, http.StatusConflict, "google_not_connected", "connect a Google account first")
		return
	}
	id := strings.TrimSpace(chi.URLParam(r, "id"))
	if id == "" || len(id) > 256 {
		writeError(w, http.StatusBadRequest, "invalid_request", "message id is required")
		return
	}
	cacheKey := "message|" + id
	if cached, ok := a.mailCache.Get(cacheKey); ok {
		w.Header().Set("Cache-Control", "no-store")
		writeJSON(w, http.StatusOK, cached)
		return
	}
	message, err := a.mailbox.GetMessage(r.Context(), id)
	if err != nil {
		a.writeMailError(w, "get message", err)
		return
	}
	response := messageDetailDTO{
		messageDTO: toMessageDTO(message),
		BodyText:   message.BodyText,
		BodyHTML:   message.BodyHTML,
	}
	a.mailCache.Set(cacheKey, response)
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, response)
}

func (a *API) handleMailStats(w http.ResponseWriter, r *http.Request) {
	if a.mailbox == nil {
		writeError(w, http.StatusConflict, "google_not_connected", "connect a Google account first")
		return
	}
	if cached, ok := a.mailCache.Get("stats"); ok {
		w.Header().Set("Cache-Control", "no-store")
		writeJSON(w, http.StatusOK, cached)
		return
	}
	total, err := a.mailbox.CountMessages(r.Context(), "in:inbox")
	if err != nil {
		a.writeMailError(w, "count inbox", err)
		return
	}
	unread, err := a.mailbox.CountMessages(r.Context(), "in:inbox is:unread")
	if err != nil {
		a.writeMailError(w, "count unread inbox", err)
		return
	}
	response := mailStatsDTO{InboxTotal: total, InboxUnread: unread}
	a.mailCache.Set("stats", response)
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, response)
}

func (a *API) writeMailError(w http.ResponseWriter, operation string, err error) {
	switch {
	case errors.Is(err, gmail.ErrNotConfigured):
		writeError(w, http.StatusConflict, "google_not_configured", "save Google OAuth client credentials first")
	case errors.Is(err, gmail.ErrNotConnected):
		writeError(w, http.StatusConflict, "google_not_connected", "connect a Google account first")
	case errors.Is(err, context.Canceled):
		writeError(w, http.StatusRequestTimeout, "request_cancelled", "request was cancelled")
	default:
		a.log.Error(operation, "error", err)
		writeError(w, http.StatusBadGateway, "gmail_error", "Gmail request failed")
	}
}

func parsePageSize(raw string) (int, error) {
	if raw == "" {
		return mailDefaultPageSize, nil
	}
	size, err := strconv.Atoi(raw)
	if err != nil || size < 1 {
		return 0, errors.New("page_size must be a positive integer")
	}
	if size > mailMaxPageSize {
		size = mailMaxPageSize
	}
	return size, nil
}

func (a *API) mailRoutes(r chi.Router) {
	if a.mailbox == nil {
		return
	}
	r.Get("/mail/messages", a.handleMailList)
	r.Get("/mail/messages/{id}", a.handleMailGet)
	r.Get("/mail/stats", a.handleMailStats)
	r.With(a.requireCSRF).Post("/mail/clean/preview", a.handleCleanPreview)
	r.With(a.requireCSRF).Post("/mail/clean/execute", a.handleCleanExecute)
	r.With(a.requireCSRF).Post("/mail/bulk", a.handleMailBulk)
}
