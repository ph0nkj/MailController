-- +goose Up
CREATE TABLE recipients (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    campaign_id TEXT NOT NULL REFERENCES campaigns (id) ON DELETE CASCADE,
    email       TEXT NOT NULL,
    status      TEXT NOT NULL DEFAULT 'pending'
                CHECK (status IN ('pending', 'sent', 'failed', 'skipped')),
    error       TEXT NOT NULL DEFAULT '',
    sent_at     INTEGER
);

CREATE INDEX idx_recipients_campaign_status ON recipients (campaign_id, status);
CREATE UNIQUE INDEX idx_recipients_campaign_email ON recipients (campaign_id, email);
CREATE INDEX idx_recipients_sent_at ON recipients (sent_at);

-- +goose Down
DROP TABLE recipients;
