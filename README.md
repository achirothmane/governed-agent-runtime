# AI-Native Agent Runtime

A persistent runtime for AI workers that combines agent-facing primitives with durable execution and explicit effect boundaries.

> The repository name, `governed-agent-runtime`, reflects its first phase. The scope is now broader: governance remains an optional first-class boundary for consequential effects, while the runtime itself owns agents, tools, conversations, workspaces, events, durable work, model integration, and recovery.

## Target shape

```text
agent / conversation
        ↓
tools + workspace
(native / MCP)
        ↓
bound run request
        ↓
ExecutionBackend
        ↓
native runtime | Temporal
        ↓
engine / external system
        ↓
receipt + observation + reconciliation
```

The public [`sdk`](sdk/) package is the cross-repository contract. It deliberately does not expose `internal/*` implementation details.

The current durable event loop, PostgreSQL store, model-plan binding, Aegis admission integration, and GitHub effect adapter remain useful substrate. They are being composed under the wider AI-native runtime rather than discarded.

## Current capabilities

Implemented and tested before the AI-native expansion:

- durable event ownership, fenced takeover, and lease-bound agent lifecycle;
- file and transactional PostgreSQL stores;
- model plans bound to exact persisted bytes and SHA-256 digests;
- externally signed admission verified against a host-configured trust root;
- authenticated HTTP admission issuance and live witness revalidation;
- a bounded GitHub pull-request effect adapter with reconciliation for lost acknowledgements.

Runtime A1 adds a public contract for:

- agents and missions;
- native and MCP tools;
- local, ephemeral, and remote workspaces;
- conversations and run identity;
- deterministic capability resolution;
- bound run fingerprints;
- pluggable durable execution backends;
- event envelopes for the future Agent Server.

Runtime A2 adds a Temporal durable-execution adapter behind the same `ExecutionBackend` contract:

- stable Runtime `RunID` → Temporal Workflow ID mapping;
- exact run fingerprint binding in workflow input and Temporal Memo;
- idempotent duplicate-start reconciliation;
- fail-closed inspection when binding evidence is missing or mismatched;
- start, inspect, signal, and cancel operations;
- explicit `UNKNOWN` handling where workflow-chain continuity is not yet proven;
- Temporal Go SDK v1.48.0, pinned to preserve the Go 1.25 toolchain boundary.

Runtime A3 adds an MCP transport boundary using the official MCP Go SDK:

- deterministic capability snapshots for discovered MCP tool surfaces;
- SHA-256 snapshot digests bound into runtime `ToolDescriptor` and therefore run fingerprints;
- explicit rejection of tools not present in the admitted snapshot;
- fail-closed invalidation after `tools/list_changed`;
- normalized structured/unstructured results and input-required continuations;
- remote MCP `ToolAnnotations` retained as descriptive hints, never trusted as runtime authority;
- official MCP Go SDK v1.8.0 while preserving the Go 1.25 toolchain boundary.

Runtime A4 adds the Agent Server service boundary:

- bearer-authenticated REST endpoints for agents, conversations, runs, signals, and cancellation;
- server-owned runtime construction so clients cannot inject backends or tool catalogs;
- MCP snapshot binding enforced before a run may start;
- idempotent run creation and fingerprint checks across retries;
- replayable Server-Sent Events with `Last-Event-ID` / cursor resume;
- strict bounded JSON request decoding and explicit conflict / retry semantics;
- pluggable server metadata/event `Store`, with a concurrency-safe in-memory reference implementation.

Runtime A4.1 adds a restart-durable PostgreSQL Agent Server store:

- durable conversations and immutable agent/workspace bindings;
- durable run metadata and execution identity bindings;
- transactionally allocated, gap-free event sequence numbers per conversation;
- ordered event replay after process restart;
- database-level foreign keys preventing events from being attached to the wrong run/conversation;
- polling-based wakeups that preserve correctness across multiple server processes without pinning one database connection per SSE client;
- the in-memory store remains available only as a local/test reference implementation.

Runtime A4.1 adds restart-durable Agent Server persistence in PostgreSQL:

- durable conversation → agent → workspace bindings;
- durable run metadata, input digests, and exact runtime fingerprints;
- per-conversation transactional event sequencing;
- replay after process/store reconstruction;
- idempotent run inserts and fail-closed binding conflicts;
- in-process watchers treated only as wakeup hints, never as durable event evidence.

Runtime A5 connects the first real independent engine, Data Engine:

- `data.profile` discovered through MCP and pinned into the exact run fingerprint;
- server-owned Data Engine identity, endpoint, and read-only admission;
- run-scoped `POST /v1/runs/{runID}/tools/{tool}` for admitted read-only MCP capabilities;
- capability changes rejected before raw arguments leave Agent Server;
- a second snapshot check immediately before invocation closes the discovery/invoke race;
- `tool.called`, `tool.returned`, and `tool.failed` evidence without copying raw rows/results into the event log;
- synchronous A5 invocation deliberately limited to read-only/idempotent work.

Runtime A5.1 moves that read-only tool execution onto Temporal:

- explicit `invocation_id` as the durable tool-execution identity;
- deterministic Temporal workflow ID `ai-native-tool/<run>/<invocation>`;
- raw arguments/results persisted in PostgreSQL instead of copied into Temporal workflow input;
- exact run/tool/snapshot/arguments binding checked before activity execution;
- completed results reused without reinvoking MCP after retry/replay;
- capability-change failures made non-retryable;
- execution evidence emitted by the Temporal activity rather than invented by the HTTP request;
- mutating tools refused by this retry contract.

Runtime A5.2 turns the durable tool plane into a runnable composition:

- `cmd/ai-native-tool-worker` wires Temporal + PostgreSQL + Data Engine MCP;
- `temporaltools.NewWorker` validates and registers the real workflow/activity pair;
- an opt-in integration proof starts a real Temporal dev server and worker;
- the cross-repository gate calls the actual private Data Engine MCP service rather than an in-repo fake;
- repeated invocation IDs recover the stored COMPLETE result without another activity execution.


Runtime A6 adds the first provider-neutral agent decision loop:

- structured Reasoner decisions: `TOOL | FINISH | ASK | FAIL`;
- exact bound-run capability check before reasoning;
- unbound or non-read-only tool selections rejected before execution;
- deterministic per-step invocation IDs feeding the A5.1 durable tool path;
- normalized tool results returned to the next reasoning turn as observations;
- Agent Server `POST /v1/runs/{runID}/execute`;
- no chain-of-thought requirement or persistence;
- terminal `run.completed / run.waiting / run.failed` evidence without copying raw observations.

Runtime A6.1 makes the reasoning loop durable:

- `ai-native.agent.v1` survives caller disconnect and reconciles retries to one workflow per run;
- PostgreSQL pins run fingerprint, mission digest, conversation, and max-step budget;
- committed reasoning steps are replayed without recalling the Reasoner;
- reasoning records are compact: kind/tool/message/invocation ID/digest only;
- raw TOOL arguments live only in the A5.1 invocation ledger;
- agent workflow history carries decision and observation references rather than raw input/tool results;
- A5.1 tool workflow results are also compact and reload the verified result from PostgreSQL after Temporal completion;
- every reasoning activity revalidates the current bound runtime before continuing.


See [AI-Native Runtime Architecture](docs/ai-native-runtime.md), [Temporal Durable Backend A2](docs/temporal-backend.md), [MCP Transport A3](docs/mcp-transport.md), and [Agent Server A4](docs/agent-server.md), and [Agent Server PostgreSQL Store A4.1](docs/agent-server-postgres.md), [Data Engine A5](docs/data-engine-a5.md), and [Temporal Tool Activity A5.1](docs/temporal-tool-activity-a5-1.md), and [Worker E2E A5.2](docs/worker-e2e-a5-2.md), and [Agent Loop A6](docs/agent-loop-a6.md), and [Durable Agent A6.1](docs/durable-agent-a6-1.md).

## Effect boundary

Models still do **not** receive direct authority to execute consequential real-world effects. When a tool crosses into a governed effect, the existing path remains available:

```text
proposed effect
  ↓
governance boundary
  ↓
admission + execution authority
  ↓
effect boundary
  ↓
real system
  ↓
receipt + observation + reconciliation
```

This repository remains intentionally separate from Aegis-EGE, EASL, assumption-gate, token-governance-protocol, Data Engine, Marketing OS, and Dots. Those are independent capabilities connected through explicit contracts.

Existing proof-limit documentation remains under [`docs/`](docs/).
