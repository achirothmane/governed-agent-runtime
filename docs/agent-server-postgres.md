# Agent Server PostgreSQL Store A4.1

A4.1 makes Agent Server metadata and event replay restart-durable without moving durable execution out of A2 Temporal.

## Ownership

```text
Temporal (A2)               PostgreSQL Agent Server Store (A4.1)
---------------------       -------------------------------------
workflow execution          conversation binding
timers / retries            run metadata + fingerprint
signals / cancellation      ordered event evidence
workflow state              SSE replay cursor history
```

These are intentionally separate authorities. PostgreSQL does not claim that an execution succeeded; Temporal does not replace the server's conversation/event history.

## Tables

- `agent_server_conversations`
- `agent_server_runs`
- `agent_server_event_cursors`
- `agent_server_events`

Run handles are stored as JSONB together with an indexed relational binding: run ID, conversation ID, agent ID, input digest, fingerprint, and creation time.

## Event ordering

Each conversation owns one cursor row. Event append allocates sequence numbers with a single atomic PostgreSQL upsert:

```text
conversation cursor N
        |
        | INSERT ... ON CONFLICT DO UPDATE
        v
conversation cursor N+1
        |
        v
event (conversation, N+1)
```

Cursor allocation and event insertion occur in the same SQL transaction. A rolled-back append therefore does not expose a phantom committed event.

The primary key `(conversation_id, sequence)` is the replay identity.

## Restart recovery

A new `PostgresStore` instance using the same database can recover:

- conversation → agent → workspace binding;
- run → conversation → input digest → runtime fingerprint binding;
- ordered event history;
- SSE replay after `Last-Event-ID` / `?after=`.

This is the A4.1 durability claim tested in CI.

## Watch boundary

Persisted events are durable. The in-process `Watch` channel is only a low-latency wakeup optimization for SSE connections attached to the same service process.

A process restart can lose a wakeup, but cannot lose the persisted event. On reconnect the client resumes from its cursor and the server rereads PostgreSQL.

A4.1 does **not** yet claim cross-process live fan-out between multiple Agent Server replicas. That later capability can use PostgreSQL LISTEN/NOTIFY or another event transport without changing the persisted replay contract.

## Falsification tests

CI verifies:

1. store reconstruction preserves conversations, run bindings, and events;
2. concurrent event appends produce contiguous ordered sequence numbers;
3. duplicate run writes are idempotent;
4. a run ID cannot be rebound to a different input/fingerprint;
5. cursor replay after reconstruction returns only events after the acknowledged sequence;
6. an event cannot bind a run to the wrong conversation.

## Boundary decision

**KNOWN:** committed PostgreSQL metadata/events survive construction of a new store instance and preserve replay/binding semantics.

**UNKNOWN / not claimed:** atomicity between PostgreSQL metadata commit and Temporal workflow start. A4's retry/fingerprint reconciliation remains the recovery contract for that cross-system boundary.

**UNSUPPORTED:** treating the in-process watcher as durable or as multi-replica pub/sub.
