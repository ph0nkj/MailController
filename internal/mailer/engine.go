package mailer

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"log/slog"
	"net/mail"
	"strings"
	"sync"
	"time"

	"github.com/ph0nkj/mailcontroller/internal/domain"
	"github.com/ph0nkj/mailcontroller/internal/jobs"
)

const (
	// pendingBatchSize is the number of recipients fetched per store round trip.
	pendingBatchSize = 50
	// maxSendAttempts is the number of tries for a transient send failure.
	maxSendAttempts = 3
	// retryBaseDelay is doubled after every failed attempt.
	retryBaseDelay = time.Second
	// minSendInterval floors the throttle for very high rate limits.
	minSendInterval = 50 * time.Millisecond
	// dailyWindow is the rolling window used for the daily limit.
	dailyWindow = 24 * time.Hour
	// maxRecipientErrorLength bounds stored provider errors.
	maxRecipientErrorLength = 300
	// auditTimeout bounds audit writes from background work.
	auditTimeout = 5 * time.Second
)

var (
	// ErrAlreadyRunning is returned when a campaign job is already active.
	ErrAlreadyRunning = errors.New("mailer: campaign is already running")
	// ErrInvalidCampaignState is returned when a campaign cannot be started.
	ErrInvalidCampaignState = errors.New("mailer: campaign cannot be started in its current state")
	// ErrNoRecipients is returned when a campaign has nothing left to send.
	ErrNoRecipients = errors.New("mailer: campaign has no pending recipients")
)

// CampaignStore is the persistence surface required by the engine.
type CampaignStore interface {
	Get(ctx context.Context, id string) (domain.Campaign, error)
	List(ctx context.Context) ([]domain.Campaign, error)
	SetStatus(ctx context.Context, id, status string, at time.Time) error
	Stats(ctx context.Context, campaignID string) (domain.RecipientStats, error)
	PendingRecipients(ctx context.Context, campaignID string, limit int) ([]domain.Recipient, error)
	MarkRecipient(ctx context.Context, id int64, status, errMessage string, sentAt time.Time) error
	CountSentSince(ctx context.Context, campaignID string, since time.Time) (int, error)
}

// Sender delivers one rendered message and returns the provider message id.
type Sender interface {
	SendMessage(ctx context.Context, to, subject, textBody, htmlBody string, headers map[string]string) (string, error)
	Retryable(err error) bool
	// FromAddress returns the connected mailbox address used as the default
	// unsubscribe contact.
	FromAddress() string
}

// SenderFactory returns a sender bound to the connected account. It is called
// once per campaign run so connection problems fail before any mail is sent.
type SenderFactory func(ctx context.Context) (Sender, error)

// AuditStore records security-relevant actions without message content.
type AuditStore interface {
	Add(ctx context.Context, entry domain.AuditEntry) error
}

// Engine runs campaign delivery jobs.
type Engine struct {
	log       *slog.Logger
	store     CampaignStore
	newSender SenderFactory
	jobs      *jobs.Runner
	audit     AuditStore
	now       func() time.Time
	sleep     func(ctx context.Context, d time.Duration) error

	unsubscribeEnabled bool
	unsubscribeEmail   string
	maxRatePerMin      int
	maxDailyLimit      int

	mu     sync.Mutex
	active map[string]*runHandle
}

// Option customises campaign delivery.
type Option func(*Engine)

// WithUnsubscribe enables a List-Unsubscribe header and a matching footer on
// every campaign message. An empty address uses the connected mailbox.
func WithUnsubscribe(email string) Option {
	return func(e *Engine) {
		e.unsubscribeEnabled = true
		e.unsubscribeEmail = strings.TrimSpace(email)
	}
}

// WithSendCaps limits every campaign to at most ratePerMin messages per minute
// and dailyLimit messages per rolling 24 hours. Non-positive values disable a
// cap.
func WithSendCaps(ratePerMin, dailyLimit int) Option {
	return func(e *Engine) {
		if ratePerMin > 0 {
			e.maxRatePerMin = ratePerMin
		}
		if dailyLimit > 0 {
			e.maxDailyLimit = dailyLimit
		}
	}
}

type runHandle struct {
	cancel context.CancelFunc
	done   chan struct{}
}

// NewEngine creates a campaign engine. The runner and sender factory are
// required; the audit store is optional.
func NewEngine(
	log *slog.Logger,
	store CampaignStore,
	newSender SenderFactory,
	runner *jobs.Runner,
	audit AuditStore,
	opts ...Option,
) (*Engine, error) {
	if store == nil {
		return nil, errors.New("mailer: campaign store is required")
	}
	if newSender == nil {
		return nil, errors.New("mailer: sender factory is required")
	}
	if runner == nil {
		return nil, errors.New("mailer: job runner is required")
	}
	if log == nil {
		log = slog.Default()
	}
	engine := &Engine{
		log:       log,
		store:     store,
		newSender: newSender,
		jobs:      runner,
		audit:     audit,
		now:       time.Now,
		sleep:     sleepContext,
		active:    make(map[string]*runHandle),
	}
	for _, opt := range opts {
		if opt != nil {
			opt(engine)
		}
	}
	if engine.unsubscribeEmail != "" {
		if err := validatePlainAddress(engine.unsubscribeEmail); err != nil {
			return nil, fmt.Errorf("mailer: invalid unsubscribe email: %w", err)
		}
	}
	return engine, nil
}

// Start validates a campaign, marks it running and launches a delivery job. It
// returns the job id for progress polling.
func (e *Engine) Start(ctx context.Context, campaignID string) (string, error) {
	campaign, err := e.store.Get(ctx, campaignID)
	if err != nil {
		return "", err
	}
	e.mu.Lock()
	_, alreadyRunning := e.active[campaignID]
	e.mu.Unlock()
	if alreadyRunning {
		return "", ErrAlreadyRunning
	}
	if campaign.Status != domain.CampaignDraft && campaign.Status != domain.CampaignPaused {
		return "", fmt.Errorf("%w: status is %s", ErrInvalidCampaignState, campaign.Status)
	}
	stats, err := e.store.Stats(ctx, campaignID)
	if err != nil {
		return "", err
	}
	if stats.Pending == 0 {
		return "", ErrNoRecipients
	}

	e.mu.Lock()
	if _, exists := e.active[campaignID]; exists {
		e.mu.Unlock()
		return "", ErrAlreadyRunning
	}
	runCtx, cancel := context.WithCancel(context.Background())
	handle := &runHandle{cancel: cancel, done: make(chan struct{})}
	e.active[campaignID] = handle
	e.mu.Unlock()

	failStart := func(startErr error) (string, error) {
		e.unregister(campaignID)
		close(handle.done)
		cancel()
		return "", startErr
	}

	sender, err := e.newSender(ctx)
	if err != nil {
		return failStart(err)
	}
	if err := e.store.SetStatus(ctx, campaignID, domain.CampaignRunning, e.now()); err != nil {
		return failStart(err)
	}

	job := e.jobs.Start("campaign:"+campaignID, int64(stats.Pending),
		func(jobCtx context.Context, progress *jobs.Progress) error {
			defer close(handle.done)
			defer e.unregister(campaignID)
			defer cancel()

			ctx, stop := mergeContext(jobCtx, runCtx)
			defer stop()
			return e.run(ctx, campaignID, sender, progress)
		},
	)
	e.log.Info("campaign started", "campaign_id", campaignID, "pending", stats.Pending, "job_id", job.ID)
	return job.ID, nil
}

// Pause stops an active campaign job and leaves the campaign paused. It is
// idempotent: pausing an already stopped campaign only reconciles the status.
func (e *Engine) Pause(ctx context.Context, campaignID string) error {
	e.mu.Lock()
	handle := e.active[campaignID]
	e.mu.Unlock()
	if handle != nil {
		handle.cancel()
		select {
		case <-handle.done:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	campaign, err := e.store.Get(ctx, campaignID)
	if err != nil {
		return err
	}
	if campaign.Status == domain.CampaignRunning {
		if err := e.store.SetStatus(ctx, campaignID, domain.CampaignPaused, e.now()); err != nil {
			return err
		}
		e.auditEvent(ctx, domain.AuditEntry{
			Action:  "campaign.pause",
			Details: detailsJSON(map[string]any{"campaign_id": campaignID, "reason": "manual"}),
		})
	}
	return nil
}

// RecoverRunning pauses campaigns left in the running state by a previous
// process. It returns the number of campaigns reconciled.
func (e *Engine) RecoverRunning(ctx context.Context) (int, error) {
	campaigns, err := e.store.List(ctx)
	if err != nil {
		return 0, err
	}
	recovered := 0
	for _, campaign := range campaigns {
		if campaign.Status != domain.CampaignRunning {
			continue
		}
		if err := e.store.SetStatus(ctx, campaign.ID, domain.CampaignPaused, e.now()); err != nil {
			return recovered, err
		}
		e.auditEvent(ctx, domain.AuditEntry{
			Action:  "campaign.recover",
			Details: detailsJSON(map[string]any{"campaign_id": campaign.ID}),
		})
		recovered++
	}
	if recovered > 0 {
		e.log.Info("paused campaigns left running by a previous process", "campaigns", recovered)
	}
	return recovered, nil
}

func (e *Engine) run(ctx context.Context, campaignID string, sender Sender, progress *jobs.Progress) error {
	dbCtx := context.WithoutCancel(ctx)
	campaign, err := e.store.Get(dbCtx, campaignID)
	if err != nil {
		return e.failRun(campaignID, fmt.Errorf("load campaign: %w", err))
	}
	stats, err := e.store.Stats(dbCtx, campaignID)
	if err != nil {
		return e.failRun(campaignID, fmt.Errorf("load campaign stats: %w", err))
	}

	dailyLimit, ratePerMin := e.effectiveLimits(campaign)
	interval := sendInterval(ratePerMin)
	processed := 0
	runSent := 0
	runFailed := 0

	for {
		if ctx.Err() != nil {
			return e.interrupted(campaignID, nil)
		}
		pending, fetchErr := e.store.PendingRecipients(dbCtx, campaignID, pendingBatchSize)
		if fetchErr != nil {
			return e.failRun(campaignID, fmt.Errorf("load pending recipients: %w", fetchErr))
		}
		if len(pending) == 0 {
			break
		}
		for _, recipient := range pending {
			if ctx.Err() != nil {
				return e.interrupted(campaignID, map[string]any{"run_sent": runSent, "run_failed": runFailed})
			}

			sentToday, countErr := e.store.CountSentSince(dbCtx, campaignID, e.now().Add(-dailyWindow))
			if countErr != nil {
				return e.failRun(campaignID, fmt.Errorf("count daily sends: %w", countErr))
			}
			if sentToday >= dailyLimit {
				e.pauseRun(campaignID, "daily_limit", map[string]any{
					"sent_today":  sentToday,
					"daily_limit": dailyLimit,
				})
				return nil
			}

			sendErr := e.sendWithRetries(ctx, sender, &campaign, recipient.Email)
			switch {
			case sendErr == nil:
				if err := e.store.MarkRecipient(dbCtx, recipient.ID, domain.RecipientSent, "", e.now()); err != nil {
					return e.failRun(campaignID, fmt.Errorf("mark recipient sent: %w", err))
				}
				runSent++
			case ctx.Err() != nil:
				return e.interrupted(campaignID, map[string]any{"run_sent": runSent, "run_failed": runFailed})
			default:
				message := truncateError(sendErr)
				if err := e.store.MarkRecipient(dbCtx, recipient.ID, domain.RecipientFailed, message, time.Time{}); err != nil {
					return e.failRun(campaignID, fmt.Errorf("mark recipient failed: %w", err))
				}
				runFailed++
				e.log.Warn("campaign recipient failed",
					"campaign_id", campaignID,
					"recipient_id", recipient.ID,
					"error", message,
				)
			}

			processed++
			progress.Report(int64(processed))
			if processed < stats.Pending {
				if err := e.sleep(ctx, interval); err != nil {
					return e.interrupted(campaignID, map[string]any{"run_sent": runSent, "run_failed": runFailed})
				}
			}
		}
	}

	finalStats, err := e.store.Stats(dbCtx, campaignID)
	if err != nil {
		return e.failRun(campaignID, fmt.Errorf("load final stats: %w", err))
	}
	if err := e.store.SetStatus(dbCtx, campaignID, domain.CampaignDone, e.now()); err != nil {
		return e.failRun(campaignID, fmt.Errorf("mark campaign done: %w", err))
	}
	e.auditEvent(dbCtx, domain.AuditEntry{
		Action: "campaign.finish",
		Details: detailsJSON(map[string]any{
			"campaign_id": campaignID,
			"ok":          true,
			"sent":        finalStats.Sent,
			"failed":      finalStats.Failed,
			"skipped":     finalStats.Skipped,
			"total":       finalStats.Total,
			"run_sent":    runSent,
			"run_failed":  runFailed,
		}),
	})
	e.log.Info("campaign finished",
		"campaign_id", campaignID,
		"sent", finalStats.Sent,
		"failed", finalStats.Failed,
		"total", finalStats.Total,
	)
	return nil
}

// interrupted pauses a campaign after its context was cancelled. Pausing is a
// normal outcome, so the job is reported as done rather than failed.
func (e *Engine) interrupted(campaignID string, extra map[string]any) error {
	e.pauseRun(campaignID, "interrupted", extra)
	return nil
}

func (e *Engine) sendWithRetries(ctx context.Context, sender Sender, campaign *domain.Campaign, email string) error {
	subject := Render(campaign.Subject, email)
	textBody, htmlBody, headers := e.prepareMessage(sender,
		Render(campaign.BodyText, email),
		Render(campaign.BodyHTML, email),
	)

	var lastErr error
	for attempt := 1; attempt <= maxSendAttempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		_, lastErr = sender.SendMessage(ctx, email, subject, textBody, htmlBody, headers)
		if lastErr == nil {
			return nil
		}
		if !sender.Retryable(lastErr) || attempt == maxSendAttempts {
			break
		}
		backoff := retryBaseDelay * time.Duration(1<<(attempt-1))
		if err := e.sleep(ctx, backoff); err != nil {
			return err
		}
	}
	return lastErr
}

// htmlOnlyTextBody mirrors the fallback used by the MIME builder when a
// campaign has HTML content but no text body.
const htmlOnlyTextBody = "This message contains HTML content. Please use an HTML-capable mail client."

// prepareMessage appends the unsubscribe footer to both bodies and returns the
// List-Unsubscribe header. The configured address wins; otherwise the
// connected mailbox is used so unsubscribe works without extra configuration.
func (e *Engine) prepareMessage(sender Sender, textBody, htmlBody string) (string, string, map[string]string) {
	if !e.unsubscribeEnabled {
		return textBody, htmlBody, nil
	}
	address := strings.TrimSpace(e.unsubscribeEmail)
	if address == "" {
		address = strings.TrimSpace(sender.FromAddress())
	}
	if address == "" || validatePlainAddress(address) != nil {
		return textBody, htmlBody, nil
	}
	if strings.TrimSpace(textBody) == "" && strings.TrimSpace(htmlBody) == "" {
		return textBody, htmlBody, nil
	}
	if strings.TrimSpace(textBody) == "" {
		textBody = htmlOnlyTextBody
	}
	textBody += "\n\n---\nTo unsubscribe, reply to this message or email " + address + ".\n"
	if strings.TrimSpace(htmlBody) != "" {
		mailto := "mailto:" + address + "?subject=unsubscribe"
		htmlBody += "\n<hr style=\"border:none;border-top:1px solid #d7e1ef;margin:24px 0\">\n" +
			"<p style=\"color:#5a6b82;font-size:13px;line-height:1.5\">" +
			"To unsubscribe, reply to this message or email <a href=\"" +
			html.EscapeString(mailto) + "\">" + html.EscapeString(address) + "</a>.</p>\n"
	}
	return textBody, htmlBody, map[string]string{
		"List-Unsubscribe": "<mailto:" + address + "?subject=unsubscribe>",
	}
}

func validatePlainAddress(value string) error {
	parsed, err := mail.ParseAddress(value)
	if err != nil {
		return err
	}
	if parsed.Name != "" || !strings.EqualFold(parsed.Address, value) {
		return errors.New("address must be a plain mailbox")
	}
	return nil
}

func (e *Engine) pauseRun(campaignID, reason string, extra map[string]any) {
	ctx, cancel := context.WithTimeout(context.Background(), auditTimeout)
	defer cancel()
	if err := e.store.SetStatus(ctx, campaignID, domain.CampaignPaused, e.now()); err != nil {
		e.log.Warn("could not pause campaign", "campaign_id", campaignID, "error", err)
	}
	details := map[string]any{"campaign_id": campaignID, "reason": reason}
	for key, value := range extra {
		details[key] = value
	}
	e.auditEvent(ctx, domain.AuditEntry{Action: "campaign.pause", Details: detailsJSON(details)})
	e.log.Info("campaign paused", "campaign_id", campaignID, "reason", reason)
}

func (e *Engine) failRun(campaignID string, runErr error) error {
	ctx, cancel := context.WithTimeout(context.Background(), auditTimeout)
	defer cancel()
	if err := e.store.SetStatus(ctx, campaignID, domain.CampaignFailed, e.now()); err != nil {
		e.log.Warn("could not mark campaign failed", "campaign_id", campaignID, "error", err)
	}
	e.auditEvent(ctx, domain.AuditEntry{
		Action: "campaign.finish",
		Details: detailsJSON(map[string]any{
			"campaign_id": campaignID,
			"ok":          false,
			"error":       truncateError(runErr),
		}),
	})
	e.log.Error("campaign failed", "campaign_id", campaignID, "error", runErr)
	return runErr
}

func (e *Engine) unregister(campaignID string) {
	e.mu.Lock()
	delete(e.active, campaignID)
	e.mu.Unlock()
}

func (e *Engine) auditEvent(ctx context.Context, entry domain.AuditEntry) {
	if e.audit == nil {
		return
	}
	if entry.Actor == "" {
		entry.Actor = "admin"
	}
	auditCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), auditTimeout)
	defer cancel()
	if err := e.audit.Add(auditCtx, entry); err != nil {
		e.log.Warn("write campaign audit entry", "action", entry.Action, "error", err)
	}
}

// effectiveLimits combines campaign settings with the global caps.
func (e *Engine) effectiveLimits(campaign domain.Campaign) (dailyLimit, ratePerMin int) {
	dailyLimit = campaign.DailyLimit
	if e.maxDailyLimit > 0 && (dailyLimit <= 0 || e.maxDailyLimit < dailyLimit) {
		dailyLimit = e.maxDailyLimit
	}
	ratePerMin = campaign.RatePerMin
	if e.maxRatePerMin > 0 && (ratePerMin <= 0 || e.maxRatePerMin < ratePerMin) {
		ratePerMin = e.maxRatePerMin
	}
	return dailyLimit, ratePerMin
}

func sendInterval(ratePerMin int) time.Duration {
	if ratePerMin <= 0 {
		ratePerMin = 60
	}
	interval := time.Minute / time.Duration(ratePerMin)
	if interval < minSendInterval {
		interval = minSendInterval
	}
	return interval
}

func truncateError(err error) string {
	message := err.Error()
	message = replaceControlCharacters(message)
	if len(message) > maxRecipientErrorLength {
		message = message[:maxRecipientErrorLength] + "…"
	}
	return message
}

func replaceControlCharacters(value string) string {
	out := []rune(value)
	for i, r := range out {
		if r < 0x20 || r == 0x7f {
			out[i] = ' '
		}
	}
	return string(out)
}

func detailsJSON(value map[string]any) string {
	raw, err := json.Marshal(value)
	if err != nil {
		return "{}"
	}
	return string(raw)
}

// mergeContext cancels the returned context when either parent is done. The
// returned stop function releases the callback on the secondary context.
func mergeContext(primary, secondary context.Context) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(primary)
	stop := context.AfterFunc(secondary, cancel)
	return ctx, func() {
		stop()
		cancel()
	}
}

func sleepContext(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
