// Package agent plans and executes natural-language mailbox actions with a
// reviewable preview, explicit confirmation and bounded batch sizes.
package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/ph0nkj/mailcontroller/internal/domain"
	"github.com/ph0nkj/mailcontroller/internal/jobs"
)

const (
	maxPromptChars  = 2000
	maxSummaryChars = 500
	maxQueryChars   = 512
	maxReasonChars  = 300
	// batchSize matches Gmail's batchModify limit.
	batchSize = 1000
	// finishTimeout bounds database and audit writes after a job ends.
	finishTimeout = 10 * time.Second
	auditTimeout  = 5 * time.Second

	labelInbox  = "INBOX"
	labelUnread = "UNREAD"
	labelTrash  = "TRASH"
)

var (
	// ErrInvalidPrompt is returned for empty or oversized requests.
	ErrInvalidPrompt = errors.New("agent: invalid prompt")
	// ErrInvalidPlan is returned when the model output is not an actionable plan.
	ErrInvalidPlan = errors.New("agent: invalid plan")
	// ErrPlanTooLarge is returned when a plan would exceed the configured cap.
	ErrPlanTooLarge = errors.New("agent: plan exceeds message limit")
	// ErrNoMatches is returned when no message matches any plan step.
	ErrNoMatches = errors.New("agent: no matching messages")
	// ErrPlanning is returned when the provider or planner fails.
	ErrPlanning = errors.New("agent: planning failed")
	// ErrInvalidState is returned when a run is not ready to execute.
	ErrInvalidState = errors.New("agent: run is not ready")
	// ErrBusy is returned when another agent run is executing.
	ErrBusy = errors.New("agent: another run is active")
)

// Planner turns a natural-language request into a plan without reading mail.
type Planner interface {
	Plan(ctx context.Context, prompt string) (domain.AgentPlan, error)
}

// Mailbox is the Gmail surface used for previews and execution.
type Mailbox interface {
	CountMessages(ctx context.Context, query string) (int64, error)
	ListMessageIDs(ctx context.Context, query string, limit int) ([]string, error)
	BatchModify(ctx context.Context, ids, addLabels, removeLabels []string) error
}

// Store persists runs and their plans.
type Store interface {
	Create(ctx context.Context, run *domain.AgentRun) error
	SetPlan(ctx context.Context, id string, plan domain.AgentPlan) error
	Start(ctx context.Context, id string) (bool, error)
	Finish(ctx context.Context, id, status string, result domain.AgentResult) error
	Get(ctx context.Context, id string) (domain.AgentRun, error)
	List(ctx context.Context, limit int) ([]domain.AgentRun, error)
	RecoverInterrupted(ctx context.Context) (int64, error)
}

// AuditStore records security-relevant actions.
type AuditStore interface {
	Add(ctx context.Context, entry domain.AuditEntry) error
}

// Options bounds plan size.
type Options struct {
	MaxMessages int
	MaxSteps    int
}

// Service plans and executes agent runs.
type Service struct {
	planner     Planner
	mailbox     Mailbox
	store       Store
	jobs        *jobs.Runner
	audit       AuditStore
	log         *slog.Logger
	maxMessages int
	maxSteps    int
	active      atomic.Bool
}

// NewService creates an agent service.
func NewService(
	planner Planner,
	mailbox Mailbox,
	store Store,
	runner *jobs.Runner,
	audit AuditStore,
	log *slog.Logger,
	opts Options,
) (*Service, error) {
	if planner == nil || mailbox == nil || store == nil {
		return nil, errors.New("agent: planner, mailbox and store are required")
	}
	if runner == nil {
		runner = jobs.NewRunner(20, 30*time.Minute)
	}
	if log == nil {
		log = slog.Default()
	}
	if opts.MaxMessages <= 0 {
		opts.MaxMessages = 10000
	}
	if opts.MaxSteps <= 0 {
		opts.MaxSteps = 5
	}
	return &Service{
		planner:     planner,
		mailbox:     mailbox,
		store:       store,
		jobs:        runner,
		audit:       audit,
		log:         log,
		maxMessages: opts.MaxMessages,
		maxSteps:    opts.MaxSteps,
	}, nil
}

// Plan asks the planner for a plan, validates it, counts every step and stores
// the reviewable result. Nothing in the mailbox is changed.
func (s *Service) Plan(ctx context.Context, prompt string) (domain.AgentRun, error) {
	prompt, err := validatePrompt(prompt)
	if err != nil {
		return domain.AgentRun{}, err
	}
	run := domain.AgentRun{Prompt: prompt}
	if err := s.store.Create(ctx, &run); err != nil {
		return domain.AgentRun{}, fmt.Errorf("agent: create run: %w", err)
	}
	plan, err := s.planner.Plan(ctx, prompt)
	if err != nil {
		s.finishRun(ctx, run.ID, domain.AgentFailed, domain.AgentResult{Error: "planning failed"})
		return domain.AgentRun{}, fmt.Errorf("%w: %w", ErrPlanning, err)
	}
	plan, err = validatePlan(plan, s.maxSteps)
	if err != nil {
		s.finishRun(ctx, run.ID, domain.AgentFailed, domain.AgentResult{Error: "invalid plan"})
		return domain.AgentRun{}, err
	}
	plan, err = s.countPlan(ctx, plan)
	if err != nil {
		s.finishRun(ctx, run.ID, domain.AgentFailed, domain.AgentResult{Error: "plan preview failed"})
		return domain.AgentRun{}, err
	}
	if err := s.store.SetPlan(ctx, run.ID, plan); err != nil {
		s.finishRun(ctx, run.ID, domain.AgentFailed, domain.AgentResult{Error: "could not store plan"})
		return domain.AgentRun{}, fmt.Errorf("agent: store plan: %w", err)
	}
	run.Plan = plan
	run.Status = domain.AgentPlanned
	s.auditEvent(ctx, "agent.plan", map[string]any{
		"run_id":   run.ID,
		"steps":    len(plan.Steps),
		"messages": plan.Total,
	})
	return run, nil
}

// Execute starts a confirmed plan as a background job. Only one agent run may
// execute at a time.
func (s *Service) Execute(ctx context.Context, id string) (domain.AgentRun, string, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return domain.AgentRun{}, "", ErrInvalidState
	}
	if !s.active.CompareAndSwap(false, true) {
		return domain.AgentRun{}, "", ErrBusy
	}
	run, err := s.store.Get(ctx, id)
	if err != nil {
		s.active.Store(false)
		return domain.AgentRun{}, "", fmt.Errorf("agent: load run: %w", err)
	}
	if run.Status != domain.AgentPlanned {
		s.active.Store(false)
		return domain.AgentRun{}, "", ErrInvalidState
	}
	if err := s.checkExecutionPlan(run.Plan); err != nil {
		s.active.Store(false)
		return domain.AgentRun{}, "", err
	}
	started, err := s.store.Start(ctx, id)
	if err != nil {
		s.active.Store(false)
		return domain.AgentRun{}, "", fmt.Errorf("agent: start run: %w", err)
	}
	if !started {
		s.active.Store(false)
		return domain.AgentRun{}, "", ErrInvalidState
	}
	run.Status = domain.AgentRunning
	s.auditEvent(ctx, "agent.execute", map[string]any{
		"run_id":   run.ID,
		"steps":    len(run.Plan.Steps),
		"messages": run.Plan.Total,
	})
	job := s.jobs.Start("agent.run", int64(run.Plan.Total), func(jobCtx context.Context, progress *jobs.Progress) error {
		defer s.active.Store(false)
		result, execErr := s.executePlan(jobCtx, run, progress)
		status := domain.AgentDone
		if execErr != nil {
			status = domain.AgentFailed
			if result.Error == "" {
				result.Error = "mailbox action failed"
			}
		}
		s.finishRun(jobCtx, run.ID, status, result)
		return execErr
	})
	return run, job.ID, nil
}

// Get returns one run.
func (s *Service) Get(ctx context.Context, id string) (domain.AgentRun, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return domain.AgentRun{}, ErrInvalidState
	}
	return s.store.Get(ctx, id)
}

// List returns recent runs.
func (s *Service) List(ctx context.Context, limit int) ([]domain.AgentRun, error) {
	if limit <= 0 {
		limit = 20
	}
	if limit > 50 {
		limit = 50
	}
	return s.store.List(ctx, limit)
}

// RecoverInterrupted marks runs left behind by a previous process as failed.
func (s *Service) RecoverInterrupted(ctx context.Context) (int64, error) {
	count, err := s.store.RecoverInterrupted(ctx)
	if err != nil {
		return 0, err
	}
	if count > 0 {
		s.auditEvent(ctx, "agent.recover", map[string]any{"runs": count})
	}
	return count, nil
}

// checkExecutionPlan rechecks limits at execution time. Plans are stored, so a
// restart or a lower environment limit must not allow an oversized run.
func (s *Service) checkExecutionPlan(plan domain.AgentPlan) error {
	if len(plan.Steps) == 0 || len(plan.Steps) > s.maxSteps ||
		plan.Total <= 0 || plan.Total > s.maxMessages {
		return fmt.Errorf("%w: this plan exceeds current limits; create a new plan", ErrPlanTooLarge)
	}
	total := 0
	for _, step := range plan.Steps {
		if !validAction(step.Action) || strings.TrimSpace(step.Query) == "" ||
			step.Count <= 0 || step.Count > s.maxMessages {
			return fmt.Errorf("%w: this plan is no longer valid; create a new plan", ErrInvalidPlan)
		}
		total += step.Count
	}
	if total != plan.Total {
		return fmt.Errorf("%w: this plan is no longer valid; create a new plan", ErrInvalidPlan)
	}
	return nil
}

func (s *Service) countPlan(ctx context.Context, plan domain.AgentPlan) (domain.AgentPlan, error) {
	steps := make([]domain.AgentStep, 0, len(plan.Steps))
	total := 0
	for _, step := range plan.Steps {
		count, err := s.mailbox.CountMessages(ctx, step.Query)
		if err != nil {
			return domain.AgentPlan{}, fmt.Errorf("agent: count messages: %w", err)
		}
		if count == 0 {
			continue
		}
		if count > int64(s.maxMessages) {
			return domain.AgentPlan{}, fmt.Errorf("%w: one step matches %d messages (limit %d)",
				ErrPlanTooLarge, count, s.maxMessages)
		}
		if total+int(count) > s.maxMessages {
			return domain.AgentPlan{}, fmt.Errorf("%w: plan would touch %d messages (limit %d)",
				ErrPlanTooLarge, total+int(count), s.maxMessages)
		}
		step.Count = int(count)
		total += int(count)
		steps = append(steps, step)
	}
	if len(steps) == 0 {
		return domain.AgentPlan{}, fmt.Errorf("%w: adjust the request and try again", ErrNoMatches)
	}
	plan.Steps = steps
	plan.Total = total
	return plan, nil
}

func (s *Service) executePlan(
	ctx context.Context,
	run domain.AgentRun,
	progress *jobs.Progress,
) (domain.AgentResult, error) {
	result := domain.AgentResult{Steps: make([]domain.AgentStepResult, 0, len(run.Plan.Steps))}
	affected := 0
	for _, step := range run.Plan.Steps {
		ids, err := s.mailbox.ListMessageIDs(ctx, step.Query, step.Count)
		if err != nil {
			return result, fmt.Errorf("agent: list messages: %w", err)
		}
		stepResult := domain.AgentStepResult{
			Action: step.Action,
			Query:  step.Query,
			Count:  step.Count,
		}
		for start := 0; start < len(ids); start += batchSize {
			end := min(start+batchSize, len(ids))
			add, remove := labelsFor(step.Action)
			if err := s.mailbox.BatchModify(ctx, ids[start:end], add, remove); err != nil {
				return result, fmt.Errorf("agent: apply %s: %w", step.Action, err)
			}
			stepResult.Affected += end - start
			affected += end - start
			progress.Report(int64(affected))
		}
		result.Steps = append(result.Steps, stepResult)
	}
	result.Affected = affected
	return result, nil
}

func (s *Service) finishRun(ctx context.Context, id, status string, result domain.AgentResult) {
	finishCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), finishTimeout)
	defer cancel()
	if err := s.store.Finish(finishCtx, id, status, result); err != nil {
		s.log.Error("finish agent run", "run_id", id, "error", err)
	}
	s.auditEvent(finishCtx, "agent.finish", map[string]any{
		"run_id":   id,
		"ok":       status == domain.AgentDone,
		"affected": result.Affected,
	})
}

func (s *Service) auditEvent(ctx context.Context, action string, details map[string]any) {
	if s.audit == nil {
		return
	}
	raw, err := json.Marshal(details)
	if err != nil {
		raw = []byte("{}")
	}
	auditCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), auditTimeout)
	defer cancel()
	entry := domain.AuditEntry{Actor: "admin", Action: action, Details: string(raw)}
	if err := s.audit.Add(auditCtx, entry); err != nil {
		s.log.Warn("write agent audit entry", "action", action, "error", err)
	}
}

func labelsFor(action string) (add, remove []string) {
	switch action {
	case domain.AgentActionMarkRead:
		return nil, []string{labelUnread}
	case domain.AgentActionMarkUnread:
		return []string{labelUnread}, nil
	case domain.AgentActionArchive:
		return nil, []string{labelInbox}
	case domain.AgentActionTrash:
		return []string{labelTrash}, nil
	default:
		return nil, nil
	}
}

func validatePrompt(prompt string) (string, error) {
	prompt = strings.TrimSpace(prompt)
	if prompt == "" {
		return "", fmt.Errorf("%w: describe the mailbox action you want", ErrInvalidPrompt)
	}
	if utf8.RuneCountInString(prompt) > maxPromptChars {
		return "", fmt.Errorf("%w: prompt must be at most %d characters", ErrInvalidPrompt, maxPromptChars)
	}
	for _, r := range prompt {
		if (r < 0x20 && r != '\n' && r != '\r' && r != '\t') || r == 0x7f {
			return "", fmt.Errorf("%w: prompt contains control characters", ErrInvalidPrompt)
		}
	}
	return prompt, nil
}

func validatePlan(plan domain.AgentPlan, maxSteps int) (domain.AgentPlan, error) {
	if len(plan.Steps) == 0 {
		return domain.AgentPlan{}, fmt.Errorf("%w: the request does not describe a mailbox action", ErrInvalidPlan)
	}
	if len(plan.Steps) > maxSteps {
		return domain.AgentPlan{}, fmt.Errorf("%w: a plan may contain at most %d steps", ErrInvalidPlan, maxSteps)
	}
	summary := strings.TrimSpace(plan.Summary)
	if utf8.RuneCountInString(summary) > maxSummaryChars {
		return domain.AgentPlan{}, fmt.Errorf("%w: summary is too long", ErrInvalidPlan)
	}
	if summary == "" {
		summary = fmt.Sprintf("%d mailbox action(s) will be applied.", len(plan.Steps))
	}
	seen := make(map[string]struct{}, len(plan.Steps))
	steps := make([]domain.AgentStep, len(plan.Steps))
	for i, step := range plan.Steps {
		action := strings.ToLower(strings.TrimSpace(step.Action))
		query := strings.TrimSpace(step.Query)
		reason := strings.TrimSpace(step.Reason)
		if !validAction(action) {
			return domain.AgentPlan{}, fmt.Errorf("%w: unsupported action %q", ErrInvalidPlan, action)
		}
		if query == "" || utf8.RuneCountInString(query) > maxQueryChars || containsControl(query) {
			return domain.AgentPlan{}, fmt.Errorf("%w: each step needs a valid Gmail query", ErrInvalidPlan)
		}
		if utf8.RuneCountInString(reason) > maxReasonChars || containsControl(reason) {
			return domain.AgentPlan{}, fmt.Errorf("%w: step reason is too long", ErrInvalidPlan)
		}
		key := action + "\x00" + query
		if _, ok := seen[key]; ok {
			return domain.AgentPlan{}, fmt.Errorf("%w: duplicate step", ErrInvalidPlan)
		}
		seen[key] = struct{}{}
		steps[i] = domain.AgentStep{Action: action, Query: query, Reason: reason}
	}
	return domain.AgentPlan{Summary: summary, Steps: steps}, nil
}

func validAction(action string) bool {
	switch action {
	case domain.AgentActionMarkRead, domain.AgentActionMarkUnread,
		domain.AgentActionArchive, domain.AgentActionTrash:
		return true
	default:
		return false
	}
}

func containsControl(value string) bool {
	for _, r := range value {
		if r < 0x20 || r == 0x7f {
			return true
		}
	}
	return false
}
