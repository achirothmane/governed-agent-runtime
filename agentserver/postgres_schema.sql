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


CREATE TABLE IF NOT EXISTS agent_server_tool_invocations (
    invocation_id TEXT PRIMARY KEY,
    run_id TEXT NOT NULL,
    conversation_id TEXT NOT NULL,
    tool_name TEXT NOT NULL,
    tool_protocol TEXT NOT NULL,
    tool_endpoint TEXT NOT NULL,
    tool_read_only BOOLEAN NOT NULL,
    snapshot_digest TEXT NOT NULL,
    arguments_json JSONB NOT NULL,
    arguments_digest TEXT NOT NULL,
    state TEXT NOT NULL CHECK (state IN ('PREPARED', 'FAILED', 'COMPLETE')),
    result_json JSONB,
    result_digest TEXT,
    last_error TEXT,
    created_at TIMESTAMPTZ NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL,
    FOREIGN KEY (run_id, conversation_id)
        REFERENCES agent_server_runs (run_id, conversation_id)
        ON DELETE RESTRICT
);

CREATE INDEX IF NOT EXISTS agent_server_tool_invocations_run_idx
    ON agent_server_tool_invocations (run_id, created_at, invocation_id);


CREATE TABLE IF NOT EXISTS agent_server_agent_executions (
    run_id TEXT PRIMARY KEY,
    conversation_id TEXT NOT NULL,
    run_fingerprint TEXT NOT NULL,
    mission_digest TEXT NOT NULL,
    max_steps INTEGER NOT NULL CHECK (max_steps BETWEEN 1 AND 64),
    created_at TIMESTAMPTZ NOT NULL,
    FOREIGN KEY (run_id, conversation_id)
        REFERENCES agent_server_runs (run_id, conversation_id)
        ON DELETE RESTRICT
);

CREATE TABLE IF NOT EXISTS agent_server_reasoning_steps (
    run_id TEXT NOT NULL,
    step INTEGER NOT NULL CHECK (step > 0),
    decision_kind TEXT NOT NULL CHECK (decision_kind IN ('TOOL', 'FINISH', 'ASK', 'FAIL')),
    tool_name TEXT,
    message TEXT,
    invocation_id TEXT,
    decision_digest TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (run_id, step),
    FOREIGN KEY (run_id)
        REFERENCES agent_server_agent_executions (run_id)
        ON DELETE RESTRICT
);

CREATE INDEX IF NOT EXISTS agent_server_reasoning_steps_run_idx
    ON agent_server_reasoning_steps (run_id, step);


CREATE TABLE IF NOT EXISTS portfolio_dot_decisions (
    decision_id TEXT PRIMARY KEY,
    snapshot_digest TEXT NOT NULL,
    work_item_id TEXT NOT NULL,
    decision_kind TEXT NOT NULL CHECK (
        decision_kind IN ('PROPOSE_NEXT_GATE', 'ASK_HUMAN', 'REFUSE')
    ),
    requested_authority TEXT NOT NULL,
    requested_action TEXT,
    rationale TEXT NOT NULL,
    decision_digest TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL
);

CREATE INDEX IF NOT EXISTS portfolio_dot_decisions_snapshot_idx
    ON portfolio_dot_decisions (snapshot_digest, created_at, decision_id);
