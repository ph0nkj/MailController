package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/ph0nkj/mailcontroller/internal/agent"
	"github.com/ph0nkj/mailcontroller/internal/domain"
	"github.com/ph0nkj/mailcontroller/internal/gmail"
	"github.com/ph0nkj/mailcontroller/internal/llm"
	"github.com/ph0nkj/mailcontroller/internal/store"
)

type fakeAgent struct {
	planRun      domain.AgentRun
	planErr      error
	prompts      []string
	executeRun   domain.AgentRun
	executeJobID string
	executeErr   error
	executeCalls int
	getRun       domain.AgentRun
	getErr       error
	listRuns     []domain.AgentRun
	listErr      error
	listLimit    int
}

func (f *fakeAgent) Plan(_ context.Context, prompt string) (domain.AgentRun, error) {
	f.prompts = append(f.prompts, prompt)
	if f.planErr != nil {
		return domain.AgentRun{}, f.planErr
	}
	run := f.planRun
	if run.ID == "" {
		run.ID = "run-1"
	}
	run.Prompt = prompt
	run.Status = domain.AgentPlanned
	if run.Plan.Total == 0 && len(run.Plan.Steps) == 0 {
		run.Plan = domain.AgentPlan{
			Summary: "Archive old mail",
			Steps: []domain.AgentStep{{
				Action: domain.AgentActionArchive,
				Query:  "in:inbox older_than:1y",
				Count:  4,
			}},
			Total: 4,
		}
	}
	return run, nil
}

func (f *fakeAgent) Execute(_ context.Context, id string) (domain.AgentRun, string, error) {
	f.executeCalls++
	if f.executeErr != nil {
		return domain.AgentRun{}, "", f.executeErr
	}
	run := f.executeRun
	if run.ID == "" {
		run = f.planRun
		run.ID = id
	}
	run.Status = domain.AgentRunning
	jobID := f.executeJobID
	if jobID == "" {
		jobID = "job-1"
	}
	return run, jobID, nil
}

func (f *fakeAgent) Get(_ context.Context, id string) (domain.AgentRun, error) {
	if f.getErr != nil {
		return domain.AgentRun{}, f.getErr
	}
	run := f.getRun
	if run.ID == "" {
		run = f.planRun
		if run.ID == "" {
			run.ID = id
		}
		run.Status = domain.AgentPlanned
	}
	return run, nil
}

func (f *fakeAgent) List(_ context.Context, limit int) ([]domain.AgentRun, error) {
	f.listLimit = limit
	if f.listErr != nil {
		return nil, f.listErr
	}
	return f.listRuns, nil
}

func newAgentTestAPI(t *testing.T, service *fakeAgent) (*API, http.Handler, *http.Cookie, string) {
	t.Helper()
	api, handler := newTestAPIWithDeps(t, Deps{Agent: service})
	cookie, csrf := loginSession(t, handler)
	return api, handler, cookie, csrf
}

func planAgent(t *testing.T, handler http.Handler, cookie *http.Cookie, csrf, prompt string) agentPlanResponse {
	t.Helper()
	rec := doJSON(t, handler, cookie, csrf, http.MethodPost, "/api/v1/agent/plan",
		fmt.Sprintf(`{"prompt":%q}`, prompt))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var body agentPlanResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body), rec.Body.String())
	require.NotEmpty(t, body.ID)
	require.NotEmpty(t, body.ConfirmToken)
	require.True(t, body.ExpiresAt.After(time.Now()))
	return body
}

func TestAgentPlanRequiresAuthCSRFAndReturnsToken(t *testing.T) {
	service := &fakeAgent{}
	_, handler, cookie, csrf := newAgentTestAPI(t, service)

	rec := doJSON(t, handler, nil, "", http.MethodPost, "/api/v1/agent/plan", `{"prompt":"archive old mail"}`)
	require.Equal(t, http.StatusUnauthorized, rec.Code)

	rec = doJSON(t, handler, cookie, "", http.MethodPost, "/api/v1/agent/plan", `{"prompt":"archive old mail"}`)
	require.Equal(t, http.StatusForbidden, rec.Code)
	require.Empty(t, service.prompts)

	plan := planAgent(t, handler, cookie, csrf, "archive old mail")
	require.Equal(t, []string{"archive old mail"}, service.prompts)
	require.Equal(t, "run-1", plan.ID)
	require.Equal(t, domain.AgentPlanned, plan.Status)
	require.Equal(t, 4, plan.Plan.Total)
	require.Equal(t, "Archive old mail", plan.Plan.Summary)

	rec = doJSON(t, handler, cookie, csrf, http.MethodPost, "/api/v1/agent/plan", `not-json`)
	require.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestAgentExecuteConsumesTokenOnce(t *testing.T) {
	service := &fakeAgent{}
	_, handler, cookie, csrf := newAgentTestAPI(t, service)
	plan := planAgent(t, handler, cookie, csrf, "archive old mail")

	rec := doJSON(t, handler, cookie, "", http.MethodPost,
		"/api/v1/agent/runs/"+plan.ID+"/execute", fmt.Sprintf(`{"confirm_token":%q}`, plan.ConfirmToken))
	require.Equal(t, http.StatusForbidden, rec.Code)

	rec = doJSON(t, handler, cookie, csrf, http.MethodPost,
		"/api/v1/agent/runs/"+plan.ID+"/execute", fmt.Sprintf(`{"confirm_token":%q}`, plan.ConfirmToken))
	require.Equal(t, http.StatusAccepted, rec.Code, rec.Body.String())
	var executed agentExecuteResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &executed))
	require.Equal(t, plan.ID, executed.RunID)
	require.Equal(t, "job-1", executed.JobID)
	require.Equal(t, 1, service.executeCalls)

	// The token is single-use.
	rec = doJSON(t, handler, cookie, csrf, http.MethodPost,
		"/api/v1/agent/runs/"+plan.ID+"/execute", fmt.Sprintf(`{"confirm_token":%q}`, plan.ConfirmToken))
	require.Equal(t, http.StatusConflict, rec.Code)
	require.Contains(t, rec.Body.String(), "confirm_expired")
	require.Equal(t, 1, service.executeCalls)
}

func TestAgentExecuteRequiresPlannedRunAndMatchingToken(t *testing.T) {
	service := &fakeAgent{}
	api, handler, cookie, csrf := newAgentTestAPI(t, service)
	first := planAgent(t, handler, cookie, csrf, "first")
	service.planRun.ID = "run-2"
	second := planAgent(t, handler, cookie, csrf, "second")
	require.NotEqual(t, first.ID, second.ID)

	// A token issued for another run is rejected.
	rec := doJSON(t, handler, cookie, csrf, http.MethodPost,
		"/api/v1/agent/runs/"+second.ID+"/execute", fmt.Sprintf(`{"confirm_token":%q}`, first.ConfirmToken))
	require.Equal(t, http.StatusConflict, rec.Code)
	require.Contains(t, rec.Body.String(), "confirm_expired")
	require.Zero(t, service.executeCalls)

	service.getRun = domain.AgentRun{ID: second.ID, Status: domain.AgentDone}
	token, _, err := api.confirms.issue(agentConfirmMode(second.ID), 4)
	require.NoError(t, err)
	rec = doJSON(t, handler, cookie, csrf, http.MethodPost,
		"/api/v1/agent/runs/"+second.ID+"/execute", fmt.Sprintf(`{"confirm_token":%q}`, token))
	require.Equal(t, http.StatusConflict, rec.Code)
	require.Contains(t, rec.Body.String(), "run_not_ready")
	require.Zero(t, service.executeCalls)

	rec = doJSON(t, handler, cookie, csrf, http.MethodPost, "/api/v1/agent/runs/%20/execute", `{}`)
	require.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestAgentExecuteErrorMapping(t *testing.T) {
	tests := []struct {
		name   string
		err    error
		status int
		code   string
	}{
		{"not found", store.ErrNotFound, http.StatusNotFound, "run_not_found"},
		{"invalid state", agent.ErrInvalidState, http.StatusConflict, "run_not_ready"},
		{"busy", agent.ErrBusy, http.StatusConflict, "agent_busy"},
		{"llm missing", fmt.Errorf("%w: %w", agent.ErrPlanning, llm.ErrNotConfigured), http.StatusConflict, "llm_not_configured"},
		{"gmail missing", gmail.ErrNotConnected, http.StatusConflict, "google_not_connected"},
		{"provider", fmt.Errorf("%w: %w", agent.ErrPlanning, llm.ErrProvider), http.StatusBadGateway, "agent_planning_failed"},
		{"unknown", errors.New("boom"), http.StatusInternalServerError, "agent_error"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			service := &fakeAgent{}
			_, handler, cookie, csrf := newAgentTestAPI(t, service)
			plan := planAgent(t, handler, cookie, csrf, "archive old mail")
			service.executeErr = tc.err
			rec := doJSON(t, handler, cookie, csrf, http.MethodPost,
				"/api/v1/agent/runs/"+plan.ID+"/execute", fmt.Sprintf(`{"confirm_token":%q}`, plan.ConfirmToken))
			require.Equal(t, tc.status, rec.Code, rec.Body.String())
			require.Contains(t, rec.Body.String(), tc.code)
		})
	}
}

func TestAgentPlanErrorMapping(t *testing.T) {
	tests := []struct {
		name   string
		err    error
		status int
		code   string
	}{
		{"invalid prompt", fmt.Errorf("%w: describe the mailbox action you want", agent.ErrInvalidPrompt), http.StatusBadRequest, "invalid_request"},
		{"invalid plan", fmt.Errorf("%w: unsupported action", agent.ErrInvalidPlan), http.StatusBadRequest, "invalid_request"},
		{"too large", fmt.Errorf("%w: plan would touch 20 messages (limit 10)", agent.ErrPlanTooLarge), http.StatusUnprocessableEntity, "plan_too_large"},
		{"no matches", agent.ErrNoMatches, http.StatusUnprocessableEntity, "no_matches"},
		{"not configured", fmt.Errorf("%w: %w", agent.ErrPlanning, llm.ErrNotConfigured), http.StatusConflict, "llm_not_configured"},
		{"google", gmail.ErrNotConfigured, http.StatusConflict, "google_not_configured"},
		{"planner", fmt.Errorf("%w: %w", agent.ErrPlanning, agent.ErrPlannerResponse), http.StatusBadGateway, "agent_planning_failed"},
		{"unknown", errors.New("boom"), http.StatusInternalServerError, "agent_error"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			service := &fakeAgent{planErr: tc.err}
			_, handler, cookie, csrf := newAgentTestAPI(t, service)
			rec := doJSON(t, handler, cookie, csrf, http.MethodPost, "/api/v1/agent/plan", `{"prompt":"do it"}`)
			require.Equal(t, tc.status, rec.Code, rec.Body.String())
			require.Contains(t, rec.Body.String(), tc.code)
			if tc.name == "too large" {
				require.Contains(t, rec.Body.String(), "20 messages")
			}
		})
	}
}

func TestAgentRunListAndGet(t *testing.T) {
	service := &fakeAgent{listRuns: []domain.AgentRun{
		{ID: "run-1", Prompt: "one", Status: domain.AgentPlanned},
		{ID: "run-2", Prompt: "two", Status: domain.AgentDone},
	}}
	_, handler, cookie, _ := newAgentTestAPI(t, service)

	rec := doJSON(t, handler, cookie, "", http.MethodGet, "/api/v1/agent/runs", "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var list agentRunListResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &list))
	require.Len(t, list.Items, 2)
	require.Equal(t, 2, list.Total)
	require.Equal(t, agentDefaultList, service.listLimit)

	rec = doJSON(t, handler, cookie, "", http.MethodGet, "/api/v1/agent/runs?limit=1000", "")
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, agentMaxList, service.listLimit)

	rec = doJSON(t, handler, cookie, "", http.MethodGet, "/api/v1/agent/runs?limit=zero", "")
	require.Equal(t, http.StatusBadRequest, rec.Code)

	rec = doJSON(t, handler, cookie, "", http.MethodGet, "/api/v1/agent/runs/run-1", "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var run domain.AgentRun
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &run))
	require.Equal(t, "run-1", run.ID)

	service.getErr = store.ErrNotFound
	rec = doJSON(t, handler, cookie, "", http.MethodGet, "/api/v1/agent/runs/missing", "")
	require.Equal(t, http.StatusNotFound, rec.Code)
}

func TestAgentRoutesAbsentWithoutService(t *testing.T) {
	_, handler := newTestAPI(t)
	cookie, _ := loginSession(t, handler)
	rec := doJSON(t, handler, cookie, "", http.MethodGet, "/api/v1/agent/runs", "")
	require.Equal(t, http.StatusNotFound, rec.Code)
}
