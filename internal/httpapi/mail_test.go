package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/ph0nkj/mailcontroller/internal/domain"
	"github.com/ph0nkj/mailcontroller/internal/gmail"
)

type fakeMailbox struct {
	listOptions gmail.ListOptions
	listResult  gmail.ListResult
	listErr     error
	listCalls   int

	getID     string
	getResult domain.Message
	getErr    error
	getCalls  int

	countQueries []string
	counts       map[string]int64
	countErr     error

	idsQuery string
	idsLimit int
	ids      []string
	idsErr   error

	batchMu  sync.Mutex
	batches  [][]string
	adds     [][]string
	removes  [][]string
	batchErr error
}

func (f *fakeMailbox) ListMessages(_ context.Context, opts gmail.ListOptions) (gmail.ListResult, error) {
	f.listCalls++
	f.listOptions = opts
	return f.listResult, f.listErr
}

func (f *fakeMailbox) GetMessage(_ context.Context, id string) (domain.Message, error) {
	f.getCalls++
	f.getID = id
	return f.getResult, f.getErr
}

func (f *fakeMailbox) CountMessages(_ context.Context, query string) (int64, error) {
	f.countQueries = append(f.countQueries, query)
	if f.countErr != nil {
		return 0, f.countErr
	}
	return f.counts[query], nil
}

func (f *fakeMailbox) ListMessageIDs(_ context.Context, query string, limit int) ([]string, error) {
	f.idsQuery = query
	f.idsLimit = limit
	return f.ids, f.idsErr
}

func (f *fakeMailbox) BatchModify(_ context.Context, ids, addLabels, removeLabels []string) error {
	f.batchMu.Lock()
	defer f.batchMu.Unlock()
	f.batches = append(f.batches, append([]string(nil), ids...))
	f.adds = append(f.adds, append([]string(nil), addLabels...))
	f.removes = append(f.removes, append([]string(nil), removeLabels...))
	return f.batchErr
}

func (f *fakeMailbox) batchCalls() ([][]string, [][]string, [][]string) {
	f.batchMu.Lock()
	defer f.batchMu.Unlock()
	return f.batches, f.adds, f.removes
}

func newTestAPIWithMailbox(t *testing.T, mailbox MailboxService) (*API, http.Handler) {
	t.Helper()
	return newTestAPIWithDeps(t, Deps{Mailbox: mailbox})
}

func TestMailListPaginationAndQuery(t *testing.T) {
	date := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	fake := &fakeMailbox{
		listResult: gmail.ListResult{
			Messages: []domain.Message{
				{ID: "m1", ThreadID: "t1", From: "a@example.com", Subject: "Hello", Unread: true, Labels: []string{"INBOX", "UNREAD"}, Date: date},
				{ID: "m2", Subject: "Plain"},
			},
			NextPageToken:  "next-token",
			EstimatedTotal: 12,
		},
	}
	_, handler := newTestAPIWithMailbox(t, fake)
	cookie, _ := loginSession(t, handler)

	req := httptest.NewRequest(http.MethodGet,
		"/api/v1/mail/messages?q=hello&label=INBOX&unread=true&page_token=abc&page_size=50", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	var body messageListDTO
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	require.Len(t, body.Items, 2)
	require.Equal(t, "m1", body.Items[0].ID)
	require.True(t, body.Items[0].Unread)
	require.Equal(t, date, body.Items[0].Date)
	require.NotNil(t, body.Items[1].Labels)
	require.Equal(t, "next-token", body.NextPageToken)
	require.Equal(t, int64(12), body.EstimatedTotal)

	require.Equal(t, "hello", fake.listOptions.Query)
	require.Equal(t, "INBOX", fake.listOptions.Label)
	require.True(t, fake.listOptions.Unread)
	require.Equal(t, "abc", fake.listOptions.PageToken)
	require.Equal(t, 50, fake.listOptions.PageSize)
}

func TestMailListDefaultsAndClamp(t *testing.T) {
	fake := &fakeMailbox{}
	_, handler := newTestAPIWithMailbox(t, fake)
	cookie, _ := loginSession(t, handler)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/mail/messages", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, mailDefaultPageSize, fake.listOptions.PageSize)
	require.False(t, fake.listOptions.Unread)

	req = httptest.NewRequest(http.MethodGet, "/api/v1/mail/messages?page_size=5000", nil)
	req.AddCookie(cookie)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, mailMaxPageSize, fake.listOptions.PageSize)

	req = httptest.NewRequest(http.MethodGet, "/api/v1/mail/messages?page_size=nope", nil)
	req.AddCookie(cookie)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	require.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestMailListCache(t *testing.T) {
	fake := &fakeMailbox{listResult: gmail.ListResult{Messages: []domain.Message{{ID: "m1"}}}}
	_, handler := newTestAPIWithMailbox(t, fake)
	cookie, _ := loginSession(t, handler)

	for i := 0; i < 2; i++ {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/mail/messages?q=same", nil)
		req.AddCookie(cookie)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		require.Equal(t, http.StatusOK, rec.Code)
	}
	require.Equal(t, 1, fake.listCalls)

	// A different query misses the cache.
	req := httptest.NewRequest(http.MethodGet, "/api/v1/mail/messages?q=other", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, 2, fake.listCalls)
}

func TestMailGetMessage(t *testing.T) {
	fake := &fakeMailbox{getResult: domain.Message{
		ID: "m1", Subject: "Subject", BodyText: "text body", BodyHTML: "<p>html</p>",
	}}
	_, handler := newTestAPIWithMailbox(t, fake)
	cookie, _ := loginSession(t, handler)

	for i := 0; i < 2; i++ {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/mail/messages/m1", nil)
		req.AddCookie(cookie)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		var body messageDetailDTO
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
		require.Equal(t, "text body", body.BodyText)
		require.Equal(t, "<p>html</p>", body.BodyHTML)
	}
	require.Equal(t, 1, fake.getCalls, "message detail should be cached")
	require.Equal(t, "m1", fake.getID)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/mail/messages/%20", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	require.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestMailStats(t *testing.T) {
	fake := &fakeMailbox{counts: map[string]int64{
		"in:inbox":           42,
		"in:inbox is:unread": 7,
	}}
	_, handler := newTestAPIWithMailbox(t, fake)
	cookie, _ := loginSession(t, handler)

	for i := 0; i < 2; i++ {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/mail/stats", nil)
		req.AddCookie(cookie)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		var body mailStatsDTO
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
		require.Equal(t, int64(42), body.InboxTotal)
		require.Equal(t, int64(7), body.InboxUnread)
	}
	require.Len(t, fake.countQueries, 2, "stats should be cached")
}

func TestMailErrors(t *testing.T) {
	tests := []struct {
		name       string
		err        error
		wantStatus int
		wantCode   string
	}{
		{name: "not connected", err: gmail.ErrNotConnected, wantStatus: http.StatusConflict, wantCode: "google_not_connected"},
		{name: "not configured", err: gmail.ErrNotConfigured, wantStatus: http.StatusConflict, wantCode: "google_not_configured"},
		{name: "gmail failure", err: errors.New("boom"), wantStatus: http.StatusBadGateway, wantCode: "gmail_error"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fake := &fakeMailbox{listErr: tt.err, getErr: tt.err, countErr: tt.err}
			_, handler := newTestAPIWithMailbox(t, fake)
			cookie, _ := loginSession(t, handler)

			for _, target := range []string{"/api/v1/mail/messages", "/api/v1/mail/messages/m1", "/api/v1/mail/stats"} {
				req := httptest.NewRequest(http.MethodGet, target, nil)
				req.AddCookie(cookie)
				rec := httptest.NewRecorder()
				handler.ServeHTTP(rec, req)
				require.Equal(t, tt.wantStatus, rec.Code, target)
				require.Contains(t, rec.Body.String(), tt.wantCode, target)
			}
		})
	}
}

func TestMailRoutesRequireAuth(t *testing.T) {
	_, handler := newTestAPIWithMailbox(t, &fakeMailbox{})
	for _, target := range []string{"/api/v1/mail/messages", "/api/v1/mail/messages/m1", "/api/v1/mail/stats"} {
		req := httptest.NewRequest(http.MethodGet, target, nil)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		require.Equal(t, http.StatusUnauthorized, rec.Code, target)
	}
}

func TestMailRoutesAbsentWithoutService(t *testing.T) {
	_, handler := newTestAPI(t)
	cookie, _ := loginSession(t, handler)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/mail/messages", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	require.Equal(t, http.StatusNotFound, rec.Code)
}

func TestParsePageSize(t *testing.T) {
	size, err := parsePageSize("")
	require.NoError(t, err)
	require.Equal(t, mailDefaultPageSize, size)

	size, err = parsePageSize("100")
	require.NoError(t, err)
	require.Equal(t, 100, size)

	size, err = parsePageSize("999")
	require.NoError(t, err)
	require.Equal(t, mailMaxPageSize, size)

	for _, raw := range []string{"0", "-1", "x", strconv.Itoa(1 << 20)} {
		_, err := parsePageSize(raw)
		if raw == strconv.Itoa(1<<20) {
			require.NoError(t, err)
			continue
		}
		require.Error(t, err, raw)
	}
}
