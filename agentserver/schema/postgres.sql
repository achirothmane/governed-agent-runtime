CREATE TABLE IF NOT EXISTS agent_server_conversations (
    conversation_id TEXT PRIMARY KEY,
    agent_id TEXT NOT NULL,
    workspace_id TEXT NOT NULL,
    created_at_ns BIGINT NOT NULL
);

CREATE TABLE IF NOT EXISTS agent_server_runs (
    run_id TEXT PRIMARY KEY,
    conversation_id TEXT NOT NULL REFERENCES agent_server_conversations(conversation_id),
    agent_id TEXT NOT NULL,
    input_text TEXT NOT NULL,
    input_digest TEXT NOT NULL,
    handle_json JSONB NOT NULL,
    fingerprint TEXT NOT NULL,
    created_at_ns BIGINT NOT NULL,
    CHECK (fingerprint <> '')
);

CREATE INDEX IF NOT EXISTS agent_server_runs_conversation_idx
    ON agent_server_runs (conversation_id, created_at_ns, run_id);

CREATE TABLE IF NOT EXISTS agent_server_event_cursors (
    conversation_id TEXT PRIMARY KEY REFERENCES agent_server_conversations(conversation_id) ON DELETE CASCADE,
    last_sequence BIGINT NOT NULL CHECK (last_sequence >= 0)
);

CREATE TABLE IF NOT EXISTS agent_server_events (
    conversation_id TEXT NOT NULL REFERENCES agent_server_conversations(conversation_id) ON DELETE CASCADE,
    sequence BIGINT NOT NULL CHECK (sequence > 0),
    event_type TEXT NOT NULL,
    run_id TEXT NULL REFERENCES agent_server_runs(run_id),
    occurred_at_ns BIGINT NOT NULL,
    payload_json JSONB NOT NULL,
    PRIMARY KEY (conversation_id, sequence)
);

CREATE INDEX IF NOT EXISTS agent_server_events_run_idx
    ON agent_server_events (run_id)
    WHERE run_id IS NOT NULL;
