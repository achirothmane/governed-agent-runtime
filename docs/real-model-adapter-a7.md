# A7 — Real Model Provider Adapter (draft)

A7 adds a real-model-facing `agentloop.Reasoner` adapter without changing the A6.1 Temporal workflow or the durable tool execution ledger.

## Scope of this initial PR

- OpenAI-compatible `POST /v1/chat/completions`, compatible in principle with provider endpoints supporting `response_format: json_schema`. Provider-specific support must be verified in the live gate.
- Server-configured provider URL, model, credentials and trusted `ToolGuides`.
- Exactly one **structured** `TOOL | FINISH | ASK | FAIL` proposal per model call.
- Model-selected `TOOL` requires a bound, read-only name and a JSON object for arguments.
- Every exposed MCP schema must match the exact `ToolDescriptor.SnapshotDigest` bound to the run. A guide mismatch stops before any provider call.
- No raw tool endpoint, workspace path, or transport authority in the model prompt.
- External observations are treated as untrusted input, not higher-priority instructions.
- Requests and responses are size-bounded; no implicit retries, response-body logging or HTTP redirects.
- Decision validation remains enforced by `agentloop` and again by the durable `temporalagent.ReasonActivity`.

### Why ToolGuides are needed

The existing `sdk.ToolDescriptor` currently carries name, protocol, endpoint, read-only status and snapshot digest, **but not** the argument JSON schema. Passing only the tool name would lead to model-invented arguments. Therefore A7 accepts **trusted operator-constructed** `ToolGuides`, with descriptions and object schemas. MCP guides require an exact snapshot-digest match.

A follow-up integration must populate guides from the admitted MCP discovery snapshot; it must not ask the model to invent those guides or assume that the source matches without binding.

### Composition

```go
reasoner := modeladapter.Adapter{Config: modeladapter.Config{
    BaseURL: providerURL, // e.g. https://api.openai.com/v1 or http://127.0.0.1:11434/v1
    APIKey: providerAPIKey, // local loopback providers can omit this
    Model: modelName,
    ToolGuides: trustedGuidesByBoundToolName,
}}

worker, err := temporalagent.NewWorker(temporalagent.WorkerConfig{
    // Existing A6.1 client/queue/resolver/stores/executor unchanged.
    Reasoner: reasoner,
})
```

The snippet highlights the `Reasoner` field, not a complete standalone executable.

### Data privacy and expense

A remote provider receives the mission, run input, trusted tool schemas and relevant tool observations. Configure a remote endpoint only when that data transfer is acceptable; for local usage use a loopback OpenAI-compatible provider (such as a compatible Ollama endpoint). This package never contains a hardcoded model or API key. **A ChatGPT subscription is not an API usage credit.**

A provider response can be billed twice when the worker fails after model response but before decision commit. There is **no exactly-once billing** promise. Temporal retries continue to apply, but only one committed decision is allowed to dispatch a tool.

### Tests in this PR

Unit tests use an HTTP test server to verify model request shape, strict JSON response contract, tool allowlisting, schema drift refusal, invalid output refusal, redirect refusal, and HTTPS / local-HTTP boundary. Such tests **do not prove a real LLM call**.

## A7 acceptance gate still open

The final gate must execute with one named, actually available provider and the live A6.1 stack:

1. Exact model identifier, endpoint type and credential configured at runtime without committing secrets.
2. Real `TOOL data.profile` selected with the admitted JSON input schema and executed through PostgreSQL + Temporal + real Data Engine MCP.
3. Real observation bound to invocation and digest; second model step chooses `FINISH` or an explicit `ASK/FAIL`.
4. Client disconnect and rejoin the **same** Temporal agent workflow, with no duplicate committed tool effects.
5. Provider timeouts, rate limits, malformed content, refusals, schema drift and exhausted step budget tested fail closed.
6. Record model call count and token/usage cost separately from tool-execution count; document the crash window before reasoning commit.

**Status: A7 adapter scaffold, NOT A7 PASS.** Do not merge or announce a live A7 until the real-provider gate is demonstrated.
