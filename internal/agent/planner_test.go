package agent

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/ph0nkj/mailcontroller/internal/domain"
)

type fakeCompleter struct {
	answer    string
	err       error
	system    string
	user      string
	maxTokens int
	calls     int
}

func (c *fakeCompleter) Complete(_ context.Context, system, user string, maxTokens int) (string, error) {
	c.calls++
	c.system = system
	c.user = user
	c.maxTokens = maxTokens
	return c.answer, c.err
}

func TestLLMPlannerBuildsPlan(t *testing.T) {
	completer := &fakeCompleter{answer: "Here is the plan:\n```json\n" + `{
		"summary": "Tidy promotions",
		"steps": [
			{"action": "archive", "query": "category:promotions older_than:30d", "reason": "old promos"},
			{"action": "mark_read", "query": "is:unread", "reason": "unread"}
		]
	}` + "\n```"}
	planner := NewLLMPlanner(completer, 3)
	planner.now = func() time.Time { return time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC) }

	plan, err := planner.Plan(context.Background(), "  tidy my promotions  ")
	require.NoError(t, err)
	require.Equal(t, "Tidy promotions", plan.Summary)
	require.Len(t, plan.Steps, 2)
	require.Equal(t, domain.AgentActionArchive, plan.Steps[0].Action)
	require.Equal(t, "category:promotions older_than:30d", plan.Steps[0].Query)
	require.Equal(t, "old promos", plan.Steps[0].Reason)
	require.Equal(t, 1, completer.calls)
	require.Equal(t, plannerMaxTokens, completer.maxTokens)
	require.Contains(t, completer.system, "mark_read")
	require.Contains(t, completer.system, "trash")
	require.Contains(t, completer.system, "up\nto 3 steps")
	require.Contains(t, completer.user, "Current date: 2026-11-24")
	require.Contains(t, completer.user, "<<<REQUEST>>>\ntidy my promotions\n<<<END_REQUEST>>>")
}

func TestLLMPlannerAcceptsEmptyPlan(t *testing.T) {
	planner := NewLLMPlanner(&fakeCompleter{answer: `{"summary":"","steps":[]}`}, 5)
	plan, err := planner.Plan(context.Background(), "hello")
	require.NoError(t, err)
	require.Empty(t, plan.Steps)
	require.Empty(t, plan.Summary)
}

func TestLLMPlannerParsesBracesInStrings(t *testing.T) {
	raw := `prefix {"summary":"use {braces}","steps":[{"action":"archive","query":"subject:{x}","reason":"a } b"}]} suffix`
	plan, err := parsePlanResponse(raw)
	require.NoError(t, err)
	require.Equal(t, "use {braces}", plan.Summary)
	require.Len(t, plan.Steps, 1)
	require.Equal(t, "subject:{x}", plan.Steps[0].Query)
	require.Equal(t, "a } b", plan.Steps[0].Reason)
}

func TestLLMPlannerErrors(t *testing.T) {
	sentinel := errors.New("provider unavailable")
	planner := NewLLMPlanner(&fakeCompleter{err: sentinel}, 5)
	_, err := planner.Plan(context.Background(), "archive old mail")
	require.ErrorIs(t, err, sentinel)

	for name, answer := range map[string]string{
		"empty":     "",
		"no object": "I cannot help with that.",
		"truncated": `{"summary":"x","steps":[{"action":"archive"`,
		"invalid":   `{"summary": 1, "steps": {}}`,
		"too large": strings.Repeat("x", plannerMaxResponseChars+1),
	} {
		_, err := NewLLMPlanner(&fakeCompleter{answer: answer}, 5).
			Plan(context.Background(), "archive old mail")
		require.ErrorIs(t, err, ErrPlannerResponse, name)
	}

	_, err = NewLLMPlanner(nil, 5).Plan(context.Background(), "archive")
	require.ErrorIs(t, err, ErrPlannerResponse)
	_, err = NewLLMPlanner(&fakeCompleter{answer: `{"steps":[]}`}, 5).Plan(context.Background(), " ")
	require.ErrorIs(t, err, ErrPlannerResponse)
}

func TestNewLLMPlannerDefaults(t *testing.T) {
	planner := NewLLMPlanner(&fakeCompleter{}, 0)
	require.Equal(t, 5, planner.maxSteps)
}
