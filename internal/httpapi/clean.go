package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/ph0nkj/mailcontroller/internal/domain"
	"github.com/ph0nkj/mailcontroller/internal/jobs"
)

// Clean modes accepted by the clean preview/execute endpoints.
const (
	cleanModeMarkRead = "mark_read"
	cleanModeArchive  = "archive"
	cleanModeTrash    = "trash"
)

const (
	// confirmTokenTTL is how long a preview remains valid.
	confirmTokenTTL = 5 * time.Minute
	// cleanBatchSize is the number of messages per batchModify request.
	cleanBatchSize = 1000
	// cleanMaxMessages bounds a single clean operation.
	cleanMaxMessages = 1_000_000
)

var errUnknownMode = errors.New("mode must be one of mark_read, archive, trash")

// AuditStore records security-relevant actions.
type AuditStore interface {
	Add(ctx context.Context, entry domain.AuditEntry) error
}

type cleanPreviewRequest struct {
	Mode string `json:"mode"`
}

type cleanPreviewResponse struct {
	Mode      string    `json:"mode"`
	Count     int64     `json:"count"`
	Token     string    `json:"token"`
	ExpiresAt time.Time `json:"expires_at"`
}

type cleanExecuteRequest struct {
	Mode         string `json:"mode"`
	ConfirmToken string `json:"confirm_token"`
}

type cleanExecuteResponse struct {
	JobID string `json:"job_id"`
	Mode  string `json:"mode"`
	Count int64  `json:"count"`
}

type confirmStore struct {
	mu     sync.Mutex
	tokens map[string]confirmEntry
	ttl    time.Duration
	now    func() time.Time
}

type confirmEntry struct {
	mode      string
	count     int64
	expiresAt time.Time
}

func newConfirmStore(ttl time.Duration) *confirmStore {
	return &confirmStore{
		tokens: make(map[string]confirmEntry),
		ttl:    ttl,
		now:    time.Now,
	}
}

func (s *confirmStore) issue(mode string, count int64) (string, time.Time, error) {
	token, err := randomHex(32)
	if err != nil {
		return "", time.Time{}, err
	}
	expires := s.now().Add(s.ttl)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pruneLocked()
	s.tokens[token] = confirmEntry{mode: mode, count: count, expiresAt: expires}
	return token, expires, nil
}

// consume validates and invalidates a token in one step.
func (s *confirmStore) consume(token, mode string) (int64, bool) {
	if token == "" {
		return 0, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	entry, ok := s.tokens[token]
	if !ok {
		return 0, false
	}
	delete(s.tokens, token)
	if s.now().After(entry.expiresAt) || entry.mode != mode {
		return 0, false
	}
	return entry.count, true
}

func (s *confirmStore) pruneLocked() {
	now := s.now()
	for token, entry := range s.tokens {
		if now.After(entry.expiresAt) {
			delete(s.tokens, token)
		}
	}
}

func cleanAction(mode string) (query string, addLabels, removeLabels []string, err error) {
	switch mode {
	case cleanModeMarkRead:
		return "in:inbox is:unread", nil, []string{"UNREAD"}, nil
	case cleanModeArchive:
		return "in:inbox", nil, []string{"INBOX"}, nil
	case cleanModeTrash:
		return "in:inbox", []string{"TRASH"}, nil, nil
	default:
		return "", nil, nil, errUnknownMode
	}
}

func decodeCleanRequest(w http.ResponseWriter, r *http.Request, dst any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 4<<10)
	if err := json.NewDecoder(r.Body).Decode(dst); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid JSON body")
		return false
	}
	return true
}

func (a *API) handleCleanPreview(w http.ResponseWriter, r *http.Request) {
	if a.mailbox == nil {
		writeError(w, http.StatusConflict, "google_not_connected", "connect a Google account first")
		return
	}
	var req cleanPreviewRequest
	if !decodeCleanRequest(w, r, &req) {
		return
	}
	query, _, _, err := cleanAction(req.Mode)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	count, err := a.mailbox.CountMessages(r.Context(), query)
	if err != nil {
		a.writeMailError(w, "count clean preview", err)
		return
	}
	token, expires, err := a.confirms.issue(req.Mode, count)
	if err != nil {
		a.log.Error("issue confirm token", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "could not prepare confirmation")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, cleanPreviewResponse{
		Mode:      req.Mode,
		Count:     count,
		Token:     token,
		ExpiresAt: expires,
	})
}

func (a *API) handleCleanExecute(w http.ResponseWriter, r *http.Request) {
	if a.mailbox == nil {
		writeError(w, http.StatusConflict, "google_not_connected", "connect a Google account first")
		return
	}
	var req cleanExecuteRequest
	if !decodeCleanRequest(w, r, &req) {
		return
	}
	query, addLabels, removeLabels, err := cleanAction(req.Mode)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	count, ok := a.confirms.consume(req.ConfirmToken, req.Mode)
	if !ok {
		writeError(w, http.StatusConflict, "confirm_expired", "confirmation expired or invalid; preview again")
		return
	}

	a.auditAction(r.Context(), domain.AuditEntry{
		Action:  "mail.clean.execute",
		Details: auditDetails(map[string]any{"mode": req.Mode, "planned": count}),
	})

	job := a.jobs.Start("clean:"+req.Mode, count, a.cleanJob(req.Mode, query, addLabels, removeLabels))
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusAccepted, cleanExecuteResponse{JobID: job.ID, Mode: req.Mode, Count: count})
}

func (a *API) cleanJob(mode, query string, addLabels, removeLabels []string) jobs.JobFunc {
	return func(ctx context.Context, progress *jobs.Progress) error {
		a.mailCache.Clear()
		defer a.mailCache.Clear()

		ids, err := a.mailbox.ListMessageIDs(ctx, query, cleanMaxMessages)
		if err != nil {
			a.auditCleanResult(mode, 0, err)
			return err
		}
		progress.SetTotal(int64(len(ids)))

		affected := 0
		for start := 0; start < len(ids); start += cleanBatchSize {
			end := start + cleanBatchSize
			if end > len(ids) {
				end = len(ids)
			}
			if err := a.mailbox.BatchModify(ctx, ids[start:end], addLabels, removeLabels); err != nil {
				a.auditCleanResult(mode, affected, err)
				return err
			}
			affected += end - start
			progress.Report(int64(affected))
		}
		a.auditCleanResult(mode, affected, nil)
		return nil
	}
}

func (a *API) auditCleanResult(mode string, affected int, jobErr error) {
	details := map[string]any{"mode": mode, "affected": affected, "ok": jobErr == nil}
	a.auditAction(context.Background(), domain.AuditEntry{
		Action:  "mail.clean.finish",
		Details: auditDetails(details),
	})
}

func (a *API) auditAction(ctx context.Context, entry domain.AuditEntry) {
	if a.audit == nil {
		return
	}
	if entry.Actor == "" {
		entry.Actor = "admin"
	}
	auditCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if err := a.audit.Add(auditCtx, entry); err != nil {
		a.log.Warn("write audit entry", "action", entry.Action, "error", err)
	}
}

func auditDetails(value map[string]any) string {
	raw, err := json.Marshal(value)
	if err != nil {
		return "{}"
	}
	return string(raw)
}

func (a *API) handleJobGet(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	job, ok := a.jobs.Get(id)
	if !ok {
		writeError(w, http.StatusNotFound, "job_not_found", "job not found")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, job)
}

func (a *API) jobRoutes(r chi.Router) {
	r.Get("/jobs/{id}", a.handleJobGet)
}
