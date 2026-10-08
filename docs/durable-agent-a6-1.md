# A6.1 — Durable Reasoning Loop

A6.1 moves the A6 reasoning loop from the HTTP request lifetime into Temporal.

The agent execution is now identified independently from any one caller connection:

```text
POST /v1/runs/{runID}/execute
        |
        v
Temporal Agent Workflow
        |
        +--> Reasoning Activity
        |       |
        |       +--> bound run resolver
        |       +--> committed decision ledger
        |       +--> provider-neutral Reasoner
        |
        +--> Tool Dispatch Activity
        |       |
        |       +--> durable A5.1 tool workflow
        |       +--> PostgreSQL invocation ledger
        |       +--> MCP / engine
        |
        +--> next Reasoning Activity
        |
        +--> FINISH | ASK | FAIL | MAX_STEPS
```

If the caller disconnects after the workflow starts, the Temporal execution continues. A later request for the same run reconciles to the same agent workflow instead of creating a second reasoning execution.

## Compact Temporal history

The agent workflow input contains only `ExecutionRef`:

- run ID;
- conversation ID;
- exact run fingerprint;
- mission digest;
- max steps.

It does **not** contain the raw run input, mission text, tool arguments, or tool results.

Each reasoning activity returns only:

- step;
- decision kind;
- decision digest;
- user-visible terminal message when applicable;
- compact tool invocation reference for TOOL decisions.

Each tool observation carried through the agent workflow contains only:

- step;
- invocation ID;
- result digest.

The reasoning activity reconstructs the full `agentloop.Turn` locally from PostgreSQL plus the current bound runtime.

## Tool workflow hardening

A6.1 also tightens A5.1.

Previously the Temporal tool activity result contained the normalized MCP result. That meant the tool workflow history could contain the full observation.

Now the A5.1 workflow returns only:

```text
invocation_id
result_digest
reused
```

After the workflow completes, `temporaltools.Executor` loads the full result from the PostgreSQL invocation ledger and verifies its digest before returning it to the caller.

Therefore raw tool arguments and results no longer need to enter Temporal workflow/activity results.

## Durable execution binding

Before the agent workflow starts, PostgreSQL stores one execution binding:

```text
run_id
conversation_id
run_fingerprint
mission_digest
max_steps
```

Reusing the same run ID with a different fingerprint, mission, conversation, or max-step budget fails with `ErrExecutionConflict`.

The workflow ID is:

```text
ai-native-agent/<run_id>
```

and workflow ID reuse is rejected.

A lost start acknowledgement is reconciled to the existing workflow.

## Durable reasoning decisions

Each reasoning step is committed separately in PostgreSQL.

The reasoning ledger stores only:

- run ID;
- step;
- decision kind;
- tool name when applicable;
- user-visible message for terminal decisions;
- tool invocation ID when applicable;
- decision digest.

TOOL arguments are **not copied** into the reasoning ledger. They live only in the existing tool invocation ledger.

For a TOOL decision the sequence is:

```text
Reasoner
   |
   v
validate bound read-only tool
   |
   v
prepare invocation ledger
(raw args + args digest)
   |
   v
commit compact reasoning decision
(kind + tool + invocation_id + decision digest)
   |
   v
return compact InvocationRef to Temporal
```

If the reasoning activity retries after the compact decision was committed, it reuses that committed decision and does not call the Reasoner again.

If a worker dies after a model/provider returned a decision but **before** that decision was durably committed, the provider may be called again. Only the one committed decision may progress to tool execution. A6.1 does not claim exactly-once model billing.

## Observation reconstruction

A later reasoning activity receives only `ObservationRef` values from Temporal history.

For each reference it loads the invocation ledger and requires:

- COMPLETE state;
- exact invocation ID;
- exact result digest;
- semantically valid stored result.

Only then is the full normalized result exposed to the Reasoner as an `agentloop.Observation`.

## Capability drift

Every reasoning activity resolves the current runtime again through `BoundRunResolver`.

It must reproduce:

- the original run ID;
- conversation ID;
- run fingerprint;
- mission digest.

If MCP capabilities, workspace binding, input binding, policy binding, or mission change, reasoning stops fail-closed rather than silently continuing under a new agent definition.

## Caller-disconnect recovery

The A6.1 integration proof deliberately:

1. starts agent execution through Agent Server;
2. waits until reasoning step 1 is executing;
3. cancels the caller request;
4. lets the Temporal workflow continue;
5. calls `/execute` again with a fresh request;
6. reconciles to the existing agent workflow;
7. observes the same committed step 1;
8. executes `data.profile` once through the durable tool plane;
9. feeds the observation into step 2;
10. returns FINISH.

The proof requires step 1 and step 2 to each call the Reasoner exactly once.

## What is durable

**KNOWN**

- agent workflow identity;
- execution binding;
- committed structured decisions;
- tool invocation identity;
- tool arguments and results in PostgreSQL;
- tool execution through Temporal;
- compact observation references;
- continuation after caller disconnect;
- bounded max-step state.

## Explicit boundaries

**Model call acknowledgement**

A model/provider call can be repeated if its response was produced but the reasoning activity died before the decision commit. No uncommitted decision may execute a tool.

**Terminal event projection**

The Temporal workflow result is durable. Agent Server currently projects `run.completed / run.waiting / run.failed` into its event stream after it observes that result. A response loss after that event append may cause a later retry to append another terminal projection. Consumers must treat the durable agent workflow/run identity as the execution identity. Atomic terminal-event projection is a separate hardening boundary.

**Mutating tools**

A6.1 inherits the A5.1/A6 read-only restriction. Mutating tools remain unsupported by this retry model.

## Next boundary

With reasoning and tool execution both durable, the next useful slice is not another runtime abstraction. It is a **real model-provider adapter** implementing the existing `agentloop.Reasoner` contract, while preserving:

```text
structured decisions only
no chain-of-thought persistence
bound tools only
conditional context exposure
durable decision commit
```
