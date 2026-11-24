package httpapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/ph0nkj/mailcontroller/internal/domain"
	"github.com/ph0nkj/mailcontroller/internal/gmail"
)

func TestBulkActions(t *testing.T) {
	tests := []struct {
		action  string
		adds    []string
		removes []string
		confirm bool
	}{
		{action: bulkMarkRead, removes: []string{"UNREAD"}},
		{action: bulkMarkUnread, adds: []string{"UNREAD"}},
		{action: bulkArchive, removes: []string{"INBOX"}},
		{action: bulkTrash, adds: []string{"TRASH"}, confirm: true},
	}
	for _, tt := range tests {
		t.Run(tt.action, func(t *testing.T) {
			mailbox := &fakeMailbox{}
			audit := &fakeAudit{}
			_, handler := newCleanTestAPI(t, mailbox, audit)
			cookie, csrf := loginSession(t, handler)

			body := fmt.Sprintf(`{"ids":["m1","m2","m1"],"action":%q,"confirm":%t}`, tt.action, tt.confirm)
			rec := doJSON(t, handler, cookie, csrf, http.MethodPost, "/api/v1/mail/bulk", body)
			require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
			var response bulkResponse
			require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &response))
			require.Equal(t, tt.action, response.Action)
			require.Equal(t, 2, response.Affected)

			batches, adds, removes := mailbox.batchCalls()
			require.Len(t, batches, 1)
			require.Equal(t, []string{"m1", "m2"}, batches[0])
			if tt.adds == nil {
				require.Empty(t, adds[0])
			} else {
				require.Equal(t, tt.adds, adds[0])
			}
			if tt.removes == nil {
				require.Empty(t, removes[0])
			} else {
				require.Equal(t, tt.removes, removes[0])
			}

			entries := audit.list()
			require.Len(t, entries, 1)
			require.Equal(t, "mail.bulk", entries[0].Action)
			require.Contains(t, entries[0].Details, fmt.Sprintf(`"action":%q`, tt.action))
			require.Contains(t, entries[0].Details, `"count":2`)
		})
	}
}

func TestBulkTrashRequiresConfirmation(t *testing.T) {
	mailbox := &fakeMailbox{}
	audit := &fakeAudit{}
	_, handler := newCleanTestAPI(t, mailbox, audit)
	cookie, csrf := loginSession(t, handler)

	rec := doJSON(t, handler, cookie, csrf, http.MethodPost, "/api/v1/mail/bulk",
		`{"ids":["m1"],"action":"trash"}`)
	require.Equal(t, http.StatusBadRequest, rec.Code)
	require.Contains(t, rec.Body.String(), "confirmation_required")
	batches, _, _ := mailbox.batchCalls()
	require.Empty(t, batches)
	require.Empty(t, audit.list())
}

func TestBulkValidation(t *testing.T) {
	mailbox := &fakeMailbox{}
	_, handler := newCleanTestAPI(t, mailbox, &fakeAudit{})
	cookie, csrf := loginSession(t, handler)

	tests := []string{
		`not-json`,
		`{"action":"mark_read"}`,
		`{"ids":["m1"],"action":"delete"}`,
		`{"ids":[""],"action":"mark_read"}`,
		`{"ids":["m1"],"action":"mark_trash"}`,
	}
	for _, body := range tests {
		rec := doJSON(t, handler, cookie, csrf, http.MethodPost, "/api/v1/mail/bulk", body)
		require.Equal(t, http.StatusBadRequest, rec.Code, body)
	}

	// Missing CSRF is rejected.
	rec := doJSON(t, handler, cookie, "", http.MethodPost, "/api/v1/mail/bulk",
		`{"ids":["m1"],"action":"mark_read"}`)
	require.Equal(t, http.StatusForbidden, rec.Code)

	// Too many ids.
	ids := make([]string, bulkMaxMessages+1)
	for i := range ids {
		ids[i] = fmt.Sprintf("id-%d", i)
	}
	raw, err := json.Marshal(bulkRequest{IDs: ids, Action: bulkMarkRead})
	require.NoError(t, err)
	rec = doJSON(t, handler, cookie, csrf, http.MethodPost, "/api/v1/mail/bulk", string(raw))
	require.Equal(t, http.StatusBadRequest, rec.Code)
	require.Contains(t, rec.Body.String(), "at most 1000")
}

func TestBulkClearsMailCache(t *testing.T) {
	mailbox := &fakeMailbox{
		listResult: gmail.ListResult{Messages: []domain.Message{{ID: "m1"}}},
	}
	_, handler := newCleanTestAPI(t, mailbox, &fakeAudit{})
	cookie, csrf := loginSession(t, handler)

	for i := 0; i < 2; i++ {
		rec := serveAuthed(handler, http.MethodGet, "/api/v1/mail/messages?q=cache", cookie)
		require.Equal(t, http.StatusOK, rec.Code)
	}
	require.Equal(t, 1, mailbox.listCalls)

	rec := doJSON(t, handler, cookie, csrf, http.MethodPost, "/api/v1/mail/bulk",
		`{"ids":["m1"],"action":"mark_read"}`)
	require.Equal(t, http.StatusOK, rec.Code)

	rec = serveAuthed(handler, http.MethodGet, "/api/v1/mail/messages?q=cache", cookie)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, 2, mailbox.listCalls, "bulk action should invalidate the list cache")
}

func TestBulkGmailError(t *testing.T) {
	mailbox := &fakeMailbox{batchErr: fmt.Errorf("boom")}
	_, handler := newCleanTestAPI(t, mailbox, &fakeAudit{})
	cookie, csrf := loginSession(t, handler)

	rec := doJSON(t, handler, cookie, csrf, http.MethodPost, "/api/v1/mail/bulk",
		`{"ids":["m1"],"action":"mark_read"}`)
	require.Equal(t, http.StatusBadGateway, rec.Code)
}

func serveAuthed(handler http.Handler, method, target string, cookie *http.Cookie) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, target, nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec
}
