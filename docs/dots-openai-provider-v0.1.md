# Dots OpenAI Responses reasoner adapter v0.1

D5 adds one concrete model-provider transport behind the existing `dotdurable.Reasoner` interface.

The adapter uses the OpenAI **Responses API** with Structured Outputs (`text.format.type=json_schema`). It has no tool definitions and receives only the sealed Portfolio `ReasoningView`; Portfolio source bindings and arbitrary repository files are not part of the provider request.

The model is not an authority boundary.

```text
sealed Portfolio ReasoningView
        |
        v
OpenAI Responses adapter
        |
        v
candidate typed decision
        |
        v
D3 dotdecision.Validate
        |
        v
D4 Temporal + PostgreSQL commit
```

A provider may return a stale work item, authority escalation, or human-final action. Those outputs are rejected by D3 before D4 persistence.

Repository tests use a local deterministic HTTP server. No OpenAI credential or paid request is required in CI.

A live provider smoke test is intentionally not enabled by this adapter. Any paid live call requires explicit human approval under the D5 execution-queue gate.
