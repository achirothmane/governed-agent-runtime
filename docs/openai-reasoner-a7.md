# A7 — OpenAI Responses Reasoner

A7 adds the first real model-provider adapter behind the provider-neutral `agentloop.Reasoner` contract.

The adapter uses the official OpenAI Go SDK and the Responses API, but the runtime does not delegate tool execution authority to OpenAI.

## Execution shape

```text
A6.1 Reasoning Activity
        |
        v
OpenAI Responses Reasoner
        |
        | structured decision only
        v
TOOL | FINISH | ASK | FAIL
        |
        v
A6.1 decision commit
        |
        +--> TOOL
              |
              v
        durable A5.1 tool path
              |
              v
        MCP / Data Engine
```

The provider decides what the next structured action should be. It does not execute runtime tools itself.

## Bound model-visible tool contract

A7 extends `sdk.ToolDescriptor` with model-visible capability metadata:

- title;
- description;
- input schema;
- output schema.

For MCP capabilities these fields are copied from the exact A3 capability snapshot.

Because `RunRequest.Fingerprint()` hashes the complete bound run, changing a tool schema changes the run fingerprint. The model therefore receives the schema that was bound to that run rather than a separately rediscovered schema.

Only tools already admitted by the runtime as `ReadOnly=true` are exposed to the model.

## What the provider can see

The OpenAI request contains:

- agent mission;
- user input;
- current reasoning step;
- bound read-only tool name/title/description/input schema;
- prior normalized observations.

It deliberately excludes:

- MCP endpoint;
- MCP snapshot digest;
- credentials;
- run ID;
- conversation ID;
- tool invocation ID;
- Temporal workflow/activity IDs;
- internal execution bindings.

The runtime preserves those authority-bearing fields locally.

## Structured decision contract

The Responses request uses strict JSON Schema for one envelope:

```text
kind: TOOL | FINISH | ASK | FAIL
tool: string
arguments_json: string
message: string
```

Runtime validation then applies the A6 decision rules.

For `TOOL`:

- the selected name must be in the bound read-only tool set;
- `message` must be empty;
- `arguments_json` must decode to one JSON object.

For terminal decisions:

- tool must be empty;
- arguments must be an empty object;
- message must be non-empty.

Invalid provider output is classified as `ErrInvalidDecision` and becomes a non-retryable `INVALID_REASONER_OUTPUT` failure in the A6.1 Temporal activity.

## Retry and billing boundary

The OpenAI SDK is configured with zero internal retries.

Temporal owns the reasoning-activity retry policy so retry behavior exists in one place.

Deterministic local failures such as:

- missing/invalid bound input schema;
- invalid run context;
- oversized provider input;

are classified as `ErrInvalidReasoningContext` and are non-retryable.

Transport/API failures remain retryable under the A6.1 activity policy.

A7 inherits the A6.1 acknowledgement boundary: if the provider produced a response but the worker dies before the decision is committed, the model request may be repeated. A7 does not claim exactly-once model billing.

## Storage and reasoning privacy

The Responses request sets `store=false`.

The adapter requests only a structured next-step decision. It does not request, expose, or persist chain-of-thought, hidden analysis, or rationale fields.

The durable reasoning ledger continues to store only the compact committed decision/digest defined by A6.1.

## Context bound

Provider input is bounded before the API call. The default maximum serialized model input is 256 KiB and can be configured downward/upward explicitly.

A7 does not yet implement semantic context selection or summarization. Conditional context exposure remains a separate capability rather than silently truncating evidence.

## Tool-argument validation boundary

The exact MCP input schema is shown to the model and is part of the run fingerprint.

A7 validates that `arguments_json` is a single JSON object and that the selected tool is bound. It does **not yet** independently validate the generated arguments against the full JSON Schema before decision commit.

The MCP/tool implementation remains the final schema validator.

A future A7.1 can add local schema validation before the decision is committed, reducing avoidable tool-level validation failures without changing provider authority.

## Evidence

Unit tests prove:

- endpoint/snapshot/run/conversation/invocation identities do not enter the provider request;
- normalized observations do enter the provider request;
- unbound tool output fails closed;
- missing schemas fail before any provider call;
- oversized context fails before any provider call;
- the official OpenAI Go SDK emits `store=false` plus strict JSON-schema output configuration against a local Responses-compatible server.

The opt-in A7 integration proof adds:

```text
official OpenAI Go SDK
        |
local fake Responses endpoint
        |
structured TOOL decision
        |
A6.1 Temporal agent workflow
        |
A5.1 Temporal tool workflow
        |
real Data Engine MCP
        |
CONFLICTING observation
        |
official OpenAI Go SDK
        |
structured FINISH decision
```

No paid OpenAI API call or secret is required for CI.

## Version boundary

A7 pins `github.com/openai/openai-go/v3 v3.73.0`.

That release remains compatible with the repository's Go 1.25 toolchain boundary.

## Boundary decision

**KNOWN:** a real OpenAI Responses adapter can implement the provider-neutral Reasoner contract without receiving runtime execution authority.

**KNOWN:** exact model-visible MCP schemas are bound into the same run fingerprint that authorizes later tool execution.

**KNOWN:** deterministic invalid model output/context does not consume Temporal retry attempts.

**KNOWN:** provider requests omit endpoint, snapshot digest, and durable execution identity.

**UNKNOWN / not claimed:** exactly-once provider billing across worker loss before decision commit.

**UNSUPPORTED:** model-selected mutating tools under the current A5.1/A6 retry contract.
