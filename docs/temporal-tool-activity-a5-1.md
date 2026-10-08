# A5.1 — Durable Temporal Tool Activity

A5.1 moves the A5 read-only MCP invocation path onto Temporal without putting raw Data Engine rows into Temporal workflow history.

## Flow

```text
Agent Server
    |
    | invocation_id + admitted tool + raw arguments
    v
PostgreSQL Invocation Store
    |
    | PREPARED
    | raw arguments + SHA-256 digest
    v
Temporal Tool Workflow
    |
    | InvocationRef only
    | run / conversation / invocation
    | tool + snapshot digest
    | arguments digest
    v
Tool Activity
    |
    | load arguments from PostgreSQL
    | verify exact digest + binding
    v
Data Engine Provider
    |
    | fresh MCP discovery
    | exact snapshot check
    v
data.profile
    |
    v
PostgreSQL COMPLETE
    |
    | normalized result + result digest
    v
Temporal workflow result
    |
    | invocation_id + result_digest only
    v
Executor reloads verified result from PostgreSQL
```

## Payload boundary

Raw profiling rows are stored in PostgreSQL. Temporal receives only an `InvocationRef`: invocation ID, run ID, conversation ID, bound tool descriptor, MCP snapshot digest, and SHA-256 arguments digest.

As of A6.1, the activity/workflow result is compact as well: it contains only invocation ID, result digest, and reuse status. The full normalized MCP result is reloaded from PostgreSQL after the Temporal workflow completes and its digest is verified before it is returned to the caller.

The durable workflow ID is deterministic: `ai-native-tool/<run_id>/<invocation_id>`. Reusing the same invocation ID reconciles to the same Temporal workflow instead of creating another execution.

PostgreSQL independently binds the invocation ID to the run, conversation, tool name/protocol/endpoint, server-admitted read-only classification, MCP snapshot digest, and arguments digest. Rebinding fails closed.

## Retry contract

The workflow executes `ai-native.tool.execute.v1` with a bounded retry policy. Before MCP invocation, the activity reloads the stored payload, verifies its digest and binding, and returns an already stored COMPLETE result without calling the remote tool again.

A transport/protocol failure records FAILED and can be retried because A5.1 accepts only server-admitted read-only tools.

## Unknown outcome

A5.1 does not claim exactly-once execution for mutating tools. If the remote read succeeds but the process dies before PostgreSQL stores COMPLETE, Temporal may repeat the read. That boundary is acceptable for `data.profile`; mutating tools are refused by this contract.

## Evidence layers

1. Agent Server events contain invocation identity and digests without raw rows.
2. PostgreSQL contains restart-durable request/result payloads and invocation state.
3. Temporal history contains scheduling, retry, cancellation, workflow identity, binding digests, and compact invocation/result references — not the full normalized tool result.

None of these layers alone is promoted into a business-truth claim.

## Worker composition

Register `temporaltools.Workflow` and `temporaltools.Activity` with `temporaltools.Register`. Compose the activity with the PostgreSQL store and the Data Engine provider. Configure Agent Server with a `temporaltools.Executor` as its durable tool invoker.

## Falsification gates

- completed activity results are reused without reinvoking MCP;
- tampered stored arguments fail before invocation;
- failed reads remain retryable;
- raw argument values are absent from Temporal workflow input;
- normalized tool result payloads are absent from Temporal activity/workflow results;
- duplicate Temporal starts reconcile to the same workflow ID;
- PostgreSQL invocation records survive store reconstruction;
- invocation IDs cannot be rebound to another snapshot/request;
- a late failure cannot overwrite a completed invocation;
- Agent Server requires invocation ID when durable execution is configured;
- event evidence keeps digests and invocation identity without raw input.

## Boundary decision

**KNOWN:** read-only MCP tools can be coordinated durably by Temporal while raw arguments and normalized results remain in restart-durable PostgreSQL and outside Temporal workflow arguments/results.

**KNOWN:** a locally persisted COMPLETE result prevents a later activity retry from reinvoking the tool.

**UNKNOWN:** the remote read may have completed if the process dies after the remote response but before completion persistence.

**REFUSED:** mutating MCP tools on the A5.1 retry contract.

## CI authority

The merge gate is the repository CI on the final branch head: module reproducibility, format check, and `go test -race ./...` against PostgreSQL 16.
