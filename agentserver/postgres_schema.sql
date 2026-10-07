CREATE TABLE IF NOT EXISTS agent_server_conversations (
    conversation_id TEXT PRIMARY KEY,
    agent_id TEXT NOT NULL,
    workspace_id TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL,
    next_event_sequence BIGINT NOT NULL DEFAULT 0 CHECK (next_event_sequence >= 0),
    UNIQUE (conversation_id, agent_id)
);

CREATE TABLE IF NOT EXISTS agent_server_runs (
    run_id TEXT PRIMARY KEY,
    conversation_id TEXT NOT NULL,
    agent_id TEXT NOT NULL,
    input TEXT NOT NULL,
    input_digest TEXT NOT NULL,
    backend TEXT NOT NULL,
    external_id TEXT,
    fingerprint TEXT NOT NULL,
    state TEXT NOT NULL CHECK (
        state IN ('QUEUED', 'RUNNING', 'WAITING', 'SUCCEEDED', 'FAILED', 'CANCELED', 'UNKNOWN')
    ),
    created_at TIMESTAMPTZ NOT NULL,
    UNIQUE (run_id, conversation_id),
    FOREIGN KEY (conversation_id, agent_id)
        REFERENCES agent_server_conversations (conversation_id, agent_id)
        ON DELETE RESTRICT
);

CREATE INDEX IF NOT EXISTS agent_server_runs_conversation_idx
    ON agent_server_runs (conversation_id, created_at, run_id);

CREATE TABLE IF NOT EXISTS agent_server_events (
    conversation_id TEXT NOT NULL,
    sequence BIGINT NOT NULL CHECK (sequence > 0),
    run_id TEXT NOT NULL,
    event_type TEXT NOT NULL,
    occurred_at TIMESTAMPTZ NOT NULL,
    payload BYTEA,
    PRIMARY KEY (conversation_id, sequence),
    FOREIGN KEY (run_id, conversation_id)
        REFERENCES agent_server_runs (run_id, conversation_id)
        ON DELETE RESTRICT
);

CREATE INDEX IF NOT EXISTS agent_server_events_run_idx
    ON agent_server_events (run_id, sequence);
