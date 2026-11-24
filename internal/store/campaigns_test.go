package store

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/ph0nkj/mailcontroller/internal/domain"
)

func createTestCampaign(t *testing.T, repo *CampaignRepo) domain.Campaign {
	t.Helper()
	campaign := domain.Campaign{
		Name:     "January update",
		Subject:  "Hello {{email}}",
		BodyText: "Newsletter body",
	}
	require.NoError(t, repo.Create(context.Background(), &campaign))
	return campaign
}

func TestCampaignCreateAndGet(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	repo := s.Campaigns()

	campaign := createTestCampaign(t, repo)
	require.NotEmpty(t, campaign.ID)
	require.Equal(t, domain.CampaignDraft, campaign.Status)
	require.Equal(t, 500, campaign.DailyLimit)
	require.Equal(t, 60, campaign.RatePerMin)
	require.False(t, campaign.CreatedAt.IsZero())

	stored, err := repo.Get(ctx, campaign.ID)
	require.NoError(t, err)
	require.Equal(t, campaign.Name, stored.Name)
	require.Equal(t, campaign.Subject, stored.Subject)
	require.Equal(t, campaign.CreatedAt.Unix(), stored.CreatedAt.Unix())
	require.True(t, stored.StartedAt.IsZero())
	require.True(t, stored.FinishedAt.IsZero())

	_, err = repo.Get(ctx, "missing")
	require.ErrorIs(t, err, ErrNotFound)

	require.Error(t, repo.Create(ctx, &domain.Campaign{}))
	require.Error(t, repo.Create(ctx, &domain.Campaign{Name: "x"}))
	require.Error(t, repo.Create(ctx, &domain.Campaign{Name: "x", Subject: "y", Status: "weird"}))
}

func TestCampaignListOrderAndUpdate(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	repo := s.Campaigns()

	first := createTestCampaign(t, repo)
	second := domain.Campaign{
		Name: "Second", Subject: "Subject", DailyLimit: 25, RatePerMin: 10,
		CreatedAt: time.Now().Add(time.Minute),
	}
	require.NoError(t, repo.Create(ctx, &second))

	campaigns, err := repo.List(ctx)
	require.NoError(t, err)
	require.Len(t, campaigns, 2)
	require.Equal(t, second.ID, campaigns[0].ID)
	require.Equal(t, first.ID, campaigns[1].ID)

	first.Name = "Renamed"
	first.DailyLimit = 42
	first.RatePerMin = 12
	first.BodyHTML = "<p>Hello</p>"
	require.NoError(t, repo.Update(ctx, first))
	stored, err := repo.Get(ctx, first.ID)
	require.NoError(t, err)
	require.Equal(t, "Renamed", stored.Name)
	require.Equal(t, 42, stored.DailyLimit)
	require.Equal(t, 12, stored.RatePerMin)
	require.Equal(t, "<p>Hello</p>", stored.BodyHTML)

	require.NoError(t, repo.SetStatus(ctx, first.ID, domain.CampaignRunning, time.Now()))
	require.ErrorIs(t, repo.Update(ctx, first), ErrNotDraft)

	missing := first
	missing.ID = "missing"
	require.ErrorIs(t, repo.Update(ctx, missing), ErrNotFound)
}

func TestCampaignStatusTimestamps(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	repo := s.Campaigns()
	campaign := createTestCampaign(t, repo)

	start := time.Unix(1000, 0)
	require.NoError(t, repo.SetStatus(ctx, campaign.ID, domain.CampaignRunning, start))
	stored, err := repo.Get(ctx, campaign.ID)
	require.NoError(t, err)
	require.Equal(t, domain.CampaignRunning, stored.Status)
	require.Equal(t, start.Unix(), stored.StartedAt.Unix())
	require.True(t, stored.FinishedAt.IsZero())

	resume := time.Unix(2000, 0)
	require.NoError(t, repo.SetStatus(ctx, campaign.ID, domain.CampaignPaused, resume))
	stored, _ = repo.Get(ctx, campaign.ID)
	require.Equal(t, domain.CampaignPaused, stored.Status)
	require.Equal(t, start.Unix(), stored.StartedAt.Unix(), "pausing must not move started_at")
	require.True(t, stored.FinishedAt.IsZero())

	require.NoError(t, repo.SetStatus(ctx, campaign.ID, domain.CampaignRunning, resume))
	stored, _ = repo.Get(ctx, campaign.ID)
	require.Equal(t, start.Unix(), stored.StartedAt.Unix(), "resuming must not move started_at")

	finish := time.Unix(3000, 0)
	require.NoError(t, repo.SetStatus(ctx, campaign.ID, domain.CampaignDone, finish))
	stored, _ = repo.Get(ctx, campaign.ID)
	require.Equal(t, finish.Unix(), stored.FinishedAt.Unix())

	require.NoError(t, repo.SetStatus(ctx, campaign.ID, domain.CampaignFailed, finish))
	stored, _ = repo.Get(ctx, campaign.ID)
	require.Equal(t, domain.CampaignFailed, stored.Status)
	require.Equal(t, finish.Unix(), stored.FinishedAt.Unix())

	require.Error(t, repo.SetStatus(ctx, campaign.ID, "weird", finish))
	require.ErrorIs(t, repo.SetStatus(ctx, "missing", domain.CampaignDone, finish), ErrNotFound)
}

func TestAddRecipientsChunkedAndDeduped(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	repo := s.Campaigns()
	campaign := createTestCampaign(t, repo)

	emails := make([]string, 0, 600)
	for i := 0; i < 600; i++ {
		emails = append(emails, fmt.Sprintf("user%d@example.com", i))
	}
	emails = append(emails, " user0@example.com ", "", "user1@example.com")

	inserted, err := repo.AddRecipients(ctx, campaign.ID, emails)
	require.NoError(t, err)
	require.Equal(t, 600, inserted)

	inserted, err = repo.AddRecipients(ctx, campaign.ID, emails[:10])
	require.NoError(t, err)
	require.Zero(t, inserted)

	inserted, err = repo.AddRecipients(ctx, campaign.ID, nil)
	require.NoError(t, err)
	require.Zero(t, inserted)

	stats, err := repo.Stats(ctx, campaign.ID)
	require.NoError(t, err)
	require.Equal(t, 600, stats.Total)
	require.Equal(t, 600, stats.Pending)

	_, err = repo.AddRecipients(ctx, "unknown", []string{"a@example.com"})
	require.Error(t, err, "foreign key violations must not be ignored")
}

func TestListRecipientsPaginationAndFilter(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	repo := s.Campaigns()
	campaign := createTestCampaign(t, repo)

	emails := make([]string, 0, 30)
	for i := 0; i < 30; i++ {
		emails = append(emails, fmt.Sprintf("user%02d@example.com", i))
	}
	_, err := repo.AddRecipients(ctx, campaign.ID, emails)
	require.NoError(t, err)

	page, total, err := repo.ListRecipients(ctx, campaign.ID, "", 10, 0)
	require.NoError(t, err)
	require.Equal(t, 30, total)
	require.Len(t, page, 10)
	require.Equal(t, "user00@example.com", page[0].Email)
	require.Equal(t, domain.RecipientPending, page[0].Status)

	page, total, err = repo.ListRecipients(ctx, campaign.ID, "", 10, 20)
	require.NoError(t, err)
	require.Equal(t, 30, total)
	require.Len(t, page, 10)
	require.Equal(t, "user20@example.com", page[0].Email)

	require.NoError(t, repo.MarkRecipient(ctx, page[0].ID, domain.RecipientFailed, "bounce", time.Time{}))
	page, total, err = repo.ListRecipients(ctx, campaign.ID, domain.RecipientFailed, 10, 0)
	require.NoError(t, err)
	require.Equal(t, 1, total)
	require.Len(t, page, 1)
	require.Equal(t, "bounce", page[0].Error)

	_, _, err = repo.ListRecipients(ctx, campaign.ID, "bogus", 10, 0)
	require.Error(t, err)

	// Limit is clamped, not rejected.
	page, _, err = repo.ListRecipients(ctx, campaign.ID, "", 5000, 0)
	require.NoError(t, err)
	require.Len(t, page, 30)
}

func TestMarkRecipientAndStats(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	repo := s.Campaigns()
	campaign := createTestCampaign(t, repo)
	_, err := repo.AddRecipients(ctx, campaign.ID, []string{"a@example.com", "b@example.com", "c@example.com"})
	require.NoError(t, err)

	pending, err := repo.PendingRecipients(ctx, campaign.ID, 10)
	require.NoError(t, err)
	require.Len(t, pending, 3)
	require.Equal(t, "a@example.com", pending[0].Email)

	sentAt := time.Unix(5000, 0)
	require.NoError(t, repo.MarkRecipient(ctx, pending[0].ID, domain.RecipientSent, "", sentAt))
	require.NoError(t, repo.MarkRecipient(ctx, pending[1].ID, domain.RecipientFailed, "mailbox full", time.Time{}))
	require.NoError(t, repo.MarkRecipient(ctx, pending[2].ID, domain.RecipientSkipped, "duplicate", time.Time{}))

	// Repeated transitions are ignored.
	require.NoError(t, repo.MarkRecipient(ctx, pending[0].ID, domain.RecipientFailed, "late", time.Time{}))

	stats, err := repo.Stats(ctx, campaign.ID)
	require.NoError(t, err)
	require.Equal(t, domain.RecipientStats{Total: 3, Sent: 1, Failed: 1, Skipped: 1}, stats)

	pending, err = repo.PendingRecipients(ctx, campaign.ID, 10)
	require.NoError(t, err)
	require.Empty(t, pending)

	page, _, err := repo.ListRecipients(ctx, campaign.ID, "", 10, 0)
	require.NoError(t, err)
	require.Equal(t, sentAt.Unix(), page[0].SentAt.Unix())

	count, err := repo.CountSentSince(ctx, campaign.ID, time.Unix(4000, 0))
	require.NoError(t, err)
	require.Equal(t, 1, count)
	count, err = repo.CountSentSince(ctx, campaign.ID, time.Unix(6000, 0))
	require.NoError(t, err)
	require.Zero(t, count)

	require.Error(t, repo.MarkRecipient(ctx, pendingID(t, repo, campaign.ID), domain.RecipientPending, "", time.Time{}))
}

func pendingID(t *testing.T, repo *CampaignRepo, campaignID string) int64 {
	t.Helper()
	recipients, err := repo.PendingRecipients(context.Background(), campaignID, 1)
	require.NoError(t, err)
	if len(recipients) == 0 {
		return 1
	}
	return recipients[0].ID
}

func TestDeleteDraftCascadesRecipients(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	repo := s.Campaigns()
	campaign := createTestCampaign(t, repo)
	_, err := repo.AddRecipients(ctx, campaign.ID, []string{"a@example.com", "b@example.com"})
	require.NoError(t, err)

	require.NoError(t, repo.DeleteDraft(ctx, campaign.ID))
	_, err = repo.Get(ctx, campaign.ID)
	require.ErrorIs(t, err, ErrNotFound)
	stats, err := repo.Stats(ctx, campaign.ID)
	require.NoError(t, err)
	require.Zero(t, stats.Total)

	require.ErrorIs(t, repo.DeleteDraft(ctx, campaign.ID), ErrNotFound)

	other := createTestCampaign(t, repo)
	require.NoError(t, repo.SetStatus(ctx, other.ID, domain.CampaignRunning, time.Now()))
	require.ErrorIs(t, repo.DeleteDraft(ctx, other.ID), ErrNotDraft)
}
