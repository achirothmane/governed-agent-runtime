# AI-Native Runtime Architecture

This repository is evolving from a governance-centered agent runtime into a general AI-native runtime.
Governance remains a first-class capability for real-world effects, but it is no longer the identity of the whole runtime.

## Target system

```text
Agent SDK / Agent Server
        │
        ├── Agents
        ├── Tools
        │    ├── native
        │    └── MCP
        ├── Conversations
        ├── Workspaces
        └── Events
              │
              ▼
      ExecutionBackend
              │
      ┌───────┴────────┐
      │                │
 native/local       Temporal
 backend            adapter
      │                │
      └───────┬────────┘
              ▼
       long-running work
              │
      ┌───────┼──────────────┐
      ▼       ▼              ▼
 Data Engine  Marketing OS   Other Engines
              │
              ▼
             Dots
    single coordination system
              │
              ▼
     AI Native Engineering
        product / website
```

OpenHands is a reference for the agent-facing semantics: agents, tools, conversations, workspaces, events, an Agent Server, and MCP-capable tool access. We do not copy OpenHands internals or turn this repository into a fork of its application.

Temporal is the first planned production durable-execution adapter. Domain logic must not import Temporal directly. The SDK owns a small `ExecutionBackend` boundary so Temporal can be introduced, replaced, tested, or bypassed without changing agent or engine contracts.

## Ownership boundaries

### SDK

The public `sdk` package owns portable contracts for:

- agent identity and mission;
- required tool capabilities;
- local, ephemeral, and remote workspaces;
- conversations and run identity;
- native and MCP tool protocols;
- immutable run binding/fingerprints;
- execution backend control: start, inspect, signal, cancel;
- runtime event envelopes.

The package is deliberately importable by other repositories. Cross-project integrations must not depend on `internal/*` packages.

### Durable execution

A durable backend owns scheduling, recovery, retries, timers, signals, cancellation, and long-lived execution identity. The runtime sends it a fully bound `RunRequest` and requires the returned handle to preserve that binding.

The current `internal/runtime` event loop is retained as a native substrate and migration source. It is not deleted merely because Temporal is planned.

### Tools and MCP

A tool is resolved before a run starts. Missing capabilities fail closed. MCP is a transport boundary, not a special class of agent. An MCP tool must identify an endpoint; a native tool must not pretend to be remote.

A later MCP adapter will own discovery, session lifecycle, invocation, result normalization, and transport-specific errors. Agent code sees only the SDK tool contract.

### Governance and effects

Read-only reasoning, retrieval, planning, and analysis do not require a governance ceremony merely to exist. Real-world side effects can still cross the existing governance/effect boundary and preserve the stronger contracts already implemented in this repository.

This keeps the useful safety substrate without making governance the product identity.

### Engines

Data Engine, Marketing OS, and future engines remain independent repositories. They integrate through explicit tools, services, events, or workspaces rather than being copied into this repository.

### Dots

Dots sits above individual runs. It coordinates projects, dependencies, checkpoints, and outcomes. It does not become an agent runtime and does not absorb engine internals.

## Runtime A1 contract

The first implementation slice introduces:

1. `AgentSpec` with mission, workspace, required tools, and optional policy reference.
2. `ToolCatalog` with deterministic capability resolution and fail-closed missing-tool behavior.
3. explicit `native` and `mcp` tool protocols.
4. local, ephemeral, and remote workspace contracts.
5. `RunRequest` binding agent, conversation, workspace, tools, input, and policy into a SHA-256 fingerprint.
6. `ExecutionBackend` with `Start`, `Inspect`, `Signal`, and `Cancel`.
7. runtime verification that a backend cannot silently change run identity or the bound request fingerprint.
8. event-envelope contracts for the future Agent Server and durable event stream.

## Runtime slices

### A1 — Runtime contracts ✅

Portable agents, tools, workspaces, conversations, run identity, event envelopes, and the `ExecutionBackend` boundary.

### A2 — Temporal durable backend ✅

Runtime RunID → Temporal workflow identity, exact fingerprint binding, duplicate-start reconciliation, inspect/signal/cancel, and explicit UNKNOWN semantics.

### A3 — MCP transport ✅

Official MCP transport, canonical capability snapshots, snapshot digests bound into run identity, fail-closed stale-snapshot handling, and normalized tool results.

### A4 — Agent Server ✅

Authenticated REST, conversations, run start/inspect, signal/cancel, replayable SSE, strict request decoding, and server-owned runtime construction.

### A4.1 — PostgreSQL Agent Server store ✅

Restart-durable conversations, immutable run bindings, transactionally ordered per-conversation events, and replayable durable cursors without depending on process memory.

### A5 — First engine integration

Connect one real engine end-to-end. Data Engine remains the preferred first integration because read-only profiling can prove the service/runtime/engine path before mutating data effects are introduced.

## Architectural rules

- No engine-specific business logic in the runtime core.
- No direct Temporal imports from agents or engines.
- No hidden tool acquisition after a run is bound.
- No MCP endpoint treated as trusted merely because discovery succeeded.
- No direct real-world effect from a model response.
- No monorepo requirement: integration happens through contracts.
- A capability and its boundary are built together.
