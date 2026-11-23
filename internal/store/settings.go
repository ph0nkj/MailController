package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// SettingsRepo stores opaque application settings. Secrets are encrypted by the
// caller before they reach this repository.
type SettingsRepo struct {
	db *sql.DB
}

// Get returns the raw value for key, or ErrNotFound.
func (r *SettingsRepo) Get(ctx context.Context, key string) ([]byte, error) {
	var value []byte
	err := r.db.QueryRowContext(ctx, `SELECT value FROM settings WHERE key = ?`, key).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("store: get setting %q: %w", key, err)
	}
	return value, nil
}

// GetString returns the value for key as a string, or ErrNotFound.
func (r *SettingsRepo) GetString(ctx context.Context, key string) (string, error) {
	value, err := r.Get(ctx, key)
	if err != nil {
		return "", err
	}
	return string(value), nil
}

// Set upserts a setting value.
func (r *SettingsRepo) Set(ctx context.Context, key string, value []byte) error {
	_, err := r.db.ExecContext(ctx,
		`INSERT INTO settings (key, value, updated_at) VALUES (?, ?, ?)
		 ON CONFLICT (key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at`,
		key, value, time.Now().Unix(),
	)
	if err != nil {
		return fmt.Errorf("store: set setting %q: %w", key, err)
	}
	return nil
}

// SetString upserts a setting value from a string.
func (r *SettingsRepo) SetString(ctx context.Context, key, value string) error {
	return r.Set(ctx, key, []byte(value))
}

// Delete removes a setting. Missing keys are ignored.
func (r *SettingsRepo) Delete(ctx context.Context, key string) error {
	if _, err := r.db.ExecContext(ctx, `DELETE FROM settings WHERE key = ?`, key); err != nil {
		return fmt.Errorf("store: delete setting %q: %w", key, err)
	}
	return nil
}
