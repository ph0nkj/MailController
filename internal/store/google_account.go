package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/ph0nkj/mailcontroller/internal/domain"
)

// GoogleAccountRepo persists the single connected Google account.
type GoogleAccountRepo struct {
	db *sql.DB
}

// GoogleAccount returns the Google account repository.
func (s *Store) GoogleAccount() *GoogleAccountRepo { return &GoogleAccountRepo{db: s.db} }

// Get returns the connected account or ErrNotFound.
func (r *GoogleAccountRepo) Get(ctx context.Context) (domain.GoogleAccount, error) {
	var (
		account domain.GoogleAccount
		created int64
	)
	err := r.db.QueryRowContext(ctx,
		`SELECT email, token_enc, created_at FROM google_account ORDER BY id LIMIT 1`,
	).Scan(&account.Email, &account.TokenEnc, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.GoogleAccount{}, ErrNotFound
	}
	if err != nil {
		return domain.GoogleAccount{}, fmt.Errorf("store: get google account: %w", err)
	}
	account.CreatedAt = time.Unix(created, 0)
	return account, nil
}

// Save replaces the stored Google account.
func (r *GoogleAccountRepo) Save(ctx context.Context, account domain.GoogleAccount) error {
	if account.Email == "" {
		return errors.New("store: google account email is empty")
	}
	if len(account.TokenEnc) == 0 {
		return errors.New("store: google account token is empty")
	}
	createdAt := account.CreatedAt
	if createdAt.IsZero() {
		createdAt = time.Now()
	}
	_, err := r.db.ExecContext(ctx,
		`INSERT INTO google_account (id, email, token_enc, created_at) VALUES (1, ?, ?, ?)
		 ON CONFLICT (id) DO UPDATE SET email = excluded.email, token_enc = excluded.token_enc`,
		account.Email, account.TokenEnc, createdAt.Unix(),
	)
	if err != nil {
		return fmt.Errorf("store: save google account: %w", err)
	}
	return nil
}

// Delete removes the stored Google account. Missing accounts are ignored.
func (r *GoogleAccountRepo) Delete(ctx context.Context) error {
	if _, err := r.db.ExecContext(ctx, `DELETE FROM google_account`); err != nil {
		return fmt.Errorf("store: delete google account: %w", err)
	}
	return nil
}
