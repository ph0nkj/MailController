package mailer

import (
	"context"
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

type sentMessage struct {
	to      string
	subject string
	text    string
	html    string
	headers map[string]string
}

type fakeSender struct {
	mu        sync.Mutex
	attempts  int
	sends     []sentMessage
	retryable bool
	from      string
	fn        func(ctx context.Context, call int, message sentMessage) error
}

func (f *fakeSender) SendMessage(ctx context.Context, to, subject, textBody, htmlBody string, headers map[string]string) (string, error) {
	message := sentMessage{to: to, subject: subject, text: textBody, html: htmlBody, headers: cloneHeaders(headers)}
	f.mu.Lock()
	f.attempts++
	call := f.attempts
	f.sends = append(f.sends, message)
	fn := f.fn
	f.mu.Unlock()
	if fn != nil {
		if err := fn(ctx, call, message); err != nil {
			return "", err
		}
	}
	return fmt.Sprintf("sent-%d", call), nil
}

func (f *fakeSender) Retryable(error) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.retryable
}

func (f *fakeSender) FromAddress() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.from == "" {
		return "sender@example.com"
	}
	return f.from
}

func cloneHeaders(headers map[string]string) map[string]string {
	if headers == nil {
		return nil
	}
	clone := make(map[string]string, len(headers))
	for name, value := range headers {
		clone[name] = value
	}
	return clone
}

func (f *fakeSender) setFn(fn func(ctx context.Context, call int, message sentMessage) error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.fn = fn
}

func (f *fakeSender) messages() []sentMessage {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]sentMessage(nil), f.sends...)
}

func (f *fakeSender) attemptCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.attempts
}

type fakeAudit struct {
	mu      sync.Mutex
	entries []domain.AuditEntry
}

func (f *fakeAudit) Add(_ context.Context, entry domain.AuditEntry) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.entries = append(f.entries, entry)
	return nil
}

func (f *fakeAudit) list() []domain.AuditEntry {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]domain.AuditEntry(nil), f.entries...)
}

type sleepRecorder struct {
	mu     sync.Mutex
	delays []time.Duration
}

func (s *sleepRecorder) sleep(ctx context.Context, d time.Duration) error {
	s.mu.Lock()
	s.delays = append(s.delays, d)
	s.mu.Unlock()
	return ctx.Err()
}

func (s *sleepRecorder) recorded() []time.Duration {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]time.Duration(nil), s.delays...)
}

type engineFixture struct {
	engine *Engine
	repo   *store.CampaignRepo
	runner *jobs.Runner
	sender *fakeSender
	audit  *fakeAudit
	sleep  *sleepRecorder
}

func newEngineFixture(t *testing.T, opts ...Option) *engineFixture {
	t.Helper()
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "engine.db"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, st.Close()) })

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	runner := jobs.NewRunner(20, 30*time.Second)
	sender := &fakeSender{}
	audit := &fakeAudit{}
	engine, err := NewEngine(logger, st.Campaigns(), func(context.Context) (Sender, error) {
		return sender, nil
	}, runner, audit, opts...)
	require.NoError(t, err)
	sleep := &sleepRecorder{}
	engine.sleep = sleep.sleep
	return &engineFixture{
		engine: engine,
		repo:   st.Campaigns(),
		runner: runner,
		sender: sender,
		audit:  audit,
		sleep:  sleep,
	}
}

func (f *engineFixture) createCampaign(t *testing.T, dailyLimit, rate int, recipients ...string) domain.Campaign {
	t.Helper()
	campaign := domain.Campaign{
		Name:       "Test campaign",
		Subject:    "Hello {{email}}",
		BodyText:   "Text for {{email}}",
		BodyHTML:   "<p>HTML for {{email}}</p>",
		DailyLimit: dailyLimit,
		RatePerMin: rate,
	}
	require.NoError(t, f.repo.Create(context.Background(), &campaign))
	_, err := f.repo.AddRecipients(context.Background(), campaign.ID, recipients)
	require.NoError(t, err)
	return campaign
}

func waitForJob(t *testing.T, runner *jobs.Runner, id string) jobs.Job {
	t.Helper()
	var final jobs.Job
	require.Eventually(t, func() bool {
		job, ok := runner.Get(id)
		if !ok {
			return false
		}
		if job.Status == jobs.StatusDone || job.Status == jobs.StatusFailed {
			final = job
			return true
		}
		return false
	}, 5*time.Second, 5*time.Millisecond)
	return final
}

func requireCampaignStatus(t *testing.T, repo *store.CampaignRepo, id, status string) domain.Campaign {
	t.Helper()
	campaign, err := repo.Get(context.Background(), id)
	require.NoError(t, err)
	require.Equal(t, status, campaign.Status)
	return campaign
}

func TestEngineSendsAllRecipients(t *testing.T) {
	f := newEngineFixture(t)
	recipients := []string{"a@example.com", "b@example.com", "c@example.com", "d@example.com", "e@example.com"}
	campaign := f.createCampaign(t, 100, 60, recipients...)

	jobID, err := f.engine.Start(context.Background(), campaign.ID)
	require.NoError(t, err)
	job := waitForJob(t, f.runner, jobID)
	require.Equal(t, jobs.StatusDone, job.Status, job.Error)
	require.Equal(t, int64(5), job.Total)
	require.Equal(t, int64(5), job.Done)

	requireCampaignStatus(t, f.repo, campaign.ID, domain.CampaignDone)
	stats, err := f.repo.Stats(context.Background(), campaign.ID)
	require.NoError(t, err)
	require.Equal(t, domain.RecipientStats{Total: 5, Sent: 5}, stats)

	sends := f.sender.messages()
	require.Len(t, sends, 5)
	for _, message := range sends {
		require.Equal(t, "Hello "+message.to, message.subject)
		require.Equal(t, "Text for "+message.to, message.text)
		require.Equal(t, "<p>HTML for "+message.to+"</p>", message.html)
	}
	require.Equal(t, "a@example.com", sends[0].to)
	require.Equal(t, "e@example.com", sends[4].to)

	// One throttle pause between every pair of messages.
	delays := f.sleep.recorded()
	require.Len(t, delays, 4)
	for _, delay := range delays {
		require.Equal(t, time.Second, delay)
	}

	entries := f.audit.list()
	require.Len(t, entries, 1)
	require.Equal(t, "campaign.finish", entries[0].Action)
	require.Contains(t, entries[0].Details, `"sent":5`)
	require.NotContains(t, entries[0].Details, "@")
}

func TestEngineAddsUnsubscribeHeaderAndFooter(t *testing.T) {
	f := newEngineFixture(t, WithUnsubscribe("unsub@example.com"))
	campaign := f.createCampaign(t, 100, 60, "a@example.com")

	jobID, err := f.engine.Start(context.Background(), campaign.ID)
	require.NoError(t, err)
	job := waitForJob(t, f.runner, jobID)
	require.Equal(t, jobs.StatusDone, job.Status, job.Error)

	sends := f.sender.messages()
	require.Len(t, sends, 1)
	message := sends[0]
	require.Contains(t, message.text, "Text for a@example.com")
	require.Contains(t, message.text, "To unsubscribe")
	require.Contains(t, message.text, "unsub@example.com")
	require.Contains(t, message.html, "<p>HTML for a@example.com</p>")
	require.Contains(t, message.html, `href="mailto:unsub@example.com?subject=unsubscribe"`)
	require.Equal(t, map[string]string{
		"List-Unsubscribe": "<mailto:unsub@example.com?subject=unsubscribe>",
	}, message.headers)
}

func TestEngineUnsubscribeFallsBackToSenderAddress(t *testing.T) {
	f := newEngineFixture(t, WithUnsubscribe(""))
	f.sender.from = "owner@example.com"
	campaign := f.createCampaign(t, 100, 60, "a@example.com")

	jobID, err := f.engine.Start(context.Background(), campaign.ID)
	require.NoError(t, err)
	require.Equal(t, jobs.StatusDone, waitForJob(t, f.runner, jobID).Status)

	message := f.sender.messages()[0]
	require.Contains(t, message.text, "owner@example.com")
	require.Equal(t, "<mailto:owner@example.com?subject=unsubscribe>", message.headers["List-Unsubscribe"])
}

func TestEngineUnsubscribeDisabledByDefault(t *testing.T) {
	f := newEngineFixture(t)
	campaign := f.createCampaign(t, 100, 60, "a@example.com")

	jobID, err := f.engine.Start(context.Background(), campaign.ID)
	require.NoError(t, err)
	require.Equal(t, jobs.StatusDone, waitForJob(t, f.runner, jobID).Status)

	message := f.sender.messages()[0]
	require.NotContains(t, strings.ToLower(message.text), "unsubscribe")
	require.NotContains(t, strings.ToLower(message.html), "unsubscribe")
	require.Empty(t, message.headers)
}

func TestPrepareMessageHTMLEmptyTextFallback(t *testing.T) {
	engine := &Engine{unsubscribeEnabled: true, unsubscribeEmail: "unsub@example.com"}
	textBody, htmlBody, headers := engine.prepareMessage(&fakeSender{}, "", "<p>Hello</p>")
	require.Contains(t, textBody, htmlOnlyTextBody)
	require.Contains(t, textBody, "unsub@example.com")
	require.Contains(t, htmlBody, "<p>Hello</p>")
	require.NotEmpty(t, headers["List-Unsubscribe"])
}

func TestNewEngineRejectsInvalidUnsubscribeEmail(t *testing.T) {
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "unsubscribe.db"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, st.Close()) })

	_, err = NewEngine(nil, st.Campaigns(), func(context.Context) (Sender, error) {
		return &fakeSender{}, nil
	}, jobs.NewRunner(1, time.Minute), nil, WithUnsubscribe("Alice <alice@example.com>"))
	require.Error(t, err)
}

func TestEngineDailyLimitPauses(t *testing.T) {
	f := newEngineFixture(t)
	campaign := f.createCampaign(t, 2, 60,
		"a@example.com", "b@example.com", "c@example.com", "d@example.com")

	jobID, err := f.engine.Start(context.Background(), campaign.ID)
	require.NoError(t, err)
	job := waitForJob(t, f.runner, jobID)
	require.Equal(t, jobs.StatusDone, job.Status, job.Error)

	requireCampaignStatus(t, f.repo, campaign.ID, domain.CampaignPaused)
	stats, err := f.repo.Stats(context.Background(), campaign.ID)
	require.NoError(t, err)
	require.Equal(t, domain.RecipientStats{Total: 4, Pending: 2, Sent: 2}, stats)
	require.Len(t, f.sender.messages(), 2)

	entries := f.audit.list()
	require.Len(t, entries, 1)
	require.Equal(t, "campaign.pause", entries[0].Action)
	require.Contains(t, entries[0].Details, `"reason":"daily_limit"`)
	require.NotContains(t, entries[0].Details, "@")
}

func TestEngineDailyLimitAlreadyReached(t *testing.T) {
	f := newEngineFixture(t)
	campaign := f.createCampaign(t, 1, 60, "a@example.com", "b@example.com")
	recipients, err := f.repo.PendingRecipients(context.Background(), campaign.ID, 1)
	require.NoError(t, err)
	require.NoError(t, f.repo.MarkRecipient(context.Background(), recipients[0].ID,
		domain.RecipientSent, "", time.Now()))

	jobID, err := f.engine.Start(context.Background(), campaign.ID)
	require.NoError(t, err)
	job := waitForJob(t, f.runner, jobID)
	require.Equal(t, jobs.StatusDone, job.Status, job.Error)

	requireCampaignStatus(t, f.repo, campaign.ID, domain.CampaignPaused)
	require.Empty(t, f.sender.messages())
	require.Contains(t, f.audit.list()[0].Details, `"reason":"daily_limit"`)
}

func TestEngineRetriesTransientFailures(t *testing.T) {
	f := newEngineFixture(t)
	campaign := f.createCampaign(t, 100, 60, "a@example.com")
	f.sender.mu.Lock()
	f.sender.retryable = true
	f.sender.mu.Unlock()
	f.sender.setFn(func(_ context.Context, call int, _ sentMessage) error {
		if call < 3 {
			return errors.New("temporary failure")
		}
		return nil
	})

	jobID, err := f.engine.Start(context.Background(), campaign.ID)
	require.NoError(t, err)
	job := waitForJob(t, f.runner, jobID)
	require.Equal(t, jobs.StatusDone, job.Status, job.Error)

	require.Equal(t, 3, f.sender.attemptCount())
	stats, err := f.repo.Stats(context.Background(), campaign.ID)
	require.NoError(t, err)
	require.Equal(t, domain.RecipientStats{Total: 1, Sent: 1}, stats)
	require.Equal(t, []time.Duration{time.Second, 2 * time.Second}, f.sleep.recorded())
}

func TestEnginePermanentFailureIsRecorded(t *testing.T) {
	f := newEngineFixture(t)
	campaign := f.createCampaign(t, 100, 60, "a@example.com")
	f.sender.setFn(func(context.Context, int, sentMessage) error {
		return errors.New("bad address\r\ninjected")
	})

	jobID, err := f.engine.Start(context.Background(), campaign.ID)
	require.NoError(t, err)
	job := waitForJob(t, f.runner, jobID)
	require.Equal(t, jobs.StatusDone, job.Status, job.Error)
	require.Equal(t, 1, f.sender.attemptCount())

	stats, err := f.repo.Stats(context.Background(), campaign.ID)
	require.NoError(t, err)
	require.Equal(t, domain.RecipientStats{Total: 1, Failed: 1}, stats)

	recipients, _, err := f.repo.ListRecipients(context.Background(), campaign.ID, domain.RecipientFailed, 10, 0)
	require.NoError(t, err)
	require.Len(t, recipients, 1)
	require.Contains(t, recipients[0].Error, "bad address")
	require.NotContains(t, recipients[0].Error, "\r")
	require.NotContains(t, recipients[0].Error, "\n")
}

func TestEngineTruncatesFailures(t *testing.T) {
	f := newEngineFixture(t)
	campaign := f.createCampaign(t, 100, 60, "a@example.com")
	f.sender.setFn(func(context.Context, int, sentMessage) error {
		return errors.New(strings.Repeat("x", maxRecipientErrorLength+100))
	})

	jobID, err := f.engine.Start(context.Background(), campaign.ID)
	require.NoError(t, err)
	waitForJob(t, f.runner, jobID)

	recipients, _, err := f.repo.ListRecipients(context.Background(), campaign.ID, domain.RecipientFailed, 10, 0)
	require.NoError(t, err)
	require.Len(t, recipients, 1)
	require.LessOrEqual(t, len([]rune(recipients[0].Error)), maxRecipientErrorLength+1)
}

func TestEnginePauseAndResume(t *testing.T) {
	f := newEngineFixture(t)
	campaign := f.createCampaign(t, 100, 60,
		"a@example.com", "b@example.com", "c@example.com", "d@example.com")

	firstCall := make(chan struct{})
	f.sender.setFn(func(ctx context.Context, call int, _ sentMessage) error {
		if call == 1 {
			close(firstCall)
			<-ctx.Done()
			return ctx.Err()
		}
		return nil
	})

	jobID, err := f.engine.Start(context.Background(), campaign.ID)
	require.NoError(t, err)
	select {
	case <-firstCall:
	case <-time.After(5 * time.Second):
		t.Fatal("sender was not called")
	}

	pauseCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	require.NoError(t, f.engine.Pause(pauseCtx, campaign.ID))
	waitForJob(t, f.runner, jobID)
	requireCampaignStatus(t, f.repo, campaign.ID, domain.CampaignPaused)

	stats, err := f.repo.Stats(context.Background(), campaign.ID)
	require.NoError(t, err)
	require.Equal(t, domain.RecipientStats{Total: 4, Pending: 4}, stats)

	// Resume with a healthy sender: all four recipients are delivered.
	f.sender.setFn(nil)
	jobID, err = f.engine.Start(context.Background(), campaign.ID)
	require.NoError(t, err)
	job := waitForJob(t, f.runner, jobID)
	require.Equal(t, jobs.StatusDone, job.Status, job.Error)

	requireCampaignStatus(t, f.repo, campaign.ID, domain.CampaignDone)
	stats, err = f.repo.Stats(context.Background(), campaign.ID)
	require.NoError(t, err)
	require.Equal(t, domain.RecipientStats{Total: 4, Sent: 4}, stats)
}

func TestEngineStartValidation(t *testing.T) {
	f := newEngineFixture(t)

	empty := f.createCampaign(t, 10, 60)
	_, err := f.engine.Start(context.Background(), empty.ID)
	require.ErrorIs(t, err, ErrNoRecipients)
	requireCampaignStatus(t, f.repo, empty.ID, domain.CampaignDraft)

	missing, err := f.engine.Start(context.Background(), "missing")
	require.ErrorIs(t, err, store.ErrNotFound)
	require.Empty(t, missing)

	campaign := f.createCampaign(t, 10, 60, "a@example.com")
	require.NoError(t, f.repo.SetStatus(context.Background(), campaign.ID, domain.CampaignDone, time.Now()))
	_, err = f.engine.Start(context.Background(), campaign.ID)
	require.ErrorIs(t, err, ErrInvalidCampaignState)

	f.sender.setFn(func(ctx context.Context, _ int, _ sentMessage) error {
		<-ctx.Done()
		return ctx.Err()
	})
	running := f.createCampaign(t, 10, 60, "b@example.com")
	jobID, err := f.engine.Start(context.Background(), running.ID)
	require.NoError(t, err)
	_, err = f.engine.Start(context.Background(), running.ID)
	require.ErrorIs(t, err, ErrAlreadyRunning)

	pauseCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	require.NoError(t, f.engine.Pause(pauseCtx, running.ID))
	waitForJob(t, f.runner, jobID)
}

func TestEngineStartFailsWhenSenderUnavailable(t *testing.T) {
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "sender.db"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, st.Close()) })
	repo := st.Campaigns()
	campaign := domain.Campaign{Name: "x", Subject: "y", DailyLimit: 10, RatePerMin: 60}
	require.NoError(t, repo.Create(context.Background(), &campaign))
	_, err = repo.AddRecipients(context.Background(), campaign.ID, []string{"a@example.com"})
	require.NoError(t, err)

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	runner := jobs.NewRunner(5, time.Minute)
	engine, err := NewEngine(logger, repo, func(context.Context) (Sender, error) {
		return nil, errors.New("google not connected")
	}, runner, nil)
	require.NoError(t, err)

	_, err = engine.Start(context.Background(), campaign.ID)
	require.Error(t, err)
	requireCampaignStatus(t, repo, campaign.ID, domain.CampaignDraft)
	require.Zero(t, runner.Active())
}

func TestEngineRecoverRunning(t *testing.T) {
	f := newEngineFixture(t)
	campaign := f.createCampaign(t, 10, 60, "a@example.com")
	require.NoError(t, f.repo.SetStatus(context.Background(), campaign.ID, domain.CampaignRunning, time.Now()))

	recovered, err := f.engine.RecoverRunning(context.Background())
	require.NoError(t, err)
	require.Equal(t, 1, recovered)
	requireCampaignStatus(t, f.repo, campaign.ID, domain.CampaignPaused)

	entries := f.audit.list()
	require.Len(t, entries, 1)
	require.Equal(t, "campaign.recover", entries[0].Action)
}

func TestNewEngineValidation(t *testing.T) {
	_, err := NewEngine(nil, nil, nil, nil, nil)
	require.Error(t, err)

	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "validate.db"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, st.Close()) })
	_, err = NewEngine(nil, st.Campaigns(), nil, jobs.NewRunner(1, time.Minute), nil)
	require.Error(t, err)
	_, err = NewEngine(nil, st.Campaigns(), func(context.Context) (Sender, error) { return nil, nil }, nil, nil)
	require.Error(t, err)
}

func TestSendInterval(t *testing.T) {
	require.Equal(t, time.Second, sendInterval(60))
	require.Equal(t, 100*time.Millisecond, sendInterval(600))
	require.Equal(t, minSendInterval, sendInterval(100000))
	require.Equal(t, time.Second, sendInterval(0))
}

func TestTruncateError(t *testing.T) {
	require.Equal(t, "a  b", truncateError(errors.New("a\r\nb")))
	truncated := truncateError(errors.New(strings.Repeat("x", 500)))
	require.Equal(t, maxRecipientErrorLength+3, len(truncated)) // ellipsis is three bytes
}
