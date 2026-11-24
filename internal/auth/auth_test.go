package auth

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/ph0nkj/mailcontroller/internal/store"
)

func newTestService(t *testing.T) (*Service, *store.SettingsRepo) {
	t.Helper()
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "auth.db"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, st.Close()) })
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	svc, err := New(context.Background(), st.Settings(), "correct-horse", false, log)
	require.NoError(t, err)
	return svc, st.Settings()
}

func TestNewHashesPasswordOnce(t *testing.T) {
	svc, settings := newTestService(t)
	require.NotEmpty(t, svc.hash)
	require.NotEqual(t, "correct-horse", string(svc.hash))

	stored, err := settings.GetString(context.Background(), SettingPasswordHash)
	require.NoError(t, err)
	require.NotEqual(t, "correct-horse", stored)

	// Recreating the service with a different env password must keep the hash.
	svc2, err := New(context.Background(), settings, "another-password", false, slog.Default())
	require.NoError(t, err)
	_, err = svc2.Login(context.Background(), "correct-horse", "127.0.0.1")
	require.NoError(t, err)
	_, err = svc2.Login(context.Background(), "another-password", "127.0.0.2")
	require.ErrorIs(t, err, ErrInvalidCredentials)
}

func TestNewRejectsShortPasswordOnFirstStart(t *testing.T) {
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "auth.db"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, st.Close()) })

	_, err = New(context.Background(), st.Settings(), "short", false, slog.Default())
	require.Error(t, err)
}

func TestLoginAndSessionLifecycle(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()

	_, err := svc.Login(ctx, "wrong-password", "10.0.0.1")
	require.ErrorIs(t, err, ErrInvalidCredentials)

	session, err := svc.Login(ctx, "correct-horse", "10.0.0.1")
	require.NoError(t, err)
	require.NotEmpty(t, session.Token)
	require.NotEmpty(t, session.CSRF)
	require.True(t, session.ExpiresAt.After(time.Now()))

	loaded, ok := svc.Session(session.Token)
	require.True(t, ok)
	require.Equal(t, session.CSRF, loaded.CSRF)
	require.NoError(t, ValidateCSRF(loaded, session.CSRF))
	require.ErrorIs(t, ValidateCSRF(loaded, "nope"), ErrCSRF)
	require.ErrorIs(t, ValidateCSRF(loaded, ""), ErrCSRF)

	_, ok = svc.Session("unknown")
	require.False(t, ok)

	svc.Logout(session.Token)
	_, ok = svc.Session(session.Token)
	require.False(t, ok)
	svc.Logout("already-gone")
}

func TestSessionExpires(t *testing.T) {
	svc, _ := newTestService(t)
	now := time.Now()
	svc.now = func() time.Time { return now }

	session, err := svc.Login(context.Background(), "correct-horse", "127.0.0.1")
	require.NoError(t, err)

	now = now.Add(SessionTTL + time.Minute)
	_, ok := svc.Session(session.Token)
	require.False(t, ok)
}

func TestLoginRateLimit(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()
	now := time.Now()
	svc.now = func() time.Time { return now }

	for i := 0; i < maxLoginAttempts; i++ {
		_, err := svc.Login(ctx, "wrong", "192.0.2.10")
		require.ErrorIs(t, err, ErrInvalidCredentials)
	}
	_, err := svc.Login(ctx, "correct-horse", "192.0.2.10")
	require.ErrorIs(t, err, ErrRateLimited)

	// Another IP is unaffected.
	_, err = svc.Login(ctx, "correct-horse", "192.0.2.11")
	require.NoError(t, err)

	// Window expiry unlocks the original IP.
	now = now.Add(attemptWindow + time.Second)
	_, err = svc.Login(ctx, "correct-horse", "192.0.2.10")
	require.NoError(t, err)
}

func TestNormalizeIP(t *testing.T) {
	require.Equal(t, "127.0.0.1", normalizeIP("127.0.0.1:1234"))
	require.Equal(t, "example", normalizeIP("example"))
	require.Equal(t, "2001:db8::1", normalizeIP("[2001:db8::1]:443"))
}
