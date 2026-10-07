# PostgreSQL Agent Server Store A4.1

A4.1 replaces process memory as the production persistence boundary for Agent Server conversations, run bindings, and replayable events.

## Data model

```text
agent_server_conversations
  conversation_id PK
  agent_id
  workspace_id
  created_at
  next_event_sequence
          |
          +-----------------------------+
          |                             |
          v                             v
agent_server_runs                 event sequence allocator
  run_id PK                             |
  conversation_id                       v
  agent_id                      agent_server_events
  input                          (conversation_id, sequence) PK
  input_digest                   run_id
  backend                        event_type
  external_id                    occurred_at
  fingerprint                    payload
  state
  created_at
```

Foreign keys enforce both:

- a run belongs to the exact conversation/agent binding;
- an event belongs to a run inside the same conversation.

## Conversation identity

`conversation_id` is immutable. Retrying creation with the same agent and workspace returns the stored conversation. Reusing the ID with a different agent or workspace returns `ErrConflict`.

The original `created_at` is retained on idempotent retries.

## Run identity

A stored run must validate all of these fields:

- run ID;
- conversation ID;
- agent ID;
- non-empty input;
- SHA-256 input digest matching the exact input;
- backend name;
- backend external execution ID when one exists;
- runtime fingerprint;
- valid runtime state;
- creation time.

An idempotent duplicate must preserve conversation, agent, input digest, backend, external execution ID, and fingerprint. A different binding under the same run ID is rejected.

## Transactional event sequencing

A conversation owns `next_event_sequence`.

Appending an event executes, in one transaction:

```text
verify run -> conversation binding
          |
          v
UPDATE conversation
SET next_event_sequence = next_event_sequence + 1
RETURNING next_event_sequence
          |
          v
INSERT event with returned sequence
          |
          v
COMMIT
```

Concurrent writers serialize on the conversation row. If event insertion or commit fails, the sequence increment rolls back with it.

Therefore committed events for one conversation have a monotonic, gap-free sequence under normal PostgreSQL transaction semantics.

## Restart recovery

```text
Agent Server process dies
        |
        v
new Agent Server process
        |
        v
PostgreSQL Store
        |
        +--> GetConversation
        +--> GetRun
        +--> ListEvents(after)
        |
        v
SSE resumes from Last-Event-ID
```

No process-local event buffer is required for replay.

## Watch semantics

`Store.Watch` is intentionally not evidence.

The PostgreSQL implementation uses periodic wakeups rather than holding a dedicated LISTEN connection for every SSE client. On each wakeup the Agent Server executes `ListEvents(after)` against PostgreSQL.

This means process restart does not lose event history, writes from another Agent Server process become visible, and an SSE client does not pin one PostgreSQL connection for its whole lifetime. The tradeoff is bounded polling latency and extra empty reads.

A shared LISTEN/NOTIFY dispatcher can replace this wake-up implementation later without changing the Store correctness contract.

## Capability boundary

A4.1 establishes restart-durable conversations, run bindings, and ordered events; transactional per-conversation sequencing; durable cursor replay; cross-process visibility by database reread; and database-enforced run/conversation event ownership.

A4.1 does not establish an atomic distributed transaction between Temporal and PostgreSQL, exactly-once external business effects, proof that cancellation stopped already-issued external effects, a complete multi-tenant authorization system, zero-latency event fan-out, or an archival/retention policy.

The critical non-atomic case remains explicit:

```text
Temporal start succeeds
        |
        X
PostgreSQL metadata write fails
```

The server returns a retryable failure. Retrying the exact same bound request is safe with A2 because Temporal duplicate-start reconciliation checks the runtime fingerprint before reusing the execution.

## Migration

`PostgresStore.Migrate(ctx)` creates three namespaced tables:

- `agent_server_conversations`
- `agent_server_runs`
- `agent_server_events`

They remain separate from the older `agent_runtime_work` table because the two stores own different semantics.

## Next slice

A5 connects Data Engine as the first real engine through the Agent Server using read-only/profile capabilities first.
