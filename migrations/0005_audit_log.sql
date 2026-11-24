-- +goose Up
CREATE TABLE audit_log (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    ts           INTEGER NOT NULL,
    actor        TEXT NOT NULL,
    action       TEXT NOT NULL,
    details_json TEXT NOT NULL DEFAULT '{}'
);

CREATE INDEX idx_audit_log_ts ON audit_log (ts);

-- +goose Down
DROP TABLE audit_log;
