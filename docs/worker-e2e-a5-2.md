# A5.2 — Real Worker Composition and Cross-Repository Proof

A5.2 turns the A5.1 pieces into one runnable worker and proves the path against the real Data Engine repository.

## Production worker

`cmd/ai-native-tool-worker` composes:

```text
Temporal Client
      |
      v
temporaltools.Worker
      |
      +--> temporaltools.Workflow
      +--> temporaltools.Activity
              |
              +--> PostgreSQL invocation ledger
              +--> Agent Server activity evidence
              +--> Data Engine MCP provider
```

Required environment:

- `DATABASE_URL`
- `DATA_ENGINE_MCP_URL`

Optional environment:

- `TEMPORAL_ADDRESS`, default `localhost:7233`
- `TEMPORAL_NAMESPACE`, default `default`
- `TEMPORAL_TOOL_TASK_QUEUE`, default `ai-native-tools`

The worker runs until SIGINT/SIGTERM.

## Cross-repository proof

The A5.2 proof deliberately does not import Data Engine implementation code.

The runtime integration test consumes only a real MCP endpoint. Because `data-engine` is private while the runtime repository is public, the cross-repository CI is launched from the Data Engine repository. That direction requires no new PAT or shared secret:

```text
private data-engine CI
     |
     +--> local checkout: real Data Engine
     |
     +--> public checkout: governed-agent-runtime A5.2
     |
     +--> PostgreSQL 16
     |
     +--> Temporal CLI dev server
     |
     v
Agent Server HTTP
     ↓
Temporal Executor
     ↓
real Temporal Worker
     ↓
PostgreSQL invocation ledger
     ↓
real MCP HTTP
     ↓
Data Engine data.profile
```

## What the proof checks

The integration test:

1. starts a real Temporal dev server through the Temporal Go SDK;
2. starts a real Temporal worker registered with A5.1 workflow/activity;
3. connects to PostgreSQL and migrates the real Agent Server schema;
4. discovers `data.profile` from the actual Data Engine MCP service;
5. binds that exact snapshot into a run fingerprint;
6. calls Agent Server over HTTP with a stable invocation ID;
7. waits for Temporal workflow/activity completion;
8. verifies the real Data Engine result is `PRE_SEMANTIC_PROFILE / CONFLICTING`;
9. verifies PostgreSQL records the invocation as `COMPLETE`;
10. verifies activity evidence contains digests without copying the raw secret;
11. repeats the same HTTP request and verifies no second activity evidence is created.

## Boundary

A5.2 proves the **tool execution plane** end to end.

It does not claim that the parent agent run is itself executing an autonomous reasoning loop. The integration proof seeds an already-bound parent run, because A6 owns model reasoning → tool selection → observation → next-decision semantics.

This separation is intentional: A5.2 proves worker composition without smuggling an unfinished planner into the durable execution layer.


## Falsification found during A5.2

The first real cross-repository run failed with `durable tool result digest mismatch`.

The cause was not a changed Data Engine result. PostgreSQL `JSONB` is allowed to reserialize nested JSON objects, so a digest computed from one physical JSON representation could differ after a semantically equivalent value was read back from the invocation ledger.

A5.2 therefore changed durable result hashing to canonical semantic JSON before SHA-256. Equivalent object key ordering and insignificant JSON formatting now produce the same result digest.

This bug was invisible to the in-memory A5.1 tests and is the reason the real PostgreSQL + Temporal + Data Engine proof is retained as a separate gate.

## CI authority

Merge requires both:

1. the runtime repository gate: module reproducibility, format check, and `go test -race ./...` with PostgreSQL 16;
2. the cross-repository Data Engine proof using the real MCP service and real Temporal dev server.
