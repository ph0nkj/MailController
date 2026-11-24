package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/ph0nkj/mailcontroller/internal/auth"
	"github.com/ph0nkj/mailcontroller/internal/config"
	"github.com/ph0nkj/mailcontroller/internal/domain"
	"github.com/ph0nkj/mailcontroller/internal/gmail"
	"github.com/ph0nkj/mailcontroller/internal/mailer"
	"github.com/ph0nkj/mailcontroller/internal/store"
)

type fakeCampaignRunner struct {
	repo      *store.CampaignRepo
	started   []string
	startID   string
	startErr  error
	paused    []string
	pauseErr  error
	pauseCall int
}

func (f *fakeCampaignRunner) Start(ctx context.Context, campaignID string) (string, error) {
	f.started = append(f.started, campaignID)
	if f.startErr != nil {
		return "", f.startErr
	}
	if f.repo != nil {
		if err := f.repo.SetStatus(ctx, campaignID, domain.CampaignRunning, time.Now()); err != nil {
			return "", err
		}
	}
	return f.startID, nil
}

func (f *fakeCampaignRunner) Pause(ctx context.Context, campaignID string) error {
	f.pauseCall++
	f.paused = append(f.paused, campaignID)
	if f.pauseErr != nil {
		return f.pauseErr
	}
	if f.repo != nil {
		return f.repo.SetStatus(ctx, campaignID, domain.CampaignPaused, time.Now())
	}
	return nil
}

type campaignEnv struct {
	repo    *store.CampaignRepo
	runner  *fakeCampaignRunner
	audit   *fakeAudit
	handler http.Handler
}

func newCampaignEnv(t *testing.T) *campaignEnv {
	t.Helper()
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "campaigns.db"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, st.Close()) })

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	authService, err := auth.New(context.Background(), st.Settings(), "test-password", false, logger)
	require.NoError(t, err)

	env := &campaignEnv{
		repo:   st.Campaigns(),
		runner: &fakeCampaignRunner{repo: st.Campaigns(), startID: "job-1"},
		audit:  &fakeAudit{},
	}
	api := New(logger, config.Config{}, Deps{
		Auth:           authService,
		Campaigns:      st.Campaigns(),
		CampaignRunner: env.runner,
		Audit:          env.audit,
	})
	env.handler = api.Router()
	return env
}

func (e *campaignEnv) createViaAPI(t *testing.T, cookie *http.Cookie, csrf, body string) campaignDetailDTO {
	t.Helper()
	rec := doJSON(t, e.handler, cookie, csrf, http.MethodPost, "/api/v1/campaigns", body)
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	var campaign campaignDetailDTO
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &campaign))
	return campaign
}

func (e *campaignEnv) seedCampaign(t *testing.T, recipients ...string) domain.Campaign {
	t.Helper()
	campaign := domain.Campaign{
		Name:       "Seeded",
		Subject:    "Hi {{email}}",
		BodyText:   "Body",
		DailyLimit: 100,
		RatePerMin: 60,
	}
	require.NoError(t, e.repo.Create(context.Background(), &campaign))
	_, err := e.repo.AddRecipients(context.Background(), campaign.ID, recipients)
	require.NoError(t, err)
	return campaign
}

func doRaw(t *testing.T, handler http.Handler, cookie *http.Cookie, csrf, method, target, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	req.Header.Set("Content-Type", "text/plain; charset=utf-8")
	req.AddCookie(cookie)
	if csrf != "" {
		req.Header.Set("X-CSRF-Token", csrf)
	}
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec
}

func TestCampaignCRUD(t *testing.T) {
	env := newCampaignEnv(t)
	cookie, csrf := loginSession(t, env.handler)

	body := `{"name":"January","subject":"Hello {{email}}","body_text":"Text","body_html":"<p>HTML</p>","daily_limit":25,"rate_per_min":30}`
	campaign := env.createViaAPI(t, cookie, csrf, body)
	require.NotEmpty(t, campaign.ID)
	require.Equal(t, "January", campaign.Name)
	require.Equal(t, domain.CampaignDraft, campaign.Status)
	require.Equal(t, 25, campaign.DailyLimit)
	require.Equal(t, 30, campaign.RatePerMin)
	require.Equal(t, "Text", campaign.BodyText)
	require.Equal(t, "<p>HTML</p>", campaign.BodyHTML)
	require.Zero(t, campaign.Stats.Total)
	require.Nil(t, campaign.StartedAt)

	// Defaults are applied.
	defaults := env.createViaAPI(t, cookie, csrf,
		`{"name":"Defaults","subject":"Subject","body_text":"Text"}`)
	require.Equal(t, campaignDefaultDaily, defaults.DailyLimit)
	require.Equal(t, campaignDefaultRate, defaults.RatePerMin)

	rec := serveAuthed(env.handler, http.MethodGet, "/api/v1/campaigns", cookie)
	require.Equal(t, http.StatusOK, rec.Code)
	var list campaignListDTO
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &list))
	require.Equal(t, 2, list.Total)
	require.Len(t, list.Items, 2)

	rec = serveAuthed(env.handler, http.MethodGet, "/api/v1/campaigns/"+campaign.ID, cookie)
	require.Equal(t, http.StatusOK, rec.Code)
	var detail campaignDetailDTO
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &detail))
	require.Equal(t, "January", detail.Name)
	require.Equal(t, "Text", detail.BodyText)

	rec = doJSON(t, env.handler, cookie, csrf, http.MethodPut, "/api/v1/campaigns/"+campaign.ID,
		`{"name":"Renamed","subject":"New subject","body_text":"New text","daily_limit":10,"rate_per_min":12}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var updated campaignDetailDTO
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &updated))
	require.Equal(t, "Renamed", updated.Name)
	require.Equal(t, "New subject", updated.Subject)
	require.Equal(t, "New text", updated.BodyText)
	require.Empty(t, updated.BodyHTML)

	require.NoError(t, env.repo.SetStatus(context.Background(), campaign.ID, domain.CampaignPaused, time.Now()))
	rec = doJSON(t, env.handler, cookie, csrf, http.MethodPut, "/api/v1/campaigns/"+campaign.ID,
		`{"name":"No","subject":"No","body_text":"No"}`)
	require.Equal(t, http.StatusConflict, rec.Code)
	require.Contains(t, rec.Body.String(), "campaign_not_draft")

	rec = doJSON(t, env.handler, cookie, csrf, http.MethodDelete, "/api/v1/campaigns/"+defaults.ID, "")
	require.Equal(t, http.StatusNoContent, rec.Code)
	rec = serveAuthed(env.handler, http.MethodGet, "/api/v1/campaigns/"+defaults.ID, cookie)
	require.Equal(t, http.StatusNotFound, rec.Code)

	rec = doJSON(t, env.handler, cookie, csrf, http.MethodDelete, "/api/v1/campaigns/"+campaign.ID, "")
	require.Equal(t, http.StatusConflict, rec.Code, "paused campaigns cannot be deleted")

	actions := map[string]int{}
	for _, entry := range env.audit.list() {
		actions[entry.Action]++
		require.NotContains(t, entry.Details, "@")
	}
	require.Equal(t, 2, actions["campaign.create"])
	require.Equal(t, 1, actions["campaign.update"])
	require.Equal(t, 1, actions["campaign.delete"])
}

func TestCampaignCreateValidation(t *testing.T) {
	env := newCampaignEnv(t)
	cookie, csrf := loginSession(t, env.handler)

	tests := []string{
		`not json`,
		`{"subject":"x","body_text":"x"}`,
		`{"name":"n","body_text":"x"}`,
		`{"name":"n","subject":"x"}`,
		`{"name":"n","subject":"a\nb","body_text":"x"}`,
		`{"name":"n","subject":"x","body_text":"x","daily_limit":-1}`,
		`{"name":"n","subject":"x","body_text":"x","daily_limit":10001}`,
		`{"name":"n","subject":"x","body_text":"x","rate_per_min":-1}`,
		fmt.Sprintf(`{"name":"n","subject":"x","body_text":%q}`, strings.Repeat("a", campaignMaxBodyBytes+1)),
	}

	for _, body := range tests {
		rec := doJSON(t, env.handler, cookie, csrf, http.MethodPost, "/api/v1/campaigns", body)
		require.Equal(t, http.StatusBadRequest, rec.Code, body)
	}

	// Missing CSRF.
	rec := doJSON(t, env.handler, cookie, "", http.MethodPost, "/api/v1/campaigns",
		`{"name":"n","subject":"x","body_text":"x"}`)
	require.Equal(t, http.StatusForbidden, rec.Code)

	// Unauthenticated.
	rec = doJSON(t, env.handler, nil, "", http.MethodPost, "/api/v1/campaigns", `{}`)
	require.Equal(t, http.StatusUnauthorized, rec.Code)
}

func TestCampaignRecipientsImportAndList(t *testing.T) {
	env := newCampaignEnv(t)
	cookie, csrf := loginSession(t, env.handler)
	campaign := env.createViaAPI(t, cookie, csrf,
		`{"name":"Import","subject":"Hi","body_text":"Body"}`)

	rec := doRaw(t, env.handler, cookie, csrf, http.MethodPost,
		"/api/v1/campaigns/"+campaign.ID+"/recipients",
		"a@example.com\nA@example.com\nbad\nb@example.com\nName <c@example.com>\n")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var imported campaignImportResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &imported))
	require.Equal(t, 3, imported.Imported)
	require.Equal(t, 1, imported.Duplicates)
	require.Equal(t, 1, imported.Invalid)
	require.Equal(t, []string{"bad"}, imported.InvalidSamples)
	require.Equal(t, 3, imported.Total)

	rec = doRaw(t, env.handler, cookie, csrf, http.MethodPost,
		"/api/v1/campaigns/"+campaign.ID+"/recipients",
		"a@example.com\nb@example.com\nd@example.com\n")
	require.Equal(t, http.StatusOK, rec.Code)
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &imported))
	require.Equal(t, 1, imported.Imported)
	require.Equal(t, 2, imported.Duplicates)
	require.Equal(t, 4, imported.Total)

	rec = doRaw(t, env.handler, cookie, csrf, http.MethodPost,
		"/api/v1/campaigns/"+campaign.ID+"/recipients",
		"Email,Name\ne@example.com,Eve\n")
	require.Equal(t, http.StatusOK, rec.Code)
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &imported))
	require.Equal(t, 1, imported.Imported)
	require.Equal(t, 5, imported.Total)

	rec = doRaw(t, env.handler, cookie, csrf, http.MethodPost,
		"/api/v1/campaigns/"+campaign.ID+"/recipients", "not-an-email\n")
	require.Equal(t, http.StatusBadRequest, rec.Code)
	require.Contains(t, rec.Body.String(), "invalid_recipients")

	rec = serveAuthed(env.handler, http.MethodGet,
		"/api/v1/campaigns/"+campaign.ID+"/recipients?page=1&page_size=2", cookie)
	require.Equal(t, http.StatusOK, rec.Code)
	var page recipientListDTO
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &page))
	require.Equal(t, 5, page.Total)
	require.Equal(t, 1, page.Page)
	require.Equal(t, 2, page.PageSize)
	require.Len(t, page.Items, 2)
	require.Equal(t, "a@example.com", page.Items[0].Email)

	rec = serveAuthed(env.handler, http.MethodGet,
		"/api/v1/campaigns/"+campaign.ID+"/recipients?page=3&page_size=2", cookie)
	require.Equal(t, http.StatusOK, rec.Code)
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &page))
	require.Len(t, page.Items, 1)

	rec = serveAuthed(env.handler, http.MethodGet,
		"/api/v1/campaigns/"+campaign.ID+"/recipients?status=failed", cookie)
	require.Equal(t, http.StatusOK, rec.Code)
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &page))
	require.Zero(t, page.Total)

	for _, query := range []string{"?status=bogus", "?page=0", "?page_size=0", "?page=abc"} {
		rec = serveAuthed(env.handler, http.MethodGet,
			"/api/v1/campaigns/"+campaign.ID+"/recipients"+query, cookie)
		require.Equal(t, http.StatusBadRequest, rec.Code, query)
	}

	// Import is only possible for drafts.
	require.NoError(t, env.repo.SetStatus(context.Background(), campaign.ID, domain.CampaignPaused, time.Now()))
	rec = doRaw(t, env.handler, cookie, csrf, http.MethodPost,
		"/api/v1/campaigns/"+campaign.ID+"/recipients", "f@example.com\n")
	require.Equal(t, http.StatusConflict, rec.Code)

	var importAudits int
	for _, entry := range env.audit.list() {
		if entry.Action == "campaign.import" {
			importAudits++
			require.NotContains(t, entry.Details, "@")
		}
	}
	require.Equal(t, 3, importAudits)
}

func TestCampaignStartAndPause(t *testing.T) {
	env := newCampaignEnv(t)
	cookie, csrf := loginSession(t, env.handler)
	campaign := env.seedCampaign(t, "a@example.com", "b@example.com")

	rec := doJSON(t, env.handler, cookie, csrf, http.MethodPost,
		"/api/v1/campaigns/"+campaign.ID+"/start", `{"confirm":false}`)
	require.Equal(t, http.StatusBadRequest, rec.Code)
	require.Contains(t, rec.Body.String(), "confirmation_required")
	require.Empty(t, env.runner.started)

	rec = doJSON(t, env.handler, cookie, csrf, http.MethodPost,
		"/api/v1/campaigns/"+campaign.ID+"/start", `{"confirm":true}`)
	require.Equal(t, http.StatusAccepted, rec.Code, rec.Body.String())
	var started campaignStartResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &started))
	require.Equal(t, "job-1", started.JobID)
	require.Equal(t, domain.CampaignRunning, started.Status)
	require.Equal(t, []string{campaign.ID}, env.runner.started)

	rec = serveAuthed(env.handler, http.MethodGet, "/api/v1/campaigns/"+campaign.ID, cookie)
	require.Equal(t, http.StatusOK, rec.Code)
	var detail campaignDetailDTO
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &detail))
	require.Equal(t, domain.CampaignRunning, detail.Status)
	require.Equal(t, 2, detail.Stats.Pending)

	rec = doJSON(t, env.handler, cookie, csrf, http.MethodPost,
		"/api/v1/campaigns/"+campaign.ID+"/pause", `{}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var paused campaignStatusResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &paused))
	require.Equal(t, domain.CampaignPaused, paused.Status)
	require.Equal(t, []string{campaign.ID}, env.runner.paused)

	// Pausing again is idempotent.
	rec = doJSON(t, env.handler, cookie, csrf, http.MethodPost,
		"/api/v1/campaigns/"+campaign.ID+"/pause", `{}`)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, 1, env.runner.pauseCall)

	// Empty campaigns cannot start.
	empty := env.seedCampaign(t)
	rec = doJSON(t, env.handler, cookie, csrf, http.MethodPost,
		"/api/v1/campaigns/"+empty.ID+"/start", `{"confirm":true}`)
	require.Equal(t, http.StatusConflict, rec.Code)
	require.Contains(t, rec.Body.String(), "campaign_empty")

	// Finished campaigns cannot start.
	done := env.seedCampaign(t, "c@example.com")
	require.NoError(t, env.repo.SetStatus(context.Background(), done.ID, domain.CampaignDone, time.Now()))
	rec = doJSON(t, env.handler, cookie, csrf, http.MethodPost,
		"/api/v1/campaigns/"+done.ID+"/start", `{"confirm":true}`)
	require.Equal(t, http.StatusConflict, rec.Code)
	require.Contains(t, rec.Body.String(), "invalid_state")

	// Draft campaigns cannot pause.
	draft := env.seedCampaign(t, "d@example.com")
	rec = doJSON(t, env.handler, cookie, csrf, http.MethodPost,
		"/api/v1/campaigns/"+draft.ID+"/pause", `{}`)
	require.Equal(t, http.StatusConflict, rec.Code)

	var startAudits int
	for _, entry := range env.audit.list() {
		if entry.Action == "campaign.start" {
			startAudits++
			require.Contains(t, entry.Details, `"pending":2`)
			require.NotContains(t, entry.Details, "@")
		}
	}
	require.Equal(t, 1, startAudits)
}

func TestCampaignStartErrorMapping(t *testing.T) {
	tests := []struct {
		name   string
		err    error
		status int
		code   string
	}{
		{name: "busy", err: mailer.ErrAlreadyRunning, status: http.StatusConflict, code: "campaign_busy"},
		{name: "empty", err: mailer.ErrNoRecipients, status: http.StatusConflict, code: "campaign_empty"},
		{name: "state", err: mailer.ErrInvalidCampaignState, status: http.StatusConflict, code: "invalid_state"},
		{name: "not connected", err: gmail.ErrNotConnected, status: http.StatusConflict, code: "google_not_connected"},
		{name: "not configured", err: gmail.ErrNotConfigured, status: http.StatusConflict, code: "google_not_configured"},
		{name: "missing", err: store.ErrNotFound, status: http.StatusNotFound, code: "campaign_not_found"},
		{name: "unknown", err: errors.New("boom"), status: http.StatusBadGateway, code: "campaign_error"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env := newCampaignEnv(t)
			cookie, csrf := loginSession(t, env.handler)
			campaign := env.seedCampaign(t, "a@example.com")
			env.runner.startErr = tt.err

			rec := doJSON(t, env.handler, cookie, csrf, http.MethodPost,
				"/api/v1/campaigns/"+campaign.ID+"/start", `{"confirm":true}`)
			require.Equal(t, tt.status, rec.Code, rec.Body.String())
			require.Contains(t, rec.Body.String(), tt.code)
			require.Empty(t, env.audit.list())
		})
	}
}

func TestCampaignPauseError(t *testing.T) {
	env := newCampaignEnv(t)
	cookie, csrf := loginSession(t, env.handler)
	campaign := env.seedCampaign(t, "a@example.com")
	require.NoError(t, env.repo.SetStatus(context.Background(), campaign.ID, domain.CampaignRunning, time.Now()))
	env.runner.pauseErr = errors.New("boom")

	rec := doJSON(t, env.handler, cookie, csrf, http.MethodPost,
		"/api/v1/campaigns/"+campaign.ID+"/pause", `{}`)
	require.Equal(t, http.StatusBadGateway, rec.Code)
	require.Contains(t, rec.Body.String(), "campaign_error")
}

func TestCampaignRoutesRequireAuth(t *testing.T) {
	env := newCampaignEnv(t)
	for _, target := range []string{"/api/v1/campaigns", "/api/v1/campaigns/x"} {
		req := httptest.NewRequest(http.MethodGet, target, nil)
		rec := httptest.NewRecorder()
		env.handler.ServeHTTP(rec, req)
		require.Equal(t, http.StatusUnauthorized, rec.Code, target)
	}
}

func TestCampaignRoutesAbsentWithoutDeps(t *testing.T) {
	_, handler := newTestAPI(t)
	cookie, _ := loginSession(t, handler)
	for _, target := range []string{"/api/v1/campaigns", "/api/v1/campaigns/x/recipients"} {
		rec := serveAuthed(handler, http.MethodGet, target, cookie)
		require.Equal(t, http.StatusNotFound, rec.Code, target)
	}
}
