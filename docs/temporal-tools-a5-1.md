# A5.1 — Durable Temporal Tool Activities

A5.1 moves read-only MCP invocation from request-scoped execution into a Temporal workflow/activity boundary.

## Execution shape

```text
Agent Server
    |
    | invocation_id + bound tool + arguments
    v
PostgreSQL invocation ledger
    |
    | raw arguments live here for restart recovery
    | digest is bound into Temporal input
    v
Temporal tool workflow
    |
    v
tool activity
    |
    +--> tool.called evidence
    +--> pinned MCP invocation
    +--> COMPLETE / FAILED in ledger
    +--> tool.returned / tool.failed evidence
```

The workflow input contains only the immutable invocation reference and argument digest. Raw tool arguments are deliberately kept out of Temporal workflow history and persisted in the invocation ledger so an activity can recover them after a worker restart.

## Invocation identity

Every durable call requires a caller-stable `invocation_id`.

The stored binding includes:

- invocation ID;
- run ID;
- conversation ID;
- tool name/protocol/endpoint;
- server-admitted read-only flag;
- MCP capability snapshot digest;
- arguments SHA-256 digest.

Reusing the same invocation ID with a different binding fails closed.

The Temporal Workflow ID is derived from the run ID and invocation ID, and workflow reuse is rejected. A lost start acknowledgement reconciles to the already-started workflow.

## Retry semantics

The tool activity has bounded Temporal retry:

- initial interval: 1 second;
- exponential backoff coefficient: 2;
- maximum interval: 30 seconds;
- maximum attempts: 5;
- start-to-close timeout: 2 minutes;
- schedule-to-close timeout: 10 minutes;
- cancellation is propagated to the activity context.

A5.1 permits only server-admitted read-only MCP tools. Therefore an ambiguous transport outcome may be retried without risking a duplicate write effect.

Capability snapshot mismatch/staleness, invalid bindings, and invalid stored payloads are non-retryable.

Mutating tools remain unsupported in A5.1.

## Idempotent result ledger

Before Temporal starts, PostgreSQL stores the raw arguments plus their digest in `agent_server_tool_invocations`.

If the activity completes successfully, the normalized MCP result and its digest are stored as `COMPLETE`. A retried activity first checks the ledger:

```text
COMPLETE
   |
   +--> return stored result
        no second MCP invocation
```

A late failure cannot overwrite a completed result.

## Evidence authority

For the durable path, HTTP is not execution evidence.

The HTTP handler does not emit `tool.called`, `tool.returned`, or `tool.failed`. Those events are emitted by the actual Temporal activity through `ToolActivityEvidence`.

This prevents a client disconnect or request timeout from being misreported as a tool failure while Temporal continues in the background.

Event payloads contain invocation identity and digests, not raw rows/results. The raw payload exists only in the invocation ledger because restart-safe activity retry requires it.

Repeated evidence records are possible if an activity acknowledgement is lost after evidence persistence; consumers must use `invocation_id` as the logical invocation identity rather than treating each event row as a distinct business effect.

## Privacy / storage boundary

A5 deliberately avoided copying raw rows into the event log. A5.1 introduces one intentional raw-payload persistence location: the invocation ledger.

That is required for restart-safe retries without putting raw arguments into Temporal history.

A5.1 does not yet provide:

- payload encryption beyond the PostgreSQL deployment's storage controls;
- per-invocation retention/TTL;
- external blob storage for large arguments;
- payload redaction policies.

Those are explicit production-hardening boundaries, not implied guarantees.

## Falsification

The test suite covers:

- activity result reuse without re-invoking MCP;
- argument-digest tampering rejection;
- retry from FAILED to COMPLETE;
- workflow execution by registered activity name;
- raw arguments absent from Temporal workflow input;
- lost-start reconciliation;
- PostgreSQL invocation recovery after store recreation;
- invocation-ID rebinding rejection;
- completed result cannot be overwritten by a late failure;
- Agent Server requires invocation ID for durable execution;
- HTTP path does not invent durable execution events;
- activity evidence does not copy raw payloads.

## Boundary decision

**KNOWN:** read-only MCP tool calls can be assigned a stable invocation identity, persisted outside Temporal history, executed through a Temporal activity, recovered after process/store restart, retried within bounded policy, and reused from a completed result.

**UNKNOWN / not claimed:** exactly-once remote read observation when an MCP response is lost before the activity records completion. The read may be repeated.

**UNSUPPORTED:** mutating MCP tools through this A5.1 durable retry path.
