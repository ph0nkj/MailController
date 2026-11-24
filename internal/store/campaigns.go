package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/ph0nkj/mailcontroller/internal/domain"
)

// ErrNotDraft is returned when a campaign mutation is only allowed for drafts
// but the campaign is in another state.
var ErrNotDraft = errors.New("store: campaign is not a draft")

const (
	// recipientInsertChunk keeps each multi-row insert well below SQLite's
	// bound-parameter limits.
	recipientInsertChunk = 250
	// recipientListLimit caps one recipients page.
	recipientListLimit = 500
)

// CampaignRepo persists campaigns and their recipients.
type CampaignRepo struct {
	db *sql.DB
}

// Campaigns returns the campaign repository.
func (s *Store) Campaigns() *CampaignRepo { return &CampaignRepo{db: s.db} }

// Create inserts a campaign, generating an id and creation time when omitted.
func (r *CampaignRepo) Create(ctx context.Context, campaign *domain.Campaign) error {
	if campaign == nil {
		return errors.New("store: campaign is nil")
	}
	if strings.TrimSpace(campaign.Name) == "" {
		return errors.New("store: campaign name is required")
	}
	if strings.TrimSpace(campaign.Subject) == "" {
		return errors.New("store: campaign subject is required")
	}
	if campaign.ID == "" {
		id, err := newCampaignID()
		if err != nil {
			return err
		}
		campaign.ID = id
	}
	if campaign.Status == "" {
		campaign.Status = domain.CampaignDraft
	}
	if !validCampaignStatus(campaign.Status) {
		return fmt.Errorf("store: invalid campaign status %q", campaign.Status)
	}
	if campaign.DailyLimit <= 0 {
		campaign.DailyLimit = 500
	}
	if campaign.RatePerMin <= 0 {
		campaign.RatePerMin = 60
	}
	if campaign.CreatedAt.IsZero() {
		campaign.CreatedAt = time.Now()
	}
	_, err := r.db.ExecContext(ctx,
		`INSERT INTO campaigns
		 (id, name, subject, body_html, body_text, status, daily_limit, rate_per_min, created_at, started_at, finished_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, NULL, NULL)`,
		campaign.ID, campaign.Name, campaign.Subject, campaign.BodyHTML, campaign.BodyText,
		campaign.Status, campaign.DailyLimit, campaign.RatePerMin, campaign.CreatedAt.Unix(),
	)
	if err != nil {
		return fmt.Errorf("store: create campaign: %w", err)
	}
	return nil
}

// Get returns one campaign or ErrNotFound.
func (r *CampaignRepo) Get(ctx context.Context, id string) (domain.Campaign, error) {
	row := r.db.QueryRowContext(ctx,
		`SELECT id, name, subject, body_html, body_text, status, daily_limit, rate_per_min,
		        created_at, started_at, finished_at
		 FROM campaigns WHERE id = ?`, id)
	campaign, err := scanCampaign(row)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Campaign{}, ErrNotFound
	}
	if err != nil {
		return domain.Campaign{}, fmt.Errorf("store: get campaign: %w", err)
	}
	return campaign, nil
}

// List returns every campaign, newest first.
func (r *CampaignRepo) List(ctx context.Context) ([]domain.Campaign, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT id, name, subject, body_html, body_text, status, daily_limit, rate_per_min,
		        created_at, started_at, finished_at
		 FROM campaigns ORDER BY created_at DESC, id DESC`)
	if err != nil {
		return nil, fmt.Errorf("store: list campaigns: %w", err)
	}
	defer func() { _ = rows.Close() }()

	campaigns := make([]domain.Campaign, 0, 16)
	for rows.Next() {
		campaign, scanErr := scanCampaign(rows)
		if scanErr != nil {
			return nil, fmt.Errorf("store: scan campaign: %w", scanErr)
		}
		campaigns = append(campaigns, campaign)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: iterate campaigns: %w", err)
	}
	return campaigns, nil
}

// Update changes the editable fields of a draft campaign.
func (r *CampaignRepo) Update(ctx context.Context, campaign domain.Campaign) error {
	result, err := r.db.ExecContext(ctx,
		`UPDATE campaigns
		 SET name = ?, subject = ?, body_html = ?, body_text = ?, daily_limit = ?, rate_per_min = ?
		 WHERE id = ? AND status = ?`,
		campaign.Name, campaign.Subject, campaign.BodyHTML, campaign.BodyText,
		campaign.DailyLimit, campaign.RatePerMin, campaign.ID, domain.CampaignDraft,
	)
	if err != nil {
		return fmt.Errorf("store: update campaign: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("store: update campaign rows: %w", err)
	}
	if affected == 0 {
		return r.mutationError(ctx, campaign.ID)
	}
	return nil
}

// SetStatus updates a campaign status and maintains start/finish timestamps.
func (r *CampaignRepo) SetStatus(ctx context.Context, id, status string, at time.Time) error {
	if !validCampaignStatus(status) {
		return fmt.Errorf("store: invalid campaign status %q", status)
	}
	if at.IsZero() {
		at = time.Now()
	}
	result, err := r.db.ExecContext(ctx,
		`UPDATE campaigns
		 SET status = ?,
		     started_at = CASE WHEN ? = ? AND started_at IS NULL THEN ? ELSE started_at END,
		     finished_at = CASE WHEN ? IN (?, ?) THEN ? ELSE finished_at END
		 WHERE id = ?`,
		status,
		status, domain.CampaignRunning, at.Unix(),
		status, domain.CampaignDone, domain.CampaignFailed, at.Unix(),
		id,
	)
	if err != nil {
		return fmt.Errorf("store: set campaign status: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("store: set campaign status rows: %w", err)
	}
	if affected == 0 {
		return ErrNotFound
	}
	return nil
}

// DeleteDraft removes a draft campaign and its recipients.
func (r *CampaignRepo) DeleteDraft(ctx context.Context, id string) error {
	result, err := r.db.ExecContext(ctx,
		`DELETE FROM campaigns WHERE id = ? AND status = ?`, id, domain.CampaignDraft)
	if err != nil {
		return fmt.Errorf("store: delete campaign: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("store: delete campaign rows: %w", err)
	}
	if affected == 0 {
		return r.mutationError(ctx, id)
	}
	return nil
}

// AddRecipients inserts addresses in chunked statements inside one
// transaction. Existing addresses are ignored, so the return value counts only
// newly added rows.
func (r *CampaignRepo) AddRecipients(ctx context.Context, campaignID string, emails []string) (int, error) {
	unique := make([]string, 0, len(emails))
	for _, email := range emails {
		if trimmed := strings.TrimSpace(email); trimmed != "" {
			unique = append(unique, trimmed)
		}
	}
	if len(unique) == 0 {
		return 0, nil
	}

	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("store: begin recipients insert: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	inserted := 0
	for start := 0; start < len(unique); start += recipientInsertChunk {
		end := start + recipientInsertChunk
		if end > len(unique) {
			end = len(unique)
		}
		chunk := unique[start:end]

		var builder strings.Builder
		builder.WriteString(`INSERT INTO recipients (campaign_id, email) VALUES `)
		args := make([]any, 0, len(chunk)*2)
		for i, email := range chunk {
			if i > 0 {
				builder.WriteString(", ")
			}
			builder.WriteString("(?, ?)")
			args = append(args, campaignID, email)
		}
		builder.WriteString(` ON CONFLICT (campaign_id, email) DO NOTHING`)

		result, execErr := tx.ExecContext(ctx, builder.String(), args...)
		if execErr != nil {
			return 0, fmt.Errorf("store: insert recipients: %w", execErr)
		}
		affected, rowsErr := result.RowsAffected()
		if rowsErr != nil {
			return 0, fmt.Errorf("store: recipients rows affected: %w", rowsErr)
		}
		inserted += int(affected)
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("store: commit recipients insert: %w", err)
	}
	return inserted, nil
}

// ListRecipients returns one page of recipients and the filtered total. An
// empty status matches every status.
func (r *CampaignRepo) ListRecipients(ctx context.Context, campaignID, status string, limit, offset int) ([]domain.Recipient, int, error) {
	if status != "" && !validRecipientStatus(status) {
		return nil, 0, fmt.Errorf("store: invalid recipient status %q", status)
	}
	if limit <= 0 || limit > recipientListLimit {
		limit = 50
	}
	if offset < 0 {
		offset = 0
	}
	var (
		total int
		rows  *sql.Rows
		err   error
	)
	if status == "" {
		err = r.db.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM recipients WHERE campaign_id = ?`, campaignID).Scan(&total)
		if err == nil {
			rows, err = r.db.QueryContext(ctx,
				`SELECT id, campaign_id, email, status, error, sent_at
				 FROM recipients WHERE campaign_id = ?
				 ORDER BY id LIMIT ? OFFSET ?`, campaignID, limit, offset)
		}
	} else {
		err = r.db.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM recipients WHERE campaign_id = ? AND status = ?`,
			campaignID, status).Scan(&total)
		if err == nil {
			rows, err = r.db.QueryContext(ctx,
				`SELECT id, campaign_id, email, status, error, sent_at
				 FROM recipients WHERE campaign_id = ? AND status = ?
				 ORDER BY id LIMIT ? OFFSET ?`, campaignID, status, limit, offset)
		}
	}
	if err != nil {
		return nil, 0, fmt.Errorf("store: list recipients: %w", err)
	}
	defer func() { _ = rows.Close() }()

	recipients := make([]domain.Recipient, 0, limit)
	for rows.Next() {
		recipient, scanErr := scanRecipient(rows)
		if scanErr != nil {
			return nil, 0, fmt.Errorf("store: scan recipient: %w", scanErr)
		}
		recipients = append(recipients, recipient)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, fmt.Errorf("store: iterate recipients: %w", err)
	}
	return recipients, total, nil
}

// PendingRecipients returns the oldest pending recipients for sending.
func (r *CampaignRepo) PendingRecipients(ctx context.Context, campaignID string, limit int) ([]domain.Recipient, error) {
	if limit <= 0 || limit > recipientListLimit {
		limit = 100
	}
	rows, err := r.db.QueryContext(ctx,
		`SELECT id, campaign_id, email, status, error, sent_at
		 FROM recipients WHERE campaign_id = ? AND status = ?
		 ORDER BY id LIMIT ?`, campaignID, domain.RecipientPending, limit)
	if err != nil {
		return nil, fmt.Errorf("store: list pending recipients: %w", err)
	}
	defer func() { _ = rows.Close() }()

	recipients := make([]domain.Recipient, 0, limit)
	for rows.Next() {
		recipient, scanErr := scanRecipient(rows)
		if scanErr != nil {
			return nil, fmt.Errorf("store: scan pending recipient: %w", scanErr)
		}
		recipients = append(recipients, recipient)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: iterate pending recipients: %w", err)
	}
	return recipients, nil
}

// MarkRecipient transitions a pending recipient to a final state. It is a
// no-op when the recipient is no longer pending.
func (r *CampaignRepo) MarkRecipient(ctx context.Context, id int64, status, errMessage string, sentAt time.Time) error {
	if !validRecipientStatus(status) || status == domain.RecipientPending {
		return fmt.Errorf("store: invalid recipient transition %q", status)
	}
	if status == domain.RecipientSent && sentAt.IsZero() {
		sentAt = time.Now()
	}
	var sentAtValue any
	if !sentAt.IsZero() {
		sentAtValue = sentAt.Unix()
	}
	_, err := r.db.ExecContext(ctx,
		`UPDATE recipients SET status = ?, error = ?, sent_at = ?
		 WHERE id = ? AND status = ?`,
		status, errMessage, sentAtValue, id, domain.RecipientPending,
	)
	if err != nil {
		return fmt.Errorf("store: mark recipient: %w", err)
	}
	return nil
}

// Stats returns delivery counters for a campaign.
func (r *CampaignRepo) Stats(ctx context.Context, campaignID string) (domain.RecipientStats, error) {
	var stats domain.RecipientStats
	err := r.db.QueryRowContext(ctx,
		`SELECT COUNT(*),
		        COALESCE(SUM(CASE WHEN status = ? THEN 1 ELSE 0 END), 0),
		        COALESCE(SUM(CASE WHEN status = ? THEN 1 ELSE 0 END), 0),
		        COALESCE(SUM(CASE WHEN status = ? THEN 1 ELSE 0 END), 0),
		        COALESCE(SUM(CASE WHEN status = ? THEN 1 ELSE 0 END), 0)
		 FROM recipients WHERE campaign_id = ?`,
		domain.RecipientPending, domain.RecipientSent, domain.RecipientFailed,
		domain.RecipientSkipped, campaignID,
	).Scan(&stats.Total, &stats.Pending, &stats.Sent, &stats.Failed, &stats.Skipped)
	if err != nil {
		return domain.RecipientStats{}, fmt.Errorf("store: campaign stats: %w", err)
	}
	return stats, nil
}

// CountSentSince counts successful sends at or after since.
func (r *CampaignRepo) CountSentSince(ctx context.Context, campaignID string, since time.Time) (int, error) {
	var count int
	err := r.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM recipients
		 WHERE campaign_id = ? AND status = ? AND sent_at >= ?`,
		campaignID, domain.RecipientSent, since.Unix(),
	).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("store: count sent recipients: %w", err)
	}
	return count, nil
}

func (r *CampaignRepo) mutationError(ctx context.Context, id string) error {
	var status string
	err := r.db.QueryRowContext(ctx, `SELECT status FROM campaigns WHERE id = ?`, id).Scan(&status)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return ErrNotFound
	case err != nil:
		return fmt.Errorf("store: check campaign state: %w", err)
	case status != domain.CampaignDraft:
		return ErrNotDraft
	default:
		return ErrNotFound
	}
}

type rowScanner interface {
	Scan(dest ...any) error
}

func scanCampaign(row rowScanner) (domain.Campaign, error) {
	var (
		campaign   domain.Campaign
		createdAt  int64
		startedAt  sql.NullInt64
		finishedAt sql.NullInt64
	)
	if err := row.Scan(
		&campaign.ID, &campaign.Name, &campaign.Subject, &campaign.BodyHTML, &campaign.BodyText,
		&campaign.Status, &campaign.DailyLimit, &campaign.RatePerMin, &createdAt,
		&startedAt, &finishedAt,
	); err != nil {
		return domain.Campaign{}, err
	}
	campaign.CreatedAt = time.Unix(createdAt, 0)
	if startedAt.Valid {
		campaign.StartedAt = time.Unix(startedAt.Int64, 0)
	}
	if finishedAt.Valid {
		campaign.FinishedAt = time.Unix(finishedAt.Int64, 0)
	}
	return campaign, nil
}

func scanRecipient(row rowScanner) (domain.Recipient, error) {
	var (
		recipient domain.Recipient
		sentAt    sql.NullInt64
	)
	if err := row.Scan(&recipient.ID, &recipient.CampaignID, &recipient.Email,
		&recipient.Status, &recipient.Error, &sentAt); err != nil {
		return domain.Recipient{}, err
	}
	if sentAt.Valid {
		recipient.SentAt = time.Unix(sentAt.Int64, 0)
	}
	return recipient, nil
}

func validCampaignStatus(status string) bool {
	switch status {
	case domain.CampaignDraft, domain.CampaignRunning, domain.CampaignPaused,
		domain.CampaignDone, domain.CampaignFailed:
		return true
	default:
		return false
	}
}

func validRecipientStatus(status string) bool {
	switch status {
	case domain.RecipientPending, domain.RecipientSent, domain.RecipientFailed, domain.RecipientSkipped:
		return true
	default:
		return false
	}
}

func newCampaignID() (string, error) {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("store: generate campaign id: %w", err)
	}
	return hex.EncodeToString(buf), nil
}
