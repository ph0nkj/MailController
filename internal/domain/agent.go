package domain

import "time"

// Agent run statuses.
const (
	AgentPlanning = "planning"
	AgentPlanned  = "planned"
	AgentRunning  = "running"
	AgentDone     = "done"
	AgentFailed   = "failed"
	AgentCanceled = "cancelled"
)

// Agent actions supported by the mailbox executor.
const (
	AgentActionMarkRead   = "mark_read"
	AgentActionMarkUnread = "mark_unread"
	AgentActionArchive    = "archive"
	AgentActionTrash      = "trash"
)

// AgentStep is one mailbox action in a proposed plan.
type AgentStep struct {
	Action string `json:"action"`
	Query  string `json:"query"`
	Reason string `json:"reason,omitempty"`
	Count  int    `json:"count"`
}

// AgentPlan is a reviewed sequence of mailbox actions.
type AgentPlan struct {
	Summary string      `json:"summary"`
	Steps   []AgentStep `json:"steps"`
	Total   int         `json:"total"`
}

// AgentStepResult records what happened to one plan step.
type AgentStepResult struct {
	Action   string `json:"action"`
	Query    string `json:"query"`
	Count    int    `json:"count"`
	Affected int    `json:"affected"`
}

// AgentResult is the stored outcome of a run.
type AgentResult struct {
	Steps    []AgentStepResult `json:"steps,omitempty"`
	Affected int               `json:"affected"`
	Error    string            `json:"error,omitempty"`
}

// AgentRun is one natural-language mailbox automation request.
type AgentRun struct {
	ID        string      `json:"id"`
	Prompt    string      `json:"prompt"`
	Plan      AgentPlan   `json:"plan"`
	Status    string      `json:"status"`
	Result    AgentResult `json:"result"`
	CreatedAt time.Time   `json:"created_at"`
}
