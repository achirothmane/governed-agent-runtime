# Agent Server A4

A4 turns the AI-native runtime from an embeddable Go library into a service boundary that clients, UIs, and Dots can call without receiving direct control over execution backends or tool catalogs.

## Service shape

```text
Client / UI / Dots
       |
       | bearer-authenticated REST
       | + replayable SSE
       v
   Agent Server
       |
       +--> RuntimeProvider
       |       |
       |       +--> AgentSpec
       |       +--> ToolCatalog
       |       +--> MCP snapshot binding (A3)
       |       +--> ExecutionBackend
       |
       +--> Store
       |       |
       |       +--> conversations
       |       +--> run metadata
       |       +--> ordered events
       |
       v
 sdk.Runtime
   |       |
   v       v
Temporal  MCP
  A2       A3
```

The HTTP caller never supplies an `ExecutionBackend`, a `ToolCatalog`, or a raw MCP capability list. Those are server-owned through `RuntimeProvider`.

## HTTP surface

All routes except `GET /healthz` require `Authorization: Bearer <token>`.

- `GET /v1/agents/{agentID}`
- `POST /v1/conversations`
- `GET /v1/conversations/{conversationID}`
- `POST /v1/conversations/{conversationID}/runs`
- `GET /v1/runs/{runID}`
- `POST /v1/runs/{runID}/signals/{signal}`
- `POST /v1/runs/{runID}/cancel`
- `GET /v1/conversations/{conversationID}/events` as Server-Sent Events

Request bodies are bounded (1 MiB by default), reject unknown JSON fields, and must contain exactly one JSON value.

## Run identity and retry contract

A run is bound to:

- run ID;
- conversation ID;
- agent ID;
- exact input digest;
- exact `RunRequest` fingerprint;
- therefore the A3 MCP snapshot digest for every required MCP tool.

Reusing a `run_id` for different input, conversation, agent, or execution fingerprint is a conflict.

If a backend start succeeds but server metadata persistence fails, the server returns a retryable 503. Retrying the same bound request is safe when the backend is A2 Temporal because duplicate starts are reconciled by the exact run fingerprint.

Two concurrent requests that race to persist the same run do not emit duplicate `run.started` events: the metadata store chooses one creation winner.

## Event stream

Events are stored per conversation with monotonically increasing sequence numbers. SSE sends each event as:

```text
id: <sequence>
event: <event type>
data: <EventEnvelope JSON>
```

A client can resume with either:

- `?after=<sequence>`; or
- `Last-Event-ID: <sequence>`.

The Store wake-up channel is advisory only. MemoryStore uses process-local notifications; PostgreSQL Store uses periodic wakeups so writes from another server process are also discovered. The stream always rereads ordered persisted events, so wakeups are never treated as event evidence.

A4 emits service-boundary events such as:

- `run.started`
- `run.signaled`
- `run.cancel_requested`

Execution-specific tool/result/completion events can be appended by the runtime/worker path as that event bridge is expanded.

## Capability boundary

### What A4 establishes

- authenticated API access;
- server-owned agent/runtime composition;
- conversation → agent → workspace binding;
- run ID/input/fingerprint idempotency;
- fail-closed rejection of an MCP run with no A3 snapshot digest;
- backend fingerprint verification during inspect/retry;
- ordered replayable event streaming;
- explicit signal and cancellation forwarding to the durable backend.

### What A4 does not claim

- `MemoryStore` is restart durable;
- an SSE connection is itself durable (the persisted cursor/replay contract is the recovery mechanism);
- bearer authentication is a complete multi-tenant authorization system;
- a successful signal means an external business effect occurred;
- a cancellation request proves all external work stopped;
- server metadata persistence and the durable execution backend are one atomic transaction.

A4.1 now provides that restart-durable PostgreSQL implementation without changing the HTTP or runtime contracts. `MemoryStore` remains a local/test reference store.

## Event evidence semantics

When an operation reaches the durable backend but appending its service event fails, the response can still be HTTP 202 with:

```json
{"accepted": true, "event_persisted": false}
```

This is deliberate. The service does not rewrite an accepted backend operation into a false failure merely because secondary event evidence could not be stored.

For run creation, `event_persisted` describes whether this request persisted the `run.started` event. Idempotent retries do not claim that evidence again.

## A4.1 persistence boundary

The PostgreSQL Store persists conversations, immutable run bindings, and the ordered event log. Event sequence allocation is serialized per conversation inside the same transaction that writes the event, so concurrent writers cannot create duplicate or reordered sequence numbers.

The wake-up mechanism intentionally remains weaker than the event log: it periodically wakes SSE readers, which then call `ListEvents(after)`. This makes replay correctness independent of process memory and independent of which Agent Server process wrote the event.

The remaining non-atomic boundary is still explicit: Temporal durable execution and Agent Server metadata persistence are separate systems. A2 duplicate-start reconciliation makes retries safe, but A4.1 does not claim a distributed transaction between PostgreSQL and Temporal.

## Next boundary

A5 connects the first real engine, Data Engine, end-to-end through the Agent Server. The preferred first slice is read-only/profile execution before mutating data operations.
