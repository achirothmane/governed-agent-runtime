# A5 — Data Engine read-only MCP bridge

A5 is the first real engine integration for the AI-native runtime.

The Data Engine remains an independent repository and service. The runtime does not import Data Engine implementation packages and Data Engine does not import Temporal.

## End-to-end boundary

```text
Client / Dots / UI
       |
       v
Agent Server
       |
       | existing bound run
       v
RuntimeProvider
       |
       | fresh MCP discovery
       v
data-engine identity + data.profile
       |
       | Runtime.Bind(...)
       | exact capability snapshot digest
       v
compare current fingerprint
with stored run fingerprint
       |
       +-- mismatch --> 409 / no data leaves server
       |
       v
persist tool.called evidence
(name + snapshot + argument digest)
       |
       v
ToolInvoker
       |
       | re-discover exact snapshot
       v
Data Engine /mcp
       |
       v
data.profile
       |
       v
PRE_SEMANTIC_PROFILE
       |
       v
persist tool.returned evidence
(name + snapshot + result digest)
```

## Server-owned authority

The HTTP caller supplies only:

- the existing run ID;
- the bound tool name;
- tool arguments.

The caller cannot supply or override:

- MCP endpoint;
- server identity;
- capability snapshot digest;
- execution backend;
- tool catalog;
- read-only admission.

The Data Engine provider owns those values.

## Two capability checks

A5 deliberately checks the capability surface twice.

### 1. Run fingerprint check

Before arguments leave Agent Server, the server asks the current RuntimeProvider to reconstruct the run with `Runtime.Bind`.

The newly computed fingerprint must equal the fingerprint stored when the run began.

A changed Data Engine tool schema, server identity, endpoint, or snapshot digest changes the bound run fingerprint. The call is rejected with HTTP 409 before invocation.

### 2. Invocation snapshot check

The Data Engine provider reconnects and discovers the MCP surface immediately before invoking `data.profile`.

The current snapshot digest must still equal the descriptor bound to the run. A change racing after the first check is rejected through the MCP snapshot-mismatch contract.

These checks do not prove the remote implementation code is immutable. They prove that the runtime does not silently adopt a changed MCP capability surface.

## Read-only gate

The A5 HTTP invocation route permits MCP tools only when the **server-owned descriptor** marks them read-only.

Remote MCP `readOnlyHint` is descriptive metadata and does not grant authority. The Data Engine integration explicitly admits `data.profile` as read-only because that capability is our known contract.

Non-read-only tools are rejected before invocation.

## Evidence without duplicating data

Agent Server persists:

`tool.called`

- tool name;
- snapshot digest;
- SHA-256 digest of the JSON arguments.

`tool.returned`

- tool name;
- snapshot digest;
- tool-level error flag;
- SHA-256 digest of the normalized result.

Raw rows and full profile results are not copied into the Agent Server event log.

If the pre-call event cannot be persisted, invocation does not happen.

If the read-only call succeeds but post-call event persistence fails, the result is still returned with `event_persisted=false`; the server does not rewrite an observed successful read into a false execution failure.

## HTTP surface

A5 adds:

```text
POST /v1/runs/{runID}/tools/{tool}
Authorization: Bearer <token>
Content-Type: application/json

{
  "arguments": {
    "...": "..."
  }
}
```

The route is intentionally synchronous and read-only in A5.

## What A5 proves

- a real Data Engine capability exists over MCP;
- Agent Server can bind that capability into a run fingerprint;
- the capability can be invoked through the runtime boundary;
- capability expansion/change fails closed;
- raw arguments/results do not become event-log copies;
- only server-admitted read-only tools can use this synchronous route.

## What A5 does not claim

A5 is **not yet** the generic Temporal worker/tool execution loop.

The synchronous HTTP call itself is not durable execution. If the HTTP acknowledgement is lost, the caller may repeat the read. That is acceptable only because A5 is restricted to a read-only, idempotent capability.

A mutating tool must not be enabled through this route.

## Next boundary — A5.1

A5.1 moves tool execution inside the durable runtime:

```text
Temporal Workflow
      |
      v
Tool Activity
      |
      +--> pinned MCP descriptor
      +--> tool.called evidence
      +--> invoke
      +--> tool.returned / tool.failed evidence
      |
      v
resume durable agent run
```

That is where retries, activity identity, cancellation, and unknown outcomes for generic tools become part of the durable execution contract.
