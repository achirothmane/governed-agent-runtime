# A6 — Agent Execution Loop

A6 adds the first model-facing execution loop to the AI-native runtime.

The loop is provider-neutral. A `Reasoner` decides only among four structured actions:

- `TOOL`
- `FINISH`
- `ASK`
- `FAIL`

No chain-of-thought is requested, persisted, or required by the runtime.

## Execution shape

```text
bound RunRequest
     |
     v
Reasoner
     |
     +--> TOOL
     |      |
     |      v
     |  validate tool is bound
     |      |
     |  validate read-only
     |      |
     |  stable invocation_id
     |      |
     |      v
     |  DurableToolInvoker
     |      |
     |      v
     |  Temporal + PostgreSQL + MCP
     |      |
     |      v
     |   Observation
     |      |
     +------+
     |
     +--> FINISH
     +--> ASK
     +--> FAIL
```

The default maximum is 8 reasoning steps. Deployments may configure 1–64.

## Bound tool authority

The Reasoner receives the exact `RunRequest` that was bound when the run started.

A model cannot introduce:

- a new MCP endpoint;
- a different capability snapshot;
- a tool not present in the run;
- write authority.

If it selects an unbound tool, the loop fails before the tool executor is called.

A6 continues the A5.1 restriction to server-admitted read-only tools.

## Stable tool invocation identity

For every TOOL decision, A6 derives a deterministic invocation ID from:

- run ID;
- reasoning step;
- selected tool;
- arguments.

Equivalent replay of the same bound decision therefore reaches the same A5.1 invocation identity and can reuse an already completed durable result.

## Observation semantics

The Reasoner receives normalized tool results as `Observation` values.

Tool-level MCP errors remain observations when the transport returned a valid tool result with `is_error=true`. Transport/protocol/durable execution failures remain execution errors.

The runtime does not require a rationale field and does not expose private reasoning traces.

## Agent Server

A6 adds:

```text
POST /v1/runs/{runID}/execute
```

Before reasoning begins, Agent Server reconstructs the current run with `Runtime.Bind` and requires the fingerprint to equal the stored run fingerprint.

A changed capability surface therefore produces HTTP 409 before the Reasoner is called.

Agent Server can be composed either with an explicit `AgentRunner` or with:

- `Reasoner`
- `DurableInvoker`
- optional `AgentMaxSteps`

In the latter case it constructs the A6 Engine automatically.

## Terminal evidence

Agent Server persists only terminal execution evidence:

- `run.completed` for FINISH;
- `run.waiting` for ASK;
- `run.failed` for FAIL or MAX_STEPS.

The terminal event contains:

- outcome kind;
- user-visible message;
- number of steps;
- observation count.

Raw tool observations are not copied into the terminal event.

Tool execution evidence remains owned by the A5.1 Temporal activity.

## Cross-repository proof

The opt-in A6 integration proof uses:

- real PostgreSQL;
- real Temporal dev server;
- real Temporal worker;
- real Agent Server HTTP;
- real Data Engine MCP endpoint;
- a deterministic Reasoner implementation.

The proof requires the Reasoner to:

1. select `data.profile`;
2. receive the real `PRE_SEMANTIC_PROFILE / CONFLICTING` observation;
3. return FINISH;
4. produce durable tool evidence followed by `run.completed`.

The deterministic Reasoner proves the loop contract and tool boundary. It is not presented as an LLM.

## Boundary

**KNOWN:** a provider-neutral Reasoner can choose a bound read-only tool, receive its durable observation, and choose the next structured action.

**KNOWN:** unbound or mutating tool choices fail before execution.

**KNOWN:** capability drift is rejected before reasoning begins.

**UNKNOWN / not claimed:** the A6 reasoning loop itself is restart-durable. The A6 HTTP execution request owns the reasoning loop lifetime, while tool calls inside it are durable.

**UNSUPPORTED:** mutating tool choices.

## Next boundary — A6.1

A6.1 should move reasoning steps themselves into Temporal activities and persist only compact decision/observation references outside workflow history.

That will change:

```text
request-scoped reasoning
        ↓
durable tool calls
```

into:

```text
durable reasoning step
        ↓
durable tool call
        ↓
durable observation
        ↓
durable next decision
```

A model-provider adapter (OpenAI, local model, or another provider) can then implement the same `Reasoner` contract without changing the agent loop semantics.
