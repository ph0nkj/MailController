package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/ph0nkj/mailcontroller/internal/domain"
)

// AgentRepo persists agent runs and their reviewed plans.
type AgentRepo struct {
	db *sql.DB
}

// AgentRuns returns the agent-run repository.
func (s *Store) AgentRuns() *AgentRepo { return &AgentRepo{db: s.db} }

// Create inserts a run in the planning state. A missing id and timestamp are
// generated.
func (r *AgentRepo) Create(ctx context.Context, run *domain.AgentRun) error {
	if run == nil {
		return errors.New("store: agent run is nil")
	}
	if strings.TrimSpace(run.Prompt) == "" {
		return errors.New("store: agent prompt is required")
	}
	if run.ID == "" {
		id, err := newAgentID()
		if err != nil {
			return err
		}
		run.ID = id
	}
	if run.Status == "" {
		run.Status = domain.AgentPlanning
	}
	if !validAgentStatus(run.Status) {
		return fmt.Errorf("store: invalid agent status %q", run.Status)
	}
	if run.CreatedAt.IsZero() {
		run.CreatedAt = time.Now()
	}
	planJSON, err := json.Marshal(run.Plan)
	if err != nil {
		return fmt.Errorf("store: encode agent plan: %w", err)
	}
	resultJSON, err := json.Marshal(run.Result)
	if err != nil {
		return fmt.Errorf("store: encode agent result: %w", err)
	}
	if _, err := r.db.ExecContext(ctx,
		`INSERT INTO agent_runs (id, prompt, plan_json, status, result_json, created_at)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		run.ID, run.Prompt, string(planJSON), run.Status, string(resultJSON), run.CreatedAt.Unix(),
	); err != nil {
		return fmt.Errorf("store: create agent run: %w", err)
	}
	return nil
}

// SetPlan stores a reviewed plan and moves the run to planned.
func (r *AgentRepo) SetPlan(ctx context.Context, id string, plan domain.AgentPlan) error {
	raw, err := json.Marshal(plan)
	if err != nil {
		return fmt.Errorf("store: encode agent plan: %w", err)
	}
	result, err := r.db.ExecContext(ctx,
		`UPDATE agent_runs SET plan_json = ?, status = ? WHERE id = ? AND status = ?`,
		string(raw), domain.AgentPlanned, id, domain.AgentPlanning)
	if err != nil {
		return fmt.Errorf("store: store agent plan: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("store: store agent plan: %w", err)
	}
	if affected == 0 {
		return ErrNotFound
	}
	return nil
}

// Start atomically moves a planned run to running. It reports false when the
// run was not in the planned state.
func (r *AgentRepo) Start(ctx context.Context, id string) (bool, error) {
	result, err := r.db.ExecContext(ctx,
		`UPDATE agent_runs SET status = ? WHERE id = ? AND status = ?`,
		domain.AgentRunning, id, domain.AgentPlanned)
	if err != nil {
		return false, fmt.Errorf("store: start agent run: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("store: start agent run: %w", err)
	}
	return affected == 1, nil
}

// Finish stores a terminal status and the outcome of a run.
func (r *AgentRepo) Finish(ctx context.Context, id, status string, result domain.AgentResult) error {
	switch status {
	case domain.AgentDone, domain.AgentFailed, domain.AgentCanceled:
	default:
		return fmt.Errorf("store: invalid final agent status %q", status)
	}
	raw, err := json.Marshal(result)
	if err != nil {
		return fmt.Errorf("store: encode agent result: %w", err)
	}
	if _, err := r.db.ExecContext(ctx,
		`UPDATE agent_runs SET status = ?, result_json = ? WHERE id = ?`,
		status, string(raw), id,
	); err != nil {
		return fmt.Errorf("store: finish agent run: %w", err)
	}
	return nil
}

// Get returns one run or ErrNotFound.
func (r *AgentRepo) Get(ctx context.Context, id string) (domain.AgentRun, error) {
	row := r.db.QueryRowContext(ctx,
		`SELECT id, prompt, plan_json, status, result_json, created_at
		 FROM agent_runs WHERE id = ?`, id)
	run, err := scanAgentRun(row)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.AgentRun{}, ErrNotFound
	}
	if err != nil {
		return domain.AgentRun{}, fmt.Errorf("store: get agent run: %w", err)
	}
	return run, nil
}

// List returns recent runs, newest first.
func (r *AgentRepo) List(ctx context.Context, limit int) ([]domain.AgentRun, error) {
	if limit <= 0 {
		limit = 20
	}
	if limit > 100 {
		limit = 100
	}
	rows, err := r.db.QueryContext(ctx,
		`SELECT id, prompt, plan_json, status, result_json, created_at
		 FROM agent_runs ORDER BY created_at DESC, id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("store: list agent runs: %w", err)
	}
	defer func() { _ = rows.Close() }()

	runs := make([]domain.AgentRun, 0, limit)
	for rows.Next() {
		run, scanErr := scanAgentRun(rows)
		if scanErr != nil {
			return nil, fmt.Errorf("store: scan agent run: %w", scanErr)
		}
		runs = append(runs, run)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: iterate agent runs: %w", err)
	}
	return runs, nil
}

// RecoverInterrupted marks runs left planning or running by a previous process
// as failed. It returns the number of affected runs.
func (r *AgentRepo) RecoverInterrupted(ctx context.Context) (int64, error) {
	result, err := r.db.ExecContext(ctx,
		`UPDATE agent_runs SET status = ?, result_json = ?
		 WHERE status IN (?, ?)`,
		domain.AgentFailed, `{"error":"interrupted by restart"}`,
		domain.AgentPlanning, domain.AgentRunning)
	if err != nil {
		return 0, fmt.Errorf("store: recover agent runs: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("store: recover agent runs: %w", err)
	}
	return affected, nil
}

func scanAgentRun(row rowScanner) (domain.AgentRun, error) {
	var (
		run        domain.AgentRun
		planJSON   string
		resultJSON string
		createdAt  int64
	)
	if err := row.Scan(&run.ID, &run.Prompt, &planJSON, &run.Status, &resultJSON, &createdAt); err != nil {
		return domain.AgentRun{}, err
	}
	if err := json.Unmarshal([]byte(planJSON), &run.Plan); err != nil {
		return domain.AgentRun{}, fmt.Errorf("decode plan: %w", err)
	}
	if err := json.Unmarshal([]byte(resultJSON), &run.Result); err != nil {
		return domain.AgentRun{}, fmt.Errorf("decode result: %w", err)
	}
	run.CreatedAt = time.Unix(createdAt, 0)
	return run, nil
}

func validAgentStatus(status string) bool {
	switch status {
	case domain.AgentPlanning, domain.AgentPlanned, domain.AgentRunning,
		domain.AgentDone, domain.AgentFailed, domain.AgentCanceled:
		return true
	default:
		return false
	}
}

func newAgentID() (string, error) {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("store: generate agent id: %w", err)
	}
	return hex.EncodeToString(buf), nil
}
