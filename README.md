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

See [AI-Native Runtime Architecture](docs/ai-native-runtime.md), [Temporal Durable Backend A2](docs/temporal-backend.md), and [MCP Transport A3](docs/mcp-transport.md).

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
