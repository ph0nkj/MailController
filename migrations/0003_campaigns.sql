-- +goose Up
CREATE TABLE campaigns (
    id           TEXT PRIMARY KEY,
    name         TEXT NOT NULL,
    subject      TEXT NOT NULL,
    body_html    TEXT NOT NULL DEFAULT '',
    body_text    TEXT NOT NULL DEFAULT '',
    status       TEXT NOT NULL DEFAULT 'draft'
                 CHECK (status IN ('draft', 'running', 'paused', 'done', 'failed')),
    daily_limit  INTEGER NOT NULL DEFAULT 500,
    rate_per_min INTEGER NOT NULL DEFAULT 60,
    created_at   INTEGER NOT NULL,
    started_at   INTEGER,
    finished_at  INTEGER
);

CREATE INDEX idx_campaigns_status ON campaigns (status);

-- +goose Down
DROP TABLE campaigns;
