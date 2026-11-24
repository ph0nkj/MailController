package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/ph0nkj/mailcontroller/internal/domain"
)

// AuditRepo stores security-relevant actions.
type AuditRepo struct {
	db *sql.DB
}

// Audit returns the audit-log repository.
func (s *Store) Audit() *AuditRepo { return &AuditRepo{db: s.db} }

// Add appends an audit entry. A zero timestamp is replaced with the current
// time and empty details default to an empty JSON object.
func (r *AuditRepo) Add(ctx context.Context, entry domain.AuditEntry) error {
	if entry.Actor == "" {
		entry.Actor = "admin"
	}
	if entry.Action == "" {
		return errors.New("store: audit action is required")
	}
	if entry.Details == "" {
		entry.Details = "{}"
	}
	ts := entry.TS
	if ts.IsZero() {
		ts = time.Now()
	}
	if _, err := r.db.ExecContext(ctx,
		`INSERT INTO audit_log (ts, actor, action, details_json) VALUES (?, ?, ?, ?)`,
		ts.Unix(), entry.Actor, entry.Action, entry.Details,
	); err != nil {
		return fmt.Errorf("store: add audit entry: %w", err)
	}
	return nil
}

// List returns the most recent audit entries, newest first.
func (r *AuditRepo) List(ctx context.Context, limit int) ([]domain.AuditEntry, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := r.db.QueryContext(ctx,
		`SELECT id, ts, actor, action, details_json FROM audit_log ORDER BY id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("store: list audit entries: %w", err)
	}
	defer func() { _ = rows.Close() }()

	entries := make([]domain.AuditEntry, 0, limit)
	for rows.Next() {
		var (
			entry domain.AuditEntry
			ts    int64
		)
		if err := rows.Scan(&entry.ID, &ts, &entry.Actor, &entry.Action, &entry.Details); err != nil {
			return nil, fmt.Errorf("store: scan audit entry: %w", err)
		}
		entry.TS = time.Unix(ts, 0)
		entries = append(entries, entry)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: iterate audit entries: %w", err)
	}
	return entries, nil
}
