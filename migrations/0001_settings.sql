-- +goose Up
CREATE TABLE settings (
    key        TEXT PRIMARY KEY,
    value      BLOB NOT NULL,
    updated_at INTEGER NOT NULL
);

-- +goose Down
DROP TABLE settings;
