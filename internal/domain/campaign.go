package domain

import "time"

// Campaign status values. They match the CHECK constraint in the campaigns
// table and the values exposed by the HTTP API.
const (
	CampaignDraft   = "draft"
	CampaignRunning = "running"
	CampaignPaused  = "paused"
	CampaignDone    = "done"
	CampaignFailed  = "failed"
)

// Recipient status values. They match the CHECK constraint in the recipients
// table.
const (
	RecipientPending = "pending"
	RecipientSent    = "sent"
	RecipientFailed  = "failed"
	RecipientSkipped = "skipped"
)

// Campaign is one bulk-mailing campaign. BodyHTML and BodyText may contain the
// placeholder {{email}}, which is replaced for every recipient.
type Campaign struct {
	ID         string
	Name       string
	Subject    string
	BodyHTML   string
	BodyText   string
	Status     string
	DailyLimit int
	RatePerMin int
	CreatedAt  time.Time
	StartedAt  time.Time
	FinishedAt time.Time
}

// Recipient is one address in a campaign with its delivery state.
type Recipient struct {
	ID         int64
	CampaignID string
	Email      string
	Status     string
	Error      string
	SentAt     time.Time
}

// RecipientStats aggregates delivery counters for a campaign.
type RecipientStats struct {
	Total   int
	Pending int
	Sent    int
	Failed  int
	Skipped int
}
