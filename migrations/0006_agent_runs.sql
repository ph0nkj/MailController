-- +goose Up
CREATE TABLE agent_runs (
    id          TEXT PRIMARY KEY,
    prompt      TEXT NOT NULL,
    plan_json   TEXT NOT NULL DEFAULT '{}',
    status      TEXT NOT NULL DEFAULT 'planned'
                CHECK (status IN ('planning', 'planned', 'running', 'done', 'failed', 'cancelled')),
    result_json TEXT NOT NULL DEFAULT '{}',
    created_at  INTEGER NOT NULL
);

CREATE INDEX idx_agent_runs_created_at ON agent_runs (created_at);

-- +goose Down
DROP TABLE agent_runs;
