package store

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/ph0nkj/mailcontroller/internal/domain"
)

func TestAgentRunLifecycle(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	repo := s.AgentRuns()

	run := domain.AgentRun{Prompt: "Archive old newsletters"}
	require.NoError(t, repo.Create(ctx, &run))
	require.NotEmpty(t, run.ID)
	require.Equal(t, domain.AgentPlanning, run.Status)
	require.False(t, run.CreatedAt.IsZero())

	stored, err := repo.Get(ctx, run.ID)
	require.NoError(t, err)
	require.Equal(t, run.Prompt, stored.Prompt)
	require.Equal(t, domain.AgentPlanning, stored.Status)
	require.Empty(t, stored.Plan.Steps)

	plan := domain.AgentPlan{
		Summary: "Archive newsletters",
		Steps: []domain.AgentStep{
			{Action: domain.AgentActionArchive, Query: "category:promotions older_than:30d", Count: 12},
		},
		Total: 12,
	}
	require.NoError(t, repo.SetPlan(ctx, run.ID, plan))
	stored, err = repo.Get(ctx, run.ID)
	require.NoError(t, err)
	require.Equal(t, domain.AgentPlanned, stored.Status)
	require.Equal(t, plan, stored.Plan)

	started, err := repo.Start(ctx, run.ID)
	require.NoError(t, err)
	require.True(t, started)
	started, err = repo.Start(ctx, run.ID)
	require.NoError(t, err)
	require.False(t, started)

	result := domain.AgentResult{
		Steps: []domain.AgentStepResult{
			{Action: domain.AgentActionArchive, Query: plan.Steps[0].Query, Count: 12, Affected: 10},
		},
		Affected: 10,
	}
	require.NoError(t, repo.Finish(ctx, run.ID, domain.AgentDone, result))
	stored, err = repo.Get(ctx, run.ID)
	require.NoError(t, err)
	require.Equal(t, domain.AgentDone, stored.Status)
	require.Equal(t, result, stored.Result)

	require.Error(t, repo.Finish(ctx, run.ID, domain.AgentRunning, result))
	_, err = repo.Get(ctx, "missing")
	require.ErrorIs(t, err, ErrNotFound)
}

func TestAgentRunListAndRecovery(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	repo := s.AgentRuns()

	base := time.Now()
	planning := domain.AgentRun{Prompt: "planning", CreatedAt: base}
	running := domain.AgentRun{Prompt: "running", CreatedAt: base.Add(time.Second)}
	done := domain.AgentRun{Prompt: "done", CreatedAt: base.Add(2 * time.Second)}
	for _, run := range []*domain.AgentRun{&planning, &running, &done} {
		require.NoError(t, repo.Create(ctx, run))
	}
	plan := domain.AgentPlan{Summary: "s", Steps: []domain.AgentStep{{Action: domain.AgentActionArchive, Query: "in:inbox", Count: 1}}, Total: 1}
	for _, run := range []*domain.AgentRun{&running, &done} {
		require.NoError(t, repo.SetPlan(ctx, run.ID, plan))
	}
	started, err := repo.Start(ctx, running.ID)
	require.NoError(t, err)
	require.True(t, started)
	require.NoError(t, repo.Finish(ctx, done.ID, domain.AgentDone, domain.AgentResult{Affected: 1}))

	runs, err := repo.List(ctx, 10)
	require.NoError(t, err)
	require.Len(t, runs, 3)
	require.Equal(t, done.ID, runs[0].ID)
	require.Equal(t, running.ID, runs[1].ID)
	require.Equal(t, planning.ID, runs[2].ID)

	recovered, err := repo.RecoverInterrupted(ctx)
	require.NoError(t, err)
	require.Equal(t, int64(2), recovered)

	for _, id := range []string{planning.ID, running.ID} {
		stored, getErr := repo.Get(ctx, id)
		require.NoError(t, getErr)
		require.Equal(t, domain.AgentFailed, stored.Status)
		require.Contains(t, stored.Result.Error, "interrupted")
	}
	stored, err := repo.Get(ctx, done.ID)
	require.NoError(t, err)
	require.Equal(t, domain.AgentDone, stored.Status)
}

func TestAgentRunValidation(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	repo := s.AgentRuns()

	require.Error(t, repo.Create(ctx, nil))
	require.Error(t, repo.Create(ctx, &domain.AgentRun{}))
	require.Error(t, repo.Create(ctx, &domain.AgentRun{Prompt: "x", Status: "weird"}))

	run := domain.AgentRun{Prompt: "x"}
	require.NoError(t, repo.Create(ctx, &run))
	require.ErrorIs(t, repo.SetPlan(ctx, "missing", domain.AgentPlan{}), ErrNotFound)
}
