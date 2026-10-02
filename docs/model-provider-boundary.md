# Model Provider Boundary v0

The model is a planner, not an authority source.

A provider receives a bounded planning request and returns inert data describing proposed effects.

```text
durable event
    ↓
agent context
    ↓
ModelProvider
    ↓
Plan
    ↓
ProposedEffect[]
    ↓
NO EFFECT YET
```

The provider interface intentionally receives no worker lease, credential, execution token, approval object, or effect-boundary handle.

A plan contains:

- the agent and event identity it was produced for;
- a provider reference;
- a human-readable summary;
- zero or more proposed effects;
- symbolic tool/action names;
- JSON arguments;
- optional evidence references and rationale.

It does not contain executable callbacks or execution authority.

## Validation boundary

Before a provider result can move deeper into the runtime, the planner verifies:

1. plan identity is present;
2. plan agent identity matches the request;
3. plan event identity matches the request;
4. provider identity is present;
5. proposed effect IDs are unique;
6. proposed tool references are structurally valid;
7. proposed tools are inside the agent's declared capability set;
8. effect arguments are valid JSON;
9. evidence references are non-empty and unique.

A provider error produces no plan.

## What this does not prove yet

A declared tool capability is not authorization to execute an effect.

The next governance stage must bind a specific immutable plan/effect to evidence, policy, authority, admission, an execution lease, and the effect boundary before any real-world side effect occurs.

The intended path is:

```text
ModelProvider
    ↓
Plan (data only)
    ↓
durable plan binding
    ↓
Evidence
    ↓
Authority
    ↓
Admission
    ↓
Execution Lease
    ↓
Effect Boundary
    ↓
real effect
```

This keeps GPT, Claude, Gemini, local models, or future providers replaceable without allowing any provider to become the source of execution authority.
