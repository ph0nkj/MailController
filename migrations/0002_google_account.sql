-- +goose Up
CREATE TABLE google_account (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    email      TEXT NOT NULL,
    token_enc  BLOB NOT NULL,
    created_at INTEGER NOT NULL
);

-- +goose Down
DROP TABLE google_account;
