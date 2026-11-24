package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/ph0nkj/mailcontroller/internal/agent"
	"github.com/ph0nkj/mailcontroller/internal/domain"
	"github.com/ph0nkj/mailcontroller/internal/gmail"
	"github.com/ph0nkj/mailcontroller/internal/llm"
	"github.com/ph0nkj/mailcontroller/internal/store"
)

const (
	// agentBodyLimit caps planner and execution payloads.
	agentBodyLimit = 8 << 10
	// agentDefaultList caps the recent-run list by default.
	agentDefaultList = 20
	// agentMaxList caps the recent-run list requested by a client.
	agentMaxList = 50
	// agentMaxIDChars bounds a run id from the URL.
	agentMaxIDChars = 128
)

// AgentService plans and executes reviewable mailbox actions.
type AgentService interface {
	Plan(ctx context.Context, prompt string) (domain.AgentRun, error)
	Execute(ctx context.Context, id string) (domain.AgentRun, string, error)
	Get(ctx context.Context, id string) (domain.AgentRun, error)
	List(ctx context.Context, limit int) ([]domain.AgentRun, error)
}

type agentPlanRequest struct {
	Prompt string `json:"prompt"`
}

type agentPlanResponse struct {
	domain.AgentRun
	ConfirmToken string    `json:"confirm_token"`
	ExpiresAt    time.Time `json:"expires_at"`
}

type agentExecuteRequest struct {
	ConfirmToken string `json:"confirm_token"`
}

type agentExecuteResponse struct {
	RunID string `json:"run_id"`
	JobID string `json:"job_id"`
}

type agentRunListResponse struct {
	Items []domain.AgentRun `json:"items"`
	Total int               `json:"total"`
}

func (a *API) handleAgentPlan(w http.ResponseWriter, r *http.Request) {
	var req agentPlanRequest
	if !decodeAgentJSON(w, r, &req) {
		return
	}
	run, err := a.agent.Plan(r.Context(), req.Prompt)
	if err != nil {
		a.writeAgentError(w, "plan mailbox action", err)
		return
	}
	token, expires, err := a.confirms.issue(agentConfirmMode(run.ID), int64(run.Plan.Total))
	if err != nil {
		a.log.Error("issue agent confirm token", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "could not prepare confirmation")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, agentPlanResponse{
		AgentRun:     run,
		ConfirmToken: token,
		ExpiresAt:    expires,
	})
}

func (a *API) handleAgentExecute(w http.ResponseWriter, r *http.Request) {
	var req agentExecuteRequest
	if !decodeAgentJSON(w, r, &req) {
		return
	}
	id, ok := agentRunID(w, r)
	if !ok {
		return
	}
	run, err := a.agent.Get(r.Context(), id)
	if err != nil {
		a.writeAgentError(w, "load agent run", err)
		return
	}
	if run.Status != domain.AgentPlanned {
		writeError(w, http.StatusConflict, "run_not_ready", "this plan is not ready to execute")
		return
	}
	if _, valid := a.confirms.consume(req.ConfirmToken, agentConfirmMode(id)); !valid {
		writeError(w, http.StatusConflict, "confirm_expired", "confirmation expired or invalid; plan again")
		return
	}
	started, jobID, err := a.agent.Execute(r.Context(), id)
	if err != nil {
		a.writeAgentError(w, "execute agent run", err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusAccepted, agentExecuteResponse{RunID: started.ID, JobID: jobID})
}

func (a *API) handleAgentList(w http.ResponseWriter, r *http.Request) {
	limit := agentDefaultList
	if raw := r.URL.Query().Get("limit"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 {
			writeError(w, http.StatusBadRequest, "invalid_request", "limit must be a positive integer")
			return
		}
		limit = min(parsed, agentMaxList)
	}
	runs, err := a.agent.List(r.Context(), limit)
	if err != nil {
		a.writeAgentError(w, "list agent runs", err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, agentRunListResponse{Items: runs, Total: len(runs)})
}

func (a *API) handleAgentGet(w http.ResponseWriter, r *http.Request) {
	id, ok := agentRunID(w, r)
	if !ok {
		return
	}
	run, err := a.agent.Get(r.Context(), id)
	if err != nil {
		a.writeAgentError(w, "load agent run", err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, run)
}

func decodeAgentJSON(w http.ResponseWriter, r *http.Request, dst any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, agentBodyLimit)
	if err := json.NewDecoder(r.Body).Decode(dst); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid JSON body")
		return false
	}
	return true
}

func agentRunID(w http.ResponseWriter, r *http.Request) (string, bool) {
	id := strings.TrimSpace(chi.URLParam(r, "id"))
	if id == "" || len(id) > agentMaxIDChars || containsControlChars(id) {
		writeError(w, http.StatusBadRequest, "invalid_request", "run id is required")
		return "", false
	}
	return id, true
}

func containsControlChars(value string) bool {
	for _, r := range value {
		if r < 0x20 || r == 0x7f {
			return true
		}
	}
	return false
}

func agentConfirmMode(runID string) string {
	return "agent:" + runID
}

func (a *API) writeAgentError(w http.ResponseWriter, operation string, err error) {
	switch {
	case errors.Is(err, store.ErrNotFound):
		writeError(w, http.StatusNotFound, "run_not_found", "agent run not found")
	case errors.Is(err, agent.ErrInvalidPrompt), errors.Is(err, agent.ErrInvalidPlan):
		writeError(w, http.StatusBadRequest, "invalid_request", agentMessage(err))
	case errors.Is(err, agent.ErrPlanTooLarge):
		writeError(w, http.StatusUnprocessableEntity, "plan_too_large", agentMessage(err))
	case errors.Is(err, agent.ErrNoMatches):
		writeError(w, http.StatusUnprocessableEntity, "no_matches", "no messages match this plan; adjust the request and try again")
	case errors.Is(err, llm.ErrNotConfigured):
		writeError(w, http.StatusConflict, "llm_not_configured", "configure an AI provider first")
	case errors.Is(err, gmail.ErrNotConfigured):
		writeError(w, http.StatusConflict, "google_not_configured", "save Google OAuth client credentials first")
	case errors.Is(err, gmail.ErrNotConnected):
		writeError(w, http.StatusConflict, "google_not_connected", "connect a Google account first")
	case errors.Is(err, agent.ErrInvalidState):
		writeError(w, http.StatusConflict, "run_not_ready", "this plan is not ready to execute")
	case errors.Is(err, agent.ErrBusy):
		writeError(w, http.StatusConflict, "agent_busy", "another agent run is executing; wait for it to finish")
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		writeError(w, http.StatusRequestTimeout, "request_cancelled", "request was cancelled")
	case errors.Is(err, agent.ErrPlanning), errors.Is(err, agent.ErrPlannerResponse), errors.Is(err, llm.ErrProvider):
		a.log.Warn(operation, "error", err)
		writeError(w, http.StatusBadGateway, "agent_planning_failed", "the AI provider could not produce a plan")
	default:
		a.log.Error(operation, "error", err)
		writeError(w, http.StatusInternalServerError, "agent_error", "agent request failed")
	}
}

// agentMessage strips internal error prefixes before returning validation
// details to the browser.
func agentMessage(err error) string {
	message := err.Error()
	prefixes := []string{
		"agent: ",
		"invalid prompt: ",
		"invalid plan: ",
		"plan exceeds message limit: ",
	}
	for {
		trimmed := false
		for _, prefix := range prefixes {
			if strings.HasPrefix(message, prefix) {
				message = strings.TrimPrefix(message, prefix)
				trimmed = true
			}
		}
		if !trimmed {
			break
		}
	}
	return message
}

func (a *API) agentRoutes(r chi.Router) {
	if a.agent == nil {
		return
	}
	r.With(a.requireCSRF).Post("/agent/plan", a.handleAgentPlan)
	r.Get("/agent/runs", a.handleAgentList)
	r.Get("/agent/runs/{id}", a.handleAgentGet)
	r.With(a.requireCSRF).Post("/agent/runs/{id}/execute", a.handleAgentExecute)
}
