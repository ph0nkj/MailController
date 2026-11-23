package store

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
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
