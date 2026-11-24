package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/ph0nkj/mailcontroller/internal/domain"
)

func openTestStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(context.Background(), filepath.Join(t.TempDir(), "test.db"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, s.Close()) })
	return s
}

func TestOpenAppliesMigrations(t *testing.T) {
	s := openTestStore(t)
	require.NoError(t, s.Ping(context.Background()))

	for _, table := range []string{
		"settings", "google_account", "campaigns", "recipients", "audit_log", "agent_runs",
	} {
		var name string
		err := s.db.QueryRowContext(context.Background(),
			`SELECT name FROM sqlite_master WHERE type = 'table' AND name = ?`, table).Scan(&name)
		require.NoError(t, err, "table %s should exist", table)
		require.Equal(t, table, name)
	}
}

func TestSettingsRoundTrip(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	repo := s.Settings()

	_, err := repo.Get(ctx, "missing")
	require.ErrorIs(t, err, ErrNotFound)

	require.NoError(t, repo.SetString(ctx, "provider", "openai"))
	value, err := repo.GetString(ctx, "provider")
	require.NoError(t, err)
	require.Equal(t, "openai", value)

	require.NoError(t, repo.Set(ctx, "provider", []byte("anthropic")))
	value, err = repo.GetString(ctx, "provider")
	require.NoError(t, err)
	require.Equal(t, "anthropic", value)

	require.NoError(t, repo.Delete(ctx, "provider"))
	_, err = repo.Get(ctx, "provider")
	require.ErrorIs(t, err, ErrNotFound)

	require.NoError(t, repo.Delete(ctx, "provider"))
}

func TestOpenRejectsEmptyPath(t *testing.T) {
	_, err := Open(context.Background(), "")
	require.Error(t, err)
}

func TestDSNForPath(t *testing.T) {
	require.Contains(t, dsnForPath(":memory:"), "file::memory:")
	require.Contains(t, dsnForPath("/tmp/a b.db"), "file:")
	require.Equal(t, "file:/custom?x=1", dsnForPath("file:/custom?x=1"))
	require.True(t, isMemoryPath(":memory:"))
	require.True(t, isMemoryPath("file::memory:?cache=shared"))
	require.False(t, isMemoryPath("/tmp/x.db"))
}

func TestAuditRoundTrip(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	repo := s.Audit()

	require.Error(t, repo.Add(ctx, domain.AuditEntry{}))

	require.NoError(t, repo.Add(ctx, domain.AuditEntry{Action: "mail.clean.execute"}))
	require.NoError(t, repo.Add(ctx, domain.AuditEntry{
		Actor:   "tester",
		Action:  "mail.clean.finish",
		Details: `{"affected":10}`,
		TS:      time.Unix(1000, 0),
	}))

	entries, err := repo.List(ctx, 10)
	require.NoError(t, err)
	require.Len(t, entries, 2)
	require.Equal(t, "mail.clean.finish", entries[0].Action)
	require.Equal(t, "tester", entries[0].Actor)
	require.Equal(t, `{"affected":10}`, entries[0].Details)
	require.Equal(t, int64(1000), entries[0].TS.Unix())
	require.NotZero(t, entries[0].ID)

	require.Equal(t, "mail.clean.execute", entries[1].Action)
	require.Equal(t, "admin", entries[1].Actor)
	require.Equal(t, "{}", entries[1].Details)

	limited, err := repo.List(ctx, 1)
	require.NoError(t, err)
	require.Len(t, limited, 1)
	require.Equal(t, entries[0].ID, limited[0].ID)
}
