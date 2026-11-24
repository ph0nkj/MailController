package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/ph0nkj/mailcontroller/internal/domain"
	"github.com/ph0nkj/mailcontroller/internal/jobs"
)

type fakeAudit struct {
	mu      sync.Mutex
	entries []domain.AuditEntry
}

func (f *fakeAudit) Add(_ context.Context, entry domain.AuditEntry) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.entries = append(f.entries, entry)
	return nil
}

func (f *fakeAudit) list() []domain.AuditEntry {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]domain.AuditEntry(nil), f.entries...)
}

func newCleanTestAPI(t *testing.T, mailbox *fakeMailbox, audit *fakeAudit) (*jobs.Runner, http.Handler) {
	t.Helper()
	runner := jobs.NewRunner(10, 30*time.Second)
	_, handler := newTestAPIWithDeps(t, Deps{
		Mailbox: mailbox,
		Audit:   audit,
		Jobs:    runner,
	})
	return runner, handler
}

func doJSON(t *testing.T, handler http.Handler, cookie *http.Cookie, csrf, method, target, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(cookie)
	if csrf != "" {
		req.Header.Set("X-CSRF-Token", csrf)
	}
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec
}

func previewClean(t *testing.T, handler http.Handler, cookie *http.Cookie, csrf, mode string) cleanPreviewResponse {
	t.Helper()
	rec := doJSON(t, handler, cookie, csrf, http.MethodPost, "/api/v1/mail/clean/preview", fmt.Sprintf(`{"mode":%q}`, mode))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var body cleanPreviewResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	require.Equal(t, mode, body.Mode)
	require.NotEmpty(t, body.Token)
	require.True(t, body.ExpiresAt.After(time.Now()))
	return body
}

func TestCleanPreview(t *testing.T) {
	mailbox := &fakeMailbox{counts: map[string]int64{
		"in:inbox is:unread": 17,
		"in:inbox":           42,
	}}
	_, handler := newCleanTestAPI(t, mailbox, &fakeAudit{})
	cookie, csrf := loginSession(t, handler)

	preview := previewClean(t, handler, cookie, csrf, cleanModeMarkRead)
	require.Equal(t, int64(17), preview.Count)
	require.Equal(t, "in:inbox is:unread", mailbox.countQueries[len(mailbox.countQueries)-1])

	preview = previewClean(t, handler, cookie, csrf, cleanModeArchive)
	require.Equal(t, int64(42), preview.Count)
	preview = previewClean(t, handler, cookie, csrf, cleanModeTrash)
	require.Equal(t, int64(42), preview.Count)

	// Unknown modes and missing CSRF are rejected.
	rec := doJSON(t, handler, cookie, csrf, http.MethodPost, "/api/v1/mail/clean/preview", `{"mode":"delete_everything"}`)
	require.Equal(t, http.StatusBadRequest, rec.Code)

	rec = doJSON(t, handler, cookie, "", http.MethodPost, "/api/v1/mail/clean/preview", `{"mode":"archive"}`)
	require.Equal(t, http.StatusForbidden, rec.Code)

	rec = doJSON(t, handler, cookie, csrf, http.MethodPost, "/api/v1/mail/clean/preview", `not-json`)
	require.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestCleanExecuteMarkRead(t *testing.T) {
	ids := make([]string, 1500)
	for i := range ids {
		ids[i] = fmt.Sprintf("id-%d", i)
	}
	mailbox := &fakeMailbox{
		counts: map[string]int64{"in:inbox is:unread": int64(len(ids))},
		ids:    ids,
	}
	audit := &fakeAudit{}
	runner, handler := newCleanTestAPI(t, mailbox, audit)
	cookie, csrf := loginSession(t, handler)

	preview := previewClean(t, handler, cookie, csrf, cleanModeMarkRead)
	require.Equal(t, int64(1500), preview.Count)

	rec := doJSON(t, handler, cookie, csrf, http.MethodPost, "/api/v1/mail/clean/execute",
		fmt.Sprintf(`{"mode":"mark_read","confirm_token":%q}`, preview.Token))
	require.Equal(t, http.StatusAccepted, rec.Code, rec.Body.String())
	var execute cleanExecuteResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &execute))
	require.NotEmpty(t, execute.JobID)
	require.Equal(t, int64(1500), execute.Count)

	require.Eventually(t, func() bool {
		job, ok := runner.Get(execute.JobID)
		return ok && job.Status == jobs.StatusDone
	}, 5*time.Second, 10*time.Millisecond)

	job, _ := runner.Get(execute.JobID)
	require.Equal(t, int64(1500), job.Total)
	require.Equal(t, int64(1500), job.Done)

	batches, adds, removes := mailbox.batchCalls()
	require.Len(t, batches, 2)
	require.Len(t, batches[0], 1000)
	require.Len(t, batches[1], 500)
	require.Equal(t, []string{"UNREAD"}, removes[0])
	require.Empty(t, adds[0])

	entries := audit.list()
	require.Len(t, entries, 2)
	require.Equal(t, "mail.clean.execute", entries[0].Action)
	require.Contains(t, entries[0].Details, `"planned":1500`)
	require.Equal(t, "mail.clean.finish", entries[1].Action)
	require.Contains(t, entries[1].Details, `"affected":1500`)
	require.Contains(t, entries[1].Details, `"ok":true`)

	// One-time token: reusing it fails.
	rec = doJSON(t, handler, cookie, csrf, http.MethodPost, "/api/v1/mail/clean/execute",
		fmt.Sprintf(`{"mode":"mark_read","confirm_token":%q}`, preview.Token))
	require.Equal(t, http.StatusConflict, rec.Code)
	require.Contains(t, rec.Body.String(), "confirm_expired")
}

func TestCleanExecuteArchiveAndTrashLabels(t *testing.T) {
	tests := []struct {
		mode    string
		query   string
		adds    []string
		removes []string
	}{
		{mode: cleanModeArchive, query: "in:inbox", removes: []string{"INBOX"}},
		{mode: cleanModeTrash, query: "in:inbox", adds: []string{"TRASH"}},
	}
	for _, tt := range tests {
		t.Run(tt.mode, func(t *testing.T) {
			mailbox := &fakeMailbox{
				counts: map[string]int64{tt.query: 3},
				ids:    []string{"a", "b", "c"},
			}
			runner, handler := newCleanTestAPI(t, mailbox, &fakeAudit{})
			cookie, csrf := loginSession(t, handler)

			preview := previewClean(t, handler, cookie, csrf, tt.mode)
			rec := doJSON(t, handler, cookie, csrf, http.MethodPost, "/api/v1/mail/clean/execute",
				fmt.Sprintf(`{"mode":%q,"confirm_token":%q}`, tt.mode, preview.Token))
			require.Equal(t, http.StatusAccepted, rec.Code, rec.Body.String())
			var execute cleanExecuteResponse
			require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &execute))

			require.Eventually(t, func() bool {
				job, ok := runner.Get(execute.JobID)
				return ok && job.Status == jobs.StatusDone
			}, 5*time.Second, 10*time.Millisecond)

			batches, adds, removes := mailbox.batchCalls()
			require.Len(t, batches, 1)
			require.Equal(t, []string{"a", "b", "c"}, batches[0])
			require.Equal(t, tt.adds, adds[0])
			require.Equal(t, tt.removes, removes[0])
			require.Equal(t, tt.query, mailbox.idsQuery)
			require.Equal(t, cleanMaxMessages, mailbox.idsLimit)
		})
	}
}

func TestCleanExecuteTokenValidation(t *testing.T) {
	mailbox := &fakeMailbox{counts: map[string]int64{"in:inbox": 1}}
	runner, handler := newCleanTestAPI(t, mailbox, &fakeAudit{})
	cookie, csrf := loginSession(t, handler)

	// Missing or bogus token.
	for _, body := range []string{
		`{"mode":"archive"}`,
		`{"mode":"archive","confirm_token":"bogus"}`,
	} {
		rec := doJSON(t, handler, cookie, csrf, http.MethodPost, "/api/v1/mail/clean/execute", body)
		require.Equal(t, http.StatusConflict, rec.Code, body)
	}

	// A token is bound to its mode.
	preview := previewClean(t, handler, cookie, csrf, cleanModeArchive)
	rec := doJSON(t, handler, cookie, csrf, http.MethodPost, "/api/v1/mail/clean/execute",
		fmt.Sprintf(`{"mode":"trash","confirm_token":%q}`, preview.Token))
	require.Equal(t, http.StatusConflict, rec.Code)

	rec = doJSON(t, handler, cookie, csrf, http.MethodPost, "/api/v1/mail/clean/execute",
		fmt.Sprintf(`{"mode":"archive","confirm_token":%q}`, preview.Token))
	require.Equal(t, http.StatusConflict, rec.Code, "token was consumed by the failed attempt")

	// Unknown mode is rejected before token handling.
	preview = previewClean(t, handler, cookie, csrf, cleanModeArchive)
	rec = doJSON(t, handler, cookie, csrf, http.MethodPost, "/api/v1/mail/clean/execute",
		fmt.Sprintf(`{"mode":"nuke","confirm_token":%q}`, preview.Token))
	require.Equal(t, http.StatusBadRequest, rec.Code)

	require.Equal(t, 0, runner.Active())
}

func TestCleanConfirmStoreExpiry(t *testing.T) {
	store := newConfirmStore(time.Minute)
	now := time.Now()
	store.now = func() time.Time { return now }

	token, _, err := store.issue(cleanModeArchive, 5)
	require.NoError(t, err)
	count, ok := store.consume(token, cleanModeArchive)
	require.True(t, ok)
	require.Equal(t, int64(5), count)

	// Consumed tokens cannot be reused.
	_, ok = store.consume(token, cleanModeArchive)
	require.False(t, ok)

	token, _, err = store.issue(cleanModeArchive, 5)
	require.NoError(t, err)
	now = now.Add(2 * time.Minute)
	_, ok = store.consume(token, cleanModeArchive)
	require.False(t, ok)

	_, ok = store.consume("", cleanModeArchive)
	require.False(t, ok)
}

func TestCleanJobFailureIsAudited(t *testing.T) {
	mailbox := &fakeMailbox{
		counts:   map[string]int64{"in:inbox": 2},
		ids:      []string{"a", "b"},
		batchErr: fmt.Errorf("gmail exploded"),
	}
	audit := &fakeAudit{}
	runner, handler := newCleanTestAPI(t, mailbox, audit)
	cookie, csrf := loginSession(t, handler)

	preview := previewClean(t, handler, cookie, csrf, cleanModeArchive)
	rec := doJSON(t, handler, cookie, csrf, http.MethodPost, "/api/v1/mail/clean/execute",
		fmt.Sprintf(`{"mode":"archive","confirm_token":%q}`, preview.Token))
	require.Equal(t, http.StatusAccepted, rec.Code)
	var execute cleanExecuteResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &execute))

	require.Eventually(t, func() bool {
		job, ok := runner.Get(execute.JobID)
		return ok && job.Status == jobs.StatusFailed
	}, 5*time.Second, 10*time.Millisecond)
	job, _ := runner.Get(execute.JobID)
	require.Contains(t, job.Error, "gmail exploded")

	entries := audit.list()
	require.Len(t, entries, 2)
	require.Contains(t, entries[1].Details, `"ok":false`)
	require.Contains(t, entries[1].Details, `"affected":0`)
}

func TestCleanJobListingFailure(t *testing.T) {
	mailbox := &fakeMailbox{
		counts: map[string]int64{"in:inbox": 1},
		idsErr: fmt.Errorf("listing failed"),
	}
	audit := &fakeAudit{}
	runner, handler := newCleanTestAPI(t, mailbox, audit)
	cookie, csrf := loginSession(t, handler)

	preview := previewClean(t, handler, cookie, csrf, cleanModeTrash)
	rec := doJSON(t, handler, cookie, csrf, http.MethodPost, "/api/v1/mail/clean/execute",
		fmt.Sprintf(`{"mode":"trash","confirm_token":%q}`, preview.Token))
	require.Equal(t, http.StatusAccepted, rec.Code)
	var execute cleanExecuteResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &execute))

	require.Eventually(t, func() bool {
		job, ok := runner.Get(execute.JobID)
		return ok && job.Status == jobs.StatusFailed
	}, 5*time.Second, 10*time.Millisecond)
	require.Contains(t, audit.list()[1].Details, `"ok":false`)
}

func TestJobEndpoint(t *testing.T) {
	mailbox := &fakeMailbox{counts: map[string]int64{"in:inbox": 1}, ids: []string{"a"}}
	runner, handler := newCleanTestAPI(t, mailbox, &fakeAudit{})
	cookie, csrf := loginSession(t, handler)

	preview := previewClean(t, handler, cookie, csrf, cleanModeArchive)
	rec := doJSON(t, handler, cookie, csrf, http.MethodPost, "/api/v1/mail/clean/execute",
		fmt.Sprintf(`{"mode":"archive","confirm_token":%q}`, preview.Token))
	require.Equal(t, http.StatusAccepted, rec.Code)
	var execute cleanExecuteResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &execute))

	require.Eventually(t, func() bool {
		job, ok := runner.Get(execute.JobID)
		return ok && job.Status == jobs.StatusDone
	}, 5*time.Second, 10*time.Millisecond)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/jobs/"+execute.JobID, nil)
	req.AddCookie(cookie)
	getRec := httptest.NewRecorder()
	handler.ServeHTTP(getRec, req)
	require.Equal(t, http.StatusOK, getRec.Code)
	var job jobs.Job
	require.NoError(t, json.Unmarshal(getRec.Body.Bytes(), &job))
	require.Equal(t, execute.JobID, job.ID)
	require.Equal(t, jobs.StatusDone, job.Status)

	req = httptest.NewRequest(http.MethodGet, "/api/v1/jobs/unknown", nil)
	req.AddCookie(cookie)
	getRec = httptest.NewRecorder()
	handler.ServeHTTP(getRec, req)
	require.Equal(t, http.StatusNotFound, getRec.Code)

	req = httptest.NewRequest(http.MethodGet, "/api/v1/jobs/unknown", nil)
	getRec = httptest.NewRecorder()
	handler.ServeHTTP(getRec, req)
	require.Equal(t, http.StatusUnauthorized, getRec.Code)
}

func TestCleanActionMapping(t *testing.T) {
	query, add, remove, err := cleanAction(cleanModeMarkRead)
	require.NoError(t, err)
	require.Equal(t, "in:inbox is:unread", query)
	require.Empty(t, add)
	require.Equal(t, []string{"UNREAD"}, remove)

	query, add, remove, err = cleanAction(cleanModeArchive)
	require.NoError(t, err)
	require.Equal(t, "in:inbox", query)
	require.Empty(t, add)
	require.Equal(t, []string{"INBOX"}, remove)

	query, add, remove, err = cleanAction(cleanModeTrash)
	require.NoError(t, err)
	require.Equal(t, "in:inbox", query)
	require.Equal(t, []string{"TRASH"}, add)
	require.Empty(t, remove)

	_, _, _, err = cleanAction("delete")
	require.Error(t, err)
}
