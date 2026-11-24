package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/ph0nkj/mailcontroller/internal/agent"
	"github.com/ph0nkj/mailcontroller/internal/auth"
	"github.com/ph0nkj/mailcontroller/internal/config"
	"github.com/ph0nkj/mailcontroller/internal/domain"
	"github.com/ph0nkj/mailcontroller/internal/gmail"
	"github.com/ph0nkj/mailcontroller/internal/jobs"
	"github.com/ph0nkj/mailcontroller/internal/mailer"
	"github.com/ph0nkj/mailcontroller/internal/store"
)

// This file exercises the happy path through the real HTTP router, auth, store,
// mailer engine and agent service. Only external systems (Gmail, OAuth and the
// LLM planner) are faked.

type e2eSentMessage struct {
	to      string
	subject string
	text    string
	html    string
	headers map[string]string
}

type e2eSender struct {
	mu       sync.Mutex
	messages []e2eSentMessage
}

func (s *e2eSender) SendMessage(_ context.Context, to, subject, textBody, htmlBody string, headers map[string]string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	copied := make(map[string]string, len(headers))
	for name, value := range headers {
		copied[name] = value
	}
	s.messages = append(s.messages, e2eSentMessage{
		to:      to,
		subject: subject,
		text:    textBody,
		html:    htmlBody,
		headers: copied,
	})
	return fmt.Sprintf("sent-%d", len(s.messages)), nil
}

func (s *e2eSender) Retryable(error) bool { return false }

func (s *e2eSender) FromAddress() string { return "owner@example.com" }

func (s *e2eSender) all() []e2eSentMessage {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]e2eSentMessage(nil), s.messages...)
}

type e2eBatch struct {
	ids    []string
	add    []string
	remove []string
}

type e2eMailbox struct {
	counts map[string]int64
	ids    map[string][]string

	mu      sync.Mutex
	batches []e2eBatch
}

func (m *e2eMailbox) ListMessages(context.Context, gmail.ListOptions) (gmail.ListResult, error) {
	return gmail.ListResult{}, nil
}

func (m *e2eMailbox) GetMessage(context.Context, string) (domain.Message, error) {
	return domain.Message{}, nil
}

func (m *e2eMailbox) CountMessages(_ context.Context, query string) (int64, error) {
	return m.counts[query], nil
}

func (m *e2eMailbox) ListMessageIDs(_ context.Context, query string, limit int) ([]string, error) {
	ids := append([]string(nil), m.ids[query]...)
	if limit > 0 && len(ids) > limit {
		ids = ids[:limit]
	}
	return ids, nil
}

func (m *e2eMailbox) BatchModify(_ context.Context, ids, addLabels, removeLabels []string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.batches = append(m.batches, e2eBatch{
		ids:    append([]string(nil), ids...),
		add:    append([]string(nil), addLabels...),
		remove: append([]string(nil), removeLabels...),
	})
	return nil
}

func (m *e2eMailbox) allBatches() []e2eBatch {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]e2eBatch(nil), m.batches...)
}

type e2ePlanner struct{}

func (e2ePlanner) Plan(context.Context, string) (domain.AgentPlan, error) {
	return domain.AgentPlan{
		Summary: "Archive old messages",
		Steps: []domain.AgentStep{{
			Action: domain.AgentActionArchive,
			Query:  "in:inbox older_than:1y",
			Reason: "Old mail is no longer needed",
			Count:  10,
		}},
		Total: 10,
	}, nil
}

type e2eEnv struct {
	handler http.Handler
	runner  *jobs.Runner
	sender  *e2eSender
	mailbox *e2eMailbox
	google  *fakeGoogle
}

func newE2EEnv(t *testing.T) *e2eEnv {
	t.Helper()
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "e2e.db"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, st.Close()) })

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	authService, err := auth.New(context.Background(), st.Settings(), "test-password", false, logger)
	require.NoError(t, err)

	runner := jobs.NewRunner(20, time.Minute)
	sender := &e2eSender{}
	mailbox := &e2eMailbox{
		counts: map[string]int64{
			"in:inbox is:unread":     3,
			"in:inbox older_than:1y": 2,
		},
		ids: map[string][]string{
			"in:inbox is:unread":     {"msg-1", "msg-2", "msg-3"},
			"in:inbox older_than:1y": {"old-1", "old-2"},
		},
	}
	google := &fakeGoogle{
		status: gmail.Status{
			Configured: true,
			Connected:  true,
			Email:      "owner@example.com",
		},
		exchangeMail: "owner@example.com",
	}
	campaignEngine, err := mailer.NewEngine(logger, st.Campaigns(),
		func(context.Context) (mailer.Sender, error) { return sender, nil },
		runner, st.Audit(), mailer.WithUnsubscribe("unsub@example.com"))
	require.NoError(t, err)
	agentService, err := agent.NewService(e2ePlanner{}, mailbox, st.AgentRuns(), runner,
		st.Audit(), logger, agent.Options{MaxMessages: 100, MaxSteps: 5})
	require.NoError(t, err)

	api := New(logger, config.Config{}, Deps{
		Auth:           authService,
		Google:         google,
		Mailbox:        mailbox,
		Audit:          st.Audit(),
		Jobs:           runner,
		Campaigns:      st.Campaigns(),
		CampaignRunner: campaignEngine,
		Agent:          agentService,
	})
	return &e2eEnv{
		handler: api.Router(),
		runner:  runner,
		sender:  sender,
		mailbox: mailbox,
		google:  google,
	}
}

func waitE2EJob(t *testing.T, runner *jobs.Runner, id string) jobs.Job {
	t.Helper()
	var final jobs.Job
	require.Eventually(t, func() bool {
		job, ok := runner.Get(id)
		if !ok {
			return false
		}
		if job.Status == jobs.StatusDone || job.Status == jobs.StatusFailed {
			final = job
			return true
		}
		return false
	}, 5*time.Second, 5*time.Millisecond)
	return final
}

func doText(t *testing.T, handler http.Handler, cookie *http.Cookie, csrf, method, target, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	req.Header.Set("Content-Type", "text/plain")
	if cookie != nil {
		req.AddCookie(cookie)
	}
	if csrf != "" {
		req.Header.Set("X-CSRF-Token", csrf)
	}
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec
}

func TestEndToEndHappyPath(t *testing.T) {
	env := newE2EEnv(t)
	cookie, csrf := loginSession(t, env.handler)

	// The status endpoint reports a connected mailbox.
	rec := doJSON(t, env.handler, cookie, "", http.MethodGet, "/api/v1/google/status", "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Contains(t, rec.Body.String(), "owner@example.com")

	// Start the OAuth flow against the mock provider and complete the callback.
	req := httptest.NewRequest(http.MethodGet, "/api/v1/google/connect", nil)
	req.AddCookie(cookie)
	rec = httptest.NewRecorder()
	env.handler.ServeHTTP(rec, req)
	require.Equal(t, http.StatusFound, rec.Code, rec.Body.String())
	location, err := url.Parse(rec.Header().Get("Location"))
	require.NoError(t, err)
	state := location.Query().Get("state")
	require.NotEmpty(t, state)
	result := rec.Result()
	cookies := result.Cookies()
	require.NoError(t, result.Body.Close())
	var stateCookie *http.Cookie
	for _, candidate := range cookies {
		if candidate.Name == oauthStateCookie {
			stateCookie = candidate
		}
	}
	require.NotNil(t, stateCookie)

	req = httptest.NewRequest(http.MethodGet,
		"/api/v1/google/callback?state="+url.QueryEscape(state)+"&code=test-code", nil)
	req.AddCookie(stateCookie)
	rec = httptest.NewRecorder()
	env.handler.ServeHTTP(rec, req)
	require.Equal(t, http.StatusFound, rec.Code)
	require.Equal(t, "/settings?google=connected", rec.Header().Get("Location"))
	require.Equal(t, "test-code", env.google.exchangeCode)

	// Clean All preview and execution.
	rec = doJSON(t, env.handler, cookie, csrf, http.MethodPost, "/api/v1/mail/clean/preview", `{"mode":"mark_read"}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var preview cleanPreviewResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &preview))
	require.Equal(t, int64(3), preview.Count)
	require.NotEmpty(t, preview.Token)

	rec = doJSON(t, env.handler, cookie, csrf, http.MethodPost, "/api/v1/mail/clean/execute",
		fmt.Sprintf(`{"mode":"mark_read","confirm_token":%q}`, preview.Token))
	require.Equal(t, http.StatusAccepted, rec.Code, rec.Body.String())
	var cleanExec cleanExecuteResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &cleanExec))
	require.Equal(t, jobs.StatusDone, waitE2EJob(t, env.runner, cleanExec.JobID).Status)

	batches := env.mailbox.allBatches()
	require.Len(t, batches, 1)
	require.Equal(t, []string{"msg-1", "msg-2", "msg-3"}, batches[0].ids)
	require.Equal(t, []string{"UNREAD"}, batches[0].remove)

	// Create a campaign, import recipients and send it.
	rec = doJSON(t, env.handler, cookie, csrf, http.MethodPost, "/api/v1/campaigns", `{
		"name":"E2E campaign",
		"subject":"Hello {{email}}",
		"body_text":"Text for {{email}}",
		"body_html":"<p>Hello {{email}}</p>",
		"daily_limit":100,
		"rate_per_min":600
	}`)
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	var campaign campaignDetailDTO
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &campaign))
	require.NotEmpty(t, campaign.ID)

	rec = doText(t, env.handler, cookie, csrf, http.MethodPost,
		"/api/v1/campaigns/"+campaign.ID+"/recipients",
		"alice@example.com\nbob@example.com\n")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var imported campaignImportResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &imported))
	require.Equal(t, 2, imported.Imported)
	require.Equal(t, 2, imported.Total)

	rec = doJSON(t, env.handler, cookie, csrf, http.MethodPost,
		"/api/v1/campaigns/"+campaign.ID+"/start", `{"confirm":true}`)
	require.Equal(t, http.StatusAccepted, rec.Code, rec.Body.String())
	var started campaignStartResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &started))
	require.Equal(t, jobs.StatusDone, waitE2EJob(t, env.runner, started.JobID).Status)

	rec = doJSON(t, env.handler, cookie, "", http.MethodGet, "/api/v1/campaigns/"+campaign.ID, "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var finished campaignDetailDTO
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &finished))
	require.Equal(t, domain.CampaignDone, finished.Status)
	require.Equal(t, 2, finished.Stats.Sent)

	messages := env.sender.all()
	require.Len(t, messages, 2)
	for _, message := range messages {
		require.Contains(t, message.text, "To unsubscribe")
		require.Equal(t, "<mailto:unsub@example.com?subject=unsubscribe>",
			message.headers["List-Unsubscribe"])
	}

	// Plan and execute a reviewed agent run.
	rec = doJSON(t, env.handler, cookie, csrf, http.MethodPost, "/api/v1/agent/plan",
		`{"prompt":"Archive old newsletters"}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var plan agentPlanResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &plan))
	require.Equal(t, domain.AgentPlanned, plan.Status)
	require.NotEmpty(t, plan.ConfirmToken)
	require.Equal(t, 2, plan.Plan.Total)
	require.Equal(t, 2, plan.Plan.Steps[0].Count)

	rec = doJSON(t, env.handler, cookie, csrf, http.MethodPost,
		"/api/v1/agent/runs/"+plan.ID+"/execute",
		fmt.Sprintf(`{"confirm_token":%q}`, plan.ConfirmToken))
	require.Equal(t, http.StatusAccepted, rec.Code, rec.Body.String())
	var agentExec agentExecuteResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &agentExec))
	require.Equal(t, jobs.StatusDone, waitE2EJob(t, env.runner, agentExec.JobID).Status)

	rec = doJSON(t, env.handler, cookie, "", http.MethodGet, "/api/v1/agent/runs/"+plan.ID, "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var run domain.AgentRun
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &run))
	require.Equal(t, domain.AgentDone, run.Status)
	require.Equal(t, 2, run.Result.Affected)

	batches = env.mailbox.allBatches()
	require.Len(t, batches, 2)
	require.Equal(t, []string{"old-1", "old-2"}, batches[1].ids)
	require.Equal(t, []string{"INBOX"}, batches[1].remove)
}
