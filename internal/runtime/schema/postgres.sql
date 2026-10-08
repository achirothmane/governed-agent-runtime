CREATE TABLE IF NOT EXISTS agent_runtime_work (
    sequence BIGSERIAL UNIQUE NOT NULL,
    event_id TEXT PRIMARY KEY,
    agent_id TEXT NOT NULL,
    event_kind TEXT NOT NULL,
    event_payload BYTEA,
    event_created_at_ns BIGINT NOT NULL,
    event_digest BYTEA NOT NULL,
    work_state TEXT NOT NULL CHECK (work_state IN ('PENDING', 'LEASED', 'DONE')),
    lifecycle_state TEXT NOT NULL CHECK (
        lifecycle_state IN (
            'CREATED',
            'SLEEPING',
            'WAKING',
            'PLANNING',
            'WAITING_FOR_ADMISSION',
            'EXECUTING',
            'VERIFYING',
            'BLOCKED',
            'REVOKED',
            'UNKNOWN'
        )
    ),
    lifecycle_version BIGINT NOT NULL CHECK (lifecycle_version > 0),
    lifecycle_updated_at_ns BIGINT NOT NULL,
    plan_id TEXT,
    plan_agent_id TEXT,
    plan_event_id TEXT,
    plan_digest BYTEA,
    plan_document BYTEA,
    plan_bound_at_ns BIGINT,
    effect_resolution_document BYTEA,
    lease_owner TEXT,
    lease_epoch BIGINT NOT NULL DEFAULT 0 CHECK (lease_epoch >= 0),
    lease_expires_at_ns BIGINT,
    completed_at_ns BIGINT,
    CHECK (
        work_state <> 'LEASED'
        OR (lease_owner IS NOT NULL AND lease_expires_at_ns IS NOT NULL)
    ),
    CHECK (
        work_state <> 'DONE'
        OR completed_at_ns IS NOT NULL
    ),
    CHECK (
        (plan_id IS NULL
         AND plan_agent_id IS NULL
         AND plan_event_id IS NULL
         AND plan_digest IS NULL
         AND plan_document IS NULL
         AND plan_bound_at_ns IS NULL)
        OR
        (plan_id IS NOT NULL
         AND plan_agent_id IS NOT NULL
         AND plan_event_id IS NOT NULL
         AND plan_digest IS NOT NULL
         AND plan_document IS NOT NULL
         AND plan_bound_at_ns IS NOT NULL)
    )
);

ALTER TABLE agent_runtime_work ADD COLUMN IF NOT EXISTS plan_id TEXT;
ALTER TABLE agent_runtime_work ADD COLUMN IF NOT EXISTS plan_agent_id TEXT;
ALTER TABLE agent_runtime_work ADD COLUMN IF NOT EXISTS plan_event_id TEXT;
ALTER TABLE agent_runtime_work ADD COLUMN IF NOT EXISTS plan_digest BYTEA;
ALTER TABLE agent_runtime_work ADD COLUMN IF NOT EXISTS plan_document BYTEA;
ALTER TABLE agent_runtime_work ADD COLUMN IF NOT EXISTS plan_bound_at_ns BIGINT;
ALTER TABLE agent_runtime_work ADD COLUMN IF NOT EXISTS effect_resolution_document BYTEA;

CREATE INDEX IF NOT EXISTS agent_runtime_work_claim_idx
    ON agent_runtime_work (work_state, lease_expires_at_ns, sequence);


ALTER TABLE agent_runtime_work ADD COLUMN IF NOT EXISTS admission_document BYTEA;
