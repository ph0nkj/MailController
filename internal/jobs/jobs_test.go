package jobs

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestStartCompletesJob(t *testing.T) {
	runner := NewRunner(10, time.Minute)
	started := make(chan struct{})
	job := runner.Start("test", 3, func(_ context.Context, progress *Progress) error {
		close(started)
		progress.Report(1)
		progress.Report(2)
		progress.Report(3)
		return nil
	})
	require.NotEmpty(t, job.ID)
	require.Equal(t, "test", job.Type)
	require.Equal(t, int64(3), job.Total)

	<-started
	require.Eventually(t, func() bool {
		current, ok := runner.Get(job.ID)
		return ok && current.Status == StatusDone
	}, 2*time.Second, 5*time.Millisecond)

	current, ok := runner.Get(job.ID)
	require.True(t, ok)
	require.Equal(t, int64(3), current.Done)
	require.False(t, current.StartedAt.IsZero())
	require.False(t, current.FinishedAt.IsZero())
	require.Empty(t, current.Error)
}

func TestJobFailureAndError(t *testing.T) {
	runner := NewRunner(10, time.Minute)
	job := runner.Start("test", 10, func(context.Context, *Progress) error {
		return errors.New("boom")
	})
	require.Eventually(t, func() bool {
		current, ok := runner.Get(job.ID)
		return ok && current.Status == StatusFailed
	}, 2*time.Second, 5*time.Millisecond)
	current, _ := runner.Get(job.ID)
	require.Equal(t, "boom", current.Error)
}

func TestProgressMonotonicAndClamped(t *testing.T) {
	release := make(chan struct{})
	runner := NewRunner(10, time.Minute)
	job := runner.Start("test", 5, func(_ context.Context, progress *Progress) error {
		progress.Report(2)
		<-release
		return nil
	})
	require.Eventually(t, func() bool {
		current, _ := runner.Get(job.ID)
		return current.Done == 2
	}, 2*time.Second, 5*time.Millisecond)
	close(release)
	require.Eventually(t, func() bool {
		current, _ := runner.Get(job.ID)
		return current.Status == StatusDone && current.Done == 5
	}, 2*time.Second, 5*time.Millisecond)
}

func TestProgressIgnoresRegression(t *testing.T) {
	release := make(chan struct{})
	runner := NewRunner(10, time.Minute)
	job := runner.Start("test", 10, func(_ context.Context, progress *Progress) error {
		progress.Report(5)
		<-release
		progress.Report(3)
		progress.Report(7)
		return nil
	})
	require.Eventually(t, func() bool {
		current, _ := runner.Get(job.ID)
		return current.Done == 5
	}, 2*time.Second, 5*time.Millisecond)
	close(release)
	require.Eventually(t, func() bool {
		current, _ := runner.Get(job.ID)
		return current.Status == StatusDone && current.Done == 10
	}, 2*time.Second, 5*time.Millisecond)
}

func TestSetTotal(t *testing.T) {
	release := make(chan struct{})
	runner := NewRunner(10, time.Minute)
	job := runner.Start("test", 0, func(_ context.Context, progress *Progress) error {
		progress.SetTotal(8)
		progress.Report(2)
		<-release
		progress.Report(8)
		return nil
	})
	require.Eventually(t, func() bool {
		current, _ := runner.Get(job.ID)
		return current.Total == 8 && current.Done == 2
	}, 2*time.Second, 5*time.Millisecond)
	close(release)
	require.Eventually(t, func() bool {
		current, _ := runner.Get(job.ID)
		return current.Status == StatusDone && current.Done == 8
	}, 2*time.Second, 5*time.Millisecond)
}

func TestTimeoutCancelsJob(t *testing.T) {
	runner := NewRunner(10, 20*time.Millisecond)
	job := runner.Start("test", 1, func(ctx context.Context, _ *Progress) error {
		<-ctx.Done()
		return ctx.Err()
	})
	require.Eventually(t, func() bool {
		current, ok := runner.Get(job.ID)
		return ok && current.Status == StatusFailed
	}, 2*time.Second, 5*time.Millisecond)
	current, _ := runner.Get(job.ID)
	require.Contains(t, current.Error, "deadline exceeded")
}

func TestGetUnknown(t *testing.T) {
	runner := NewRunner(10, time.Minute)
	_, ok := runner.Get("missing")
	require.False(t, ok)
}

func TestActiveAndPrune(t *testing.T) {
	runner := NewRunner(2, time.Minute)
	first := runner.Start("test", 1, func(context.Context, *Progress) error { return nil })
	require.Eventually(t, func() bool {
		current, _ := runner.Get(first.ID)
		return current.Status == StatusDone
	}, 2*time.Second, 5*time.Millisecond)
	require.Equal(t, 0, runner.Active())

	second := runner.Start("test", 1, func(context.Context, *Progress) error { return nil })
	require.Eventually(t, func() bool {
		current, _ := runner.Get(second.ID)
		return current.Status == StatusDone
	}, 2*time.Second, 5*time.Millisecond)

	// The runner is at its limit; starting a third job prunes one finished job.
	third := runner.Start("test", 1, func(context.Context, *Progress) error { return nil })
	require.Eventually(t, func() bool {
		current, _ := runner.Get(third.ID)
		return current.Status == StatusDone
	}, 2*time.Second, 5*time.Millisecond)

	total := 0
	for _, job := range []Job{first, second, third} {
		if _, ok := runner.Get(job.ID); ok {
			total++
		}
	}
	require.Equal(t, 2, total)
}

func TestNewRunnerDefaults(t *testing.T) {
	runner := NewRunner(0, 0)
	require.Equal(t, 50, runner.limit)
	require.Equal(t, 30*time.Minute, runner.timeout)
}
