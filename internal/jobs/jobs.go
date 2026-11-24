// Package jobs provides a small in-process background job runner with
// progress tracking. Jobs are kept in memory and pruned automatically.
package jobs

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"sync"
	"time"
)

// Status describes the lifecycle state of a job.
type Status string

const (
	// StatusQueued means the job is created but has not started yet.
	StatusQueued Status = "queued"
	// StatusRunning means the job function is executing.
	StatusRunning Status = "running"
	// StatusDone means the job finished successfully.
	StatusDone Status = "done"
	// StatusFailed means the job returned an error.
	StatusFailed Status = "failed"
)

// Job is a snapshot of a background job's state.
type Job struct {
	ID         string    `json:"id"`
	Type       string    `json:"type"`
	Status     Status    `json:"status"`
	Total      int64     `json:"total"`
	Done       int64     `json:"done"`
	Error      string    `json:"error,omitempty"`
	CreatedAt  time.Time `json:"created_at"`
	StartedAt  time.Time `json:"started_at,omitempty"`
	FinishedAt time.Time `json:"finished_at,omitempty"`
}

// Progress is passed to a running job so it can update its own counters.
type Progress struct {
	runner *Runner
	id     string
}

// Report sets the absolute number of completed work units. Values lower than
// the current one are ignored.
func (p *Progress) Report(done int64) {
	p.runner.updateProgress(p.id, done, false)
}

// SetTotal updates the total number of work units, for jobs that can only
// discover their size once they start.
func (p *Progress) SetTotal(total int64) {
	p.runner.updateProgress(p.id, total, true)
}

// JobFunc performs the background work. It should call progress methods
// regularly so the UI can poll meaningful values.
type JobFunc func(ctx context.Context, progress *Progress) error

// Runner starts and tracks background jobs.
type Runner struct {
	mu      sync.Mutex
	jobs    map[string]*Job
	limit   int
	timeout time.Duration
	now     func() time.Time
}

// NewRunner creates a runner that keeps at most limit jobs and aborts jobs
// after timeout.
func NewRunner(limit int, timeout time.Duration) *Runner {
	if limit <= 0 {
		limit = 50
	}
	if timeout <= 0 {
		timeout = 30 * time.Minute
	}
	return &Runner{
		jobs:    make(map[string]*Job),
		limit:   limit,
		timeout: timeout,
		now:     time.Now,
	}
}

// Start creates a job, runs it in the background and returns its initial
// snapshot.
func (r *Runner) Start(kind string, total int64, fn JobFunc) Job {
	id, err := newID()
	if err != nil {
		id = hex.EncodeToString([]byte(time.Now().Format("150405.000000")))
	}
	job := &Job{
		ID:        id,
		Type:      kind,
		Status:    StatusQueued,
		Total:     total,
		CreatedAt: r.now(),
	}
	r.mu.Lock()
	r.pruneLocked()
	r.jobs[id] = job
	initial := snapshot(job)
	r.mu.Unlock()

	go r.run(job, fn)
	return initial
}

func (r *Runner) run(job *Job, fn JobFunc) {
	r.mu.Lock()
	job.Status = StatusRunning
	job.StartedAt = r.now()
	jobID := job.ID
	r.mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), r.timeout)
	defer cancel()

	progress := &Progress{runner: r, id: jobID}
	err := fn(ctx, progress)

	r.mu.Lock()
	current, ok := r.jobs[jobID]
	if ok {
		current.FinishedAt = r.now()
		if err != nil {
			current.Status = StatusFailed
			current.Error = err.Error()
		} else {
			current.Status = StatusDone
			if current.Total > 0 {
				current.Done = current.Total
			}
		}
	}
	r.mu.Unlock()
}

func (r *Runner) updateProgress(id string, value int64, total bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	job, ok := r.jobs[id]
	if !ok {
		return
	}
	if total {
		if value >= 0 {
			job.Total = value
			if job.Done > value {
				job.Done = value
			}
		}
		return
	}
	if value < job.Done {
		return
	}
	if job.Total > 0 && value > job.Total {
		value = job.Total
	}
	job.Done = value
}

// Get returns a snapshot of the job with the given id.
func (r *Runner) Get(id string) (Job, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	job, ok := r.jobs[id]
	if !ok {
		return Job{}, false
	}
	return snapshot(job), true
}

// Active returns the number of jobs that are not finished yet.
func (r *Runner) Active() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	count := 0
	for _, job := range r.jobs {
		if job.Status == StatusQueued || job.Status == StatusRunning {
			count++
		}
	}
	return count
}

func (r *Runner) pruneLocked() {
	if len(r.jobs) < r.limit {
		return
	}
	var oldestID string
	var oldest time.Time
	found := false
	for id, job := range r.jobs {
		if job.Status == StatusQueued || job.Status == StatusRunning {
			continue
		}
		if !found || job.CreatedAt.Before(oldest) {
			oldestID, oldest, found = id, job.CreatedAt, true
		}
	}
	if found {
		delete(r.jobs, oldestID)
	}
}

func snapshot(job *Job) Job {
	return *job
}

func newID() (string, error) {
	buf := make([]byte, 12)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}

// ErrNotFound is returned when a job id is unknown.
var ErrNotFound = errors.New("jobs: job not found")
