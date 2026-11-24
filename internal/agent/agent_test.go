package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/ph0nkj/mailcontroller/internal/domain"
	"github.com/ph0nkj/mailcontroller/internal/jobs"
	"github.com/ph0nkj/mailcontroller/internal/store"
)

type fakePlanner struct {
	plan    domain.AgentPlan
	err     error
	prompts []string
}

func (p *fakePlanner) Plan(_ context.Context, prompt string) (domain.AgentPlan, error) {
	p.prompts = append(p.prompts, prompt)
	return p.plan, p.err
}

type batchCall struct {
	ids    []string
	add    []string
	remove []string
}

type fakeMailbox struct {
	mu        sync.Mutex
	counts    map[string]int64
	ids       map[string][]string
	countErr  error
	listErr   error
	batchErr  error
	batchErrs map[string]error
	batches   []batchCall
}

func (m *fakeMailbox) CountMessages(_ context.Context, query string) (int64, error) {
	if m.countErr != nil {
		return 0, m.countErr
	}
	return m.counts[query], nil
}

func (m *fakeMailbox) ListMessageIDs(_ context.Context, query string, limit int) ([]string, error) {
	if m.listErr != nil {
		return nil, m.listErr
	}
	ids := m.ids[query]
	if limit > 0 && len(ids) > limit {
		ids = ids[:limit]
	}
	return append([]string(nil), ids...), nil
}

func (m *fakeMailbox) BatchModify(_ context.Context, ids, add, remove []string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.batches = append(m.batches, batchCall{
		ids:    append([]string(nil), ids...),
		add:    append([]string(nil), add...),
		remove: append([]string(nil), remove...),
	})
	if m.batchErr != nil {
		return m.batchErr
	}
	if len(m.batchErrs) > 0 {
		key := fmt.Sprintf("%d", len(m.batches))
		if err := m.batchErrs[key]; err != nil {
			return err
		}
	}
	return nil
}

type fakeAudit struct {
	mu      sync.Mutex
	entries []domain.AuditEntry
}

func (a *fakeAudit) Add(_ context.Context, entry domain.AuditEntry) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.entries = append(a.entries, entry)
	return nil
}

func (a *fakeAudit) actions() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	actions := make([]string, 0, len(a.entries))
	for _, entry := range a.entries {
		actions = append(actions, entry.Action)
	}
	return actions
}

func (a *fakeAudit) details(t *testing.T, action string) map[string]any {
	t.Helper()
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, entry := range a.entries {
		if entry.Action != action {
			continue
		}
		var value map[string]any
		require.NoError(t, json.Unmarshal([]byte(entry.Details), &value))
		return value
	}
	t.Fatalf("audit action %q not found", action)
	return nil
}

func newTestStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "agent.db"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, st.Close()) })
	return st
}

func newTestService(
	t *testing.T,
	planner Planner,
	mailbox Mailbox,
	audit AuditStore,
	opts Options,
) (*Service, *store.Store) {
	t.Helper()
	st := newTestStore(t)
	service, err := NewService(planner, mailbox, st.AgentRuns(), jobs.NewRunner(10, time.Minute), audit,
		slog.New(slog.NewTextHandler(io.Discard, nil)), opts)
	require.NoError(t, err)
	return service, st
}

func TestServicePlanCountsAndDropsEmptySteps(t *testing.T) {
	planner := &fakePlanner{plan: domain.AgentPlan{
		Summary: "Clean up old mail",
		Steps: []domain.AgentStep{
			{Action: "archive", Query: "category:promotions older_than:30d", Reason: "old promos"},
			{Action: "mark_read", Query: "is:unread", Reason: "nothing here"},
			{Action: "trash", Query: "from:spam@example.com"},
		},
	}}
	mailbox := &fakeMailbox{
		counts: map[string]int64{
			"category:promotions older_than:30d": 12,
			"is:unread":                          0,
			"from:spam@example.com":              3,
		},
	}
	audit := &fakeAudit{}
	service, st := newTestService(t, planner, mailbox, audit, Options{MaxMessages: 100, MaxSteps: 5})

	run, err := service.Plan(context.Background(), "  archive old promos and trash spam  ")
	require.NoError(t, err)
	require.Equal(t, domain.AgentPlanned, run.Status)
	require.Equal(t, "archive old promos and trash spam", run.Prompt)
	require.Equal(t, 15, run.Plan.Total)
	require.Len(t, run.Plan.Steps, 2)
	require.Equal(t, 12, run.Plan.Steps[0].Count)
	require.Equal(t, 3, run.Plan.Steps[1].Count)
	require.Equal(t, []string{"archive old promos and trash spam"}, planner.prompts)

	stored, err := st.AgentRuns().Get(context.Background(), run.ID)
	require.NoError(t, err)
	require.Equal(t, run.Plan, stored.Plan)
	require.Equal(t, domain.AgentPlanned, stored.Status)

	require.Equal(t, []string{"agent.plan"}, audit.actions())
	details := audit.details(t, "agent.plan")
	require.Equal(t, float64(2), details["steps"])
	require.Equal(t, float64(15), details["messages"])
	require.NotContains(t, details, "prompt")
}

func TestServicePlanRejectsInvalidPrompt(t *testing.T) {
	service, st := newTestService(t, &fakePlanner{}, &fakeMailbox{}, nil, Options{})

	for name, prompt := range map[string]string{
		"empty":    "   ",
		"too long": strings.Repeat("a", maxPromptChars+1),
		"control":  "archive\x00everything",
	} {
		_, err := validatePrompt(prompt)
		require.ErrorIs(t, err, ErrInvalidPrompt, name)
	}
	clean, err := validatePrompt("  archive\nold mail  ")
	require.NoError(t, err)
	require.Equal(t, "archive\nold mail", clean)

	// Invalid prompts never reach the store.
	_, err = service.Plan(context.Background(), " ")
	require.ErrorIs(t, err, ErrInvalidPrompt)
	runs, err := st.AgentRuns().List(context.Background(), 10)
	require.NoError(t, err)
	require.Empty(t, runs)
}

func TestServicePlanValidationAndLimits(t *testing.T) {
	tests := []struct {
		name    string
		plan    domain.AgentPlan
		counts  map[string]int64
		opts    Options
		wantErr error
	}{
		{
			name:    "no steps",
			plan:    domain.AgentPlan{Summary: "nothing"},
			wantErr: ErrInvalidPlan,
		},
		{
			name: "unsupported action",
			plan: domain.AgentPlan{Steps: []domain.AgentStep{
				{Action: "delete_forever", Query: "in:inbox"},
			}},
			wantErr: ErrInvalidPlan,
		},
		{
			name: "duplicate step",
			plan: domain.AgentPlan{Steps: []domain.AgentStep{
				{Action: "archive", Query: "in:inbox"},
				{Action: "archive", Query: "in:inbox"},
			}},
			opts:    Options{MaxSteps: 5},
			wantErr: ErrInvalidPlan,
		},
		{
			name: "too many steps",
			plan: domain.AgentPlan{Steps: []domain.AgentStep{
				{Action: "archive", Query: "a"},
				{Action: "archive", Query: "b"},
				{Action: "archive", Query: "c"},
			}},
			opts:    Options{MaxSteps: 2},
			wantErr: ErrInvalidPlan,
		},
		{
			name: "empty query",
			plan: domain.AgentPlan{Steps: []domain.AgentStep{
				{Action: "archive", Query: "  "},
			}},
			wantErr: ErrInvalidPlan,
		},
		{
			name: "control in query",
			plan: domain.AgentPlan{Steps: []domain.AgentStep{
				{Action: "archive", Query: "in:\ninbox"},
			}},
			wantErr: ErrInvalidPlan,
		},
		{
			name: "one step too large",
			plan: domain.AgentPlan{Steps: []domain.AgentStep{
				{Action: "archive", Query: "big"},
			}},
			counts:  map[string]int64{"big": 11},
			opts:    Options{MaxMessages: 10},
			wantErr: ErrPlanTooLarge,
		},
		{
			name: "total too large",
			plan: domain.AgentPlan{Steps: []domain.AgentStep{
				{Action: "archive", Query: "a"},
				{Action: "trash", Query: "b"},
			}},
			counts:  map[string]int64{"a": 6, "b": 6},
			opts:    Options{MaxMessages: 10},
			wantErr: ErrPlanTooLarge,
		},
		{
			name: "no matches",
			plan: domain.AgentPlan{Steps: []domain.AgentStep{
				{Action: "archive", Query: "none"},
			}},
			counts:  map[string]int64{"none": 0},
			wantErr: ErrNoMatches,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			opts := tc.opts
			if opts.MaxMessages == 0 {
				opts.MaxMessages = 100
			}
			service, st := newTestService(t, &fakePlanner{plan: tc.plan},
				&fakeMailbox{counts: tc.counts}, nil, opts)
			_, err := service.Plan(context.Background(), "do it")
			require.ErrorIs(t, err, tc.wantErr)
			runs, listErr := st.AgentRuns().List(context.Background(), 10)
			require.NoError(t, listErr)
			require.Len(t, runs, 1)
			require.Equal(t, domain.AgentFailed, runs[0].Status)
		})
	}
}

func TestServicePlanFailures(t *testing.T) {
	plannerErr := errors.New("provider down")
	service, st := newTestService(t, &fakePlanner{err: plannerErr}, &fakeMailbox{}, nil, Options{})
	_, err := service.Plan(context.Background(), "archive old mail")
	require.ErrorIs(t, err, ErrPlanning)
	require.ErrorIs(t, err, plannerErr)
	runs, err := st.AgentRuns().List(context.Background(), 10)
	require.NoError(t, err)
	require.Len(t, runs, 1)
	require.Equal(t, domain.AgentFailed, runs[0].Status)

	countErr := errors.New("gmail down")
	service, _ = newTestService(t, &fakePlanner{plan: domain.AgentPlan{Steps: []domain.AgentStep{
		{Action: "archive", Query: "in:inbox"},
	}}}, &fakeMailbox{countErr: countErr}, nil, Options{})
	_, err = service.Plan(context.Background(), "archive old mail")
	require.ErrorIs(t, err, countErr)
}

func TestServiceExecuteBatchesAndAudits(t *testing.T) {
	mailbox := &fakeMailbox{
		counts: map[string]int64{"in:inbox older_than:1y": 1500, "is:unread": 2},
		ids: map[string][]string{
			"in:inbox older_than:1y": makeIDs("a", 1500),
			"is:unread":              {"u1", "u2"},
		},
	}
	audit := &fakeAudit{}
	service, st := newTestService(t, &fakePlanner{}, mailbox, audit, Options{})
	run := createPlannedRun(t, st, domain.AgentPlan{
		Summary: "archive and mark read",
		Steps: []domain.AgentStep{
			{Action: domain.AgentActionArchive, Query: "in:inbox older_than:1y", Count: 1500},
			{Action: domain.AgentActionMarkRead, Query: "is:unread", Count: 2},
		},
		Total: 1502,
	})

	started, jobID, err := service.Execute(context.Background(), run.ID)
	require.NoError(t, err)
	require.Equal(t, domain.AgentRunning, started.Status)
	require.NotEmpty(t, jobID)

	job := waitJob(t, service.jobs, jobID)
	require.Equal(t, jobs.StatusDone, job.Status, job.Error)
	require.Equal(t, int64(1502), job.Done)

	stored, err := service.Get(context.Background(), run.ID)
	require.NoError(t, err)
	require.Equal(t, domain.AgentDone, stored.Status)
	require.Equal(t, 1502, stored.Result.Affected)
	require.Len(t, stored.Result.Steps, 2)
	require.Equal(t, 1500, stored.Result.Steps[0].Affected)
	require.Equal(t, 2, stored.Result.Steps[1].Affected)

	require.Len(t, mailbox.batches, 3)
	require.Len(t, mailbox.batches[0].ids, 1000)
	require.Empty(t, mailbox.batches[0].add)
	require.Equal(t, []string{"INBOX"}, mailbox.batches[0].remove)
	require.Len(t, mailbox.batches[1].ids, 500)
	require.Equal(t, []string{"UNREAD"}, mailbox.batches[2].remove)

	require.Equal(t, []string{"agent.execute", "agent.finish"}, audit.actions())
	finishDetails := audit.details(t, "agent.finish")
	ok, isBool := finishDetails["ok"].(bool)
	require.True(t, isBool)
	require.True(t, ok)
	require.Equal(t, float64(1502), finishDetails["affected"])
}

func TestServiceExecuteFailureMarksRunFailed(t *testing.T) {
	mailbox := &fakeMailbox{
		counts:    map[string]int64{"in:inbox": 3},
		ids:       map[string][]string{"in:inbox": {"a", "b", "c"}},
		batchErrs: map[string]error{"1": errors.New("gmail rejected batch")},
	}
	audit := &fakeAudit{}
	service, st := newTestService(t, &fakePlanner{}, mailbox, audit, Options{})
	run := createPlannedRun(t, st, domain.AgentPlan{
		Steps: []domain.AgentStep{{Action: domain.AgentActionTrash, Query: "in:inbox", Count: 3}},
		Total: 3,
	})

	_, jobID, err := service.Execute(context.Background(), run.ID)
	require.NoError(t, err)
	job := waitJob(t, service.jobs, jobID)
	require.Equal(t, jobs.StatusFailed, job.Status)
	require.Contains(t, job.Error, "gmail rejected batch")

	stored, err := service.Get(context.Background(), run.ID)
	require.NoError(t, err)
	require.Equal(t, domain.AgentFailed, stored.Status)
	require.NotEmpty(t, stored.Result.Error)
	ok, isBool := audit.details(t, "agent.finish")["ok"].(bool)
	require.True(t, isBool)
	require.False(t, ok)
	require.Equal(t, []string{"TRASH"}, mailbox.batches[0].add)
}

func TestServiceExecuteStateGuards(t *testing.T) {
	service, st := newTestService(t, &fakePlanner{}, &fakeMailbox{}, nil, Options{})

	_, _, err := service.Execute(context.Background(), "missing")
	require.ErrorIs(t, err, store.ErrNotFound)

	planning := domain.AgentRun{Prompt: "pending"}
	require.NoError(t, st.AgentRuns().Create(context.Background(), &planning))
	_, _, err = service.Execute(context.Background(), planning.ID)
	require.ErrorIs(t, err, ErrInvalidState)

	planned := createPlannedRun(t, st, domain.AgentPlan{
		Steps: []domain.AgentStep{{Action: domain.AgentActionArchive, Query: "in:inbox", Count: 1}},
		Total: 1,
	})
	service.active.Store(true)
	_, _, err = service.Execute(context.Background(), planned.ID)
	require.ErrorIs(t, err, ErrBusy)
	service.active.Store(false)

	_, _, err = service.Execute(context.Background(), "")
	require.ErrorIs(t, err, ErrInvalidState)
}

func TestServiceExecuteRechecksStoredPlanLimits(t *testing.T) {
	service, st := newTestService(t, &fakePlanner{}, &fakeMailbox{}, nil,
		Options{MaxMessages: 5, MaxSteps: 2})

	tooLarge := createPlannedRun(t, st, domain.AgentPlan{
		Steps: []domain.AgentStep{{Action: domain.AgentActionArchive, Query: "in:inbox", Count: 6}},
		Total: 6,
	})
	_, _, err := service.Execute(context.Background(), tooLarge.ID)
	require.ErrorIs(t, err, ErrPlanTooLarge)
	require.False(t, service.active.Load())
	stored, err := st.AgentRuns().Get(context.Background(), tooLarge.ID)
	require.NoError(t, err)
	require.Equal(t, domain.AgentPlanned, stored.Status)

	zeroCount := createPlannedRun(t, st, domain.AgentPlan{
		Steps: []domain.AgentStep{{Action: domain.AgentActionArchive, Query: "in:inbox"}},
		Total: 1,
	})
	_, _, err = service.Execute(context.Background(), zeroCount.ID)
	require.ErrorIs(t, err, ErrInvalidPlan)
	require.False(t, service.active.Load())

	mismatch := createPlannedRun(t, st, domain.AgentPlan{
		Steps: []domain.AgentStep{{Action: domain.AgentActionArchive, Query: "in:inbox", Count: 2}},
		Total: 3,
	})
	_, _, err = service.Execute(context.Background(), mismatch.ID)
	require.ErrorIs(t, err, ErrInvalidPlan)
	require.False(t, service.active.Load())

	tooManySteps := createPlannedRun(t, st, domain.AgentPlan{
		Steps: []domain.AgentStep{
			{Action: domain.AgentActionArchive, Query: "a", Count: 1},
			{Action: domain.AgentActionArchive, Query: "b", Count: 1},
			{Action: domain.AgentActionArchive, Query: "c", Count: 1},
		},
		Total: 3,
	})
	_, _, err = service.Execute(context.Background(), tooManySteps.ID)
	require.ErrorIs(t, err, ErrPlanTooLarge)
	require.False(t, service.active.Load())
}

func TestServiceRecoverInterrupted(t *testing.T) {
	audit := &fakeAudit{}
	service, st := newTestService(t, &fakePlanner{}, &fakeMailbox{}, audit, Options{})
	run := domain.AgentRun{Prompt: "was running"}
	require.NoError(t, st.AgentRuns().Create(context.Background(), &run))

	count, err := service.RecoverInterrupted(context.Background())
	require.NoError(t, err)
	require.Equal(t, int64(1), count)
	stored, err := service.Get(context.Background(), run.ID)
	require.NoError(t, err)
	require.Equal(t, domain.AgentFailed, stored.Status)
	require.Equal(t, []string{"agent.recover"}, audit.actions())
}

func TestServiceListLimits(t *testing.T) {
	service, st := newTestService(t, &fakePlanner{}, &fakeMailbox{}, nil, Options{})
	for i := 0; i < 3; i++ {
		run := domain.AgentRun{Prompt: fmt.Sprintf("run %d", i), CreatedAt: time.Now().Add(time.Duration(i) * time.Second)}
		require.NoError(t, st.AgentRuns().Create(context.Background(), &run))
	}
	runs, err := service.List(context.Background(), 0)
	require.NoError(t, err)
	require.Len(t, runs, 3)
	runs, err = service.List(context.Background(), 1000)
	require.NoError(t, err)
	require.Len(t, runs, 3)
}

func createPlannedRun(t *testing.T, st *store.Store, plan domain.AgentPlan) domain.AgentRun {
	t.Helper()
	run := domain.AgentRun{Prompt: "test request"}
	require.NoError(t, st.AgentRuns().Create(context.Background(), &run))
	require.NoError(t, st.AgentRuns().SetPlan(context.Background(), run.ID, plan))
	run.Plan = plan
	run.Status = domain.AgentPlanned
	return run
}

func makeIDs(prefix string, count int) []string {
	ids := make([]string, count)
	for i := range ids {
		ids[i] = fmt.Sprintf("%s-%d", prefix, i)
	}
	return ids
}

func waitJob(t *testing.T, runner *jobs.Runner, id string) jobs.Job {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		job, ok := runner.Get(id)
		require.True(t, ok)
		if job.Status == jobs.StatusDone || job.Status == jobs.StatusFailed {
			return job
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("job %s did not finish", id)
	return jobs.Job{}
}
