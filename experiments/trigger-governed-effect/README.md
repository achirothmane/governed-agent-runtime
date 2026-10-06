# Trigger.dev Governed Effect Experiment v0

**Disposition: EXTENSION-FIRST / NO HARD FORK YET**

This experiment asks one bounded question:

> Can the surviving Aegis effect-boundary semantics be composed with Trigger.dev's existing durable waitpoints so that a task retry reconciles a real destination before it can repeat an external effect?

The experiment is isolated from the production Go runtime. It does not modify Aegis-EGE, the existing agent runtime, or Trigger.dev upstream.

## Pinned upstream

- repository: `triggerdotdev/trigger.dev`
- commit: `b983e069851a1e0ed1c558012103bf67117cdb96`
- `@trigger.dev/sdk`: `4.7.3`

`verify-upstream-contract.mjs` downloads the exact pinned source files, reconstructs each Git blob SHA-1, and verifies the waitpoint capabilities this experiment relies on.

## Why extension-first

Trigger.dev already supplies a durable carrier:

- `wait.createToken({ idempotencyKey })`;
- repeated creation returns the original waitpoint while the key is live;
- `isCached` tells the caller that the reservation already existed;
- `wait.retrieveToken()` exposes `WAITING | COMPLETED | TIMED_OUT`;
- `wait.forToken()` durably suspends a run until completion;
- `wait.completeToken()` stores the reconciliation result.

That is enough to test recovery without changing Trigger.dev's run-engine state machine.

## v0 effect protocol

```text
stable effectId
      |
      v
create/recover durable waitpoint
      |
      v
retrieve prior completion
      |
      +---- COMPLETED --------------------> return durable CLOSED receipt
      |
      v
observe destination BEFORE dispatch
      |
      +---- APPLIED_ONCE -----------------> close token; CLOSED
      +---- DIVERGENT --------------------> freeze; no dispatch
      +---- UNKNOWN ----------------------> wait for reconciliation; no dispatch
      |
    ABSENT
      |
      v
destination-native guarded execution
      |
      v
observe destination AFTER dispatch
      |
      +---- APPLIED_ONCE -----------------> close token; CLOSED
      +---- UNKNOWN ----------------------> wait; no retry authority
      +---- ABSENT after dispatch claim --> UNKNOWN, not success
```

An exception during dispatch is treated as ambiguous. The destination is observed again. Only a proof-grade `ABSENT` observation yields `RETRY_ALLOWED`; the helper never performs that second dispatch itself.

## Falsification corpus

The executable tests cover:

1. normal effect: one physical effect, CLOSED only after observation;
2. crash after reservation but before effect: recovery executes once;
3. crash after effect but before waitpoint completion: recovery observes and does not redispatch;
4. lost acknowledgement after physical effect: closes from observation;
5. completed waitpoint replay: cached receipt, zero destination calls;
6. UNKNOWN observation: durable hold, zero execution authority until reconciliation;
7. trusted ABSENT after dispatch error: grants retry authority but does not immediately replay;
8. stale authority/generation: DENIED, zero effect;
9. same effect replay: physical cardinality remains one;
10. divergent physical cardinality: freeze/no dispatch;
11. dispatch says success but observer sees absence: UNKNOWN, never false CLOSED.

## KEEP / ADD / REWRITE / DELETE

| Class | v0 decision |
| --- | --- |
| KEEP | Trigger.dev task retries, waitpoints, waitpoint idempotency, suspend/resume |
| KEEP | Aegis's bounded exact-effect / native-boundary / observation semantics |
| ADD | `governedEffect()` wrapper and a Trigger waitpoint adapter |
| ADD | destination-specific observer + native guarded executor |
| REWRITE upstream | **none** |
| DELETE upstream | **none** |

This is why a hard fork is not justified yet.

## Boundary and non-claims

Only calls explicitly routed through `governedEffect()` receive this behavior. Direct provider calls inside arbitrary task code remain unmanaged.

This experiment is not production evidence. Its waitpoint and destination tests are deterministic contract tests. The upstream source contract is verified against real pinned Trigger.dev source, but no live Trigger.dev server or real PostgreSQL effect is exercised in v0.

## Advancement gate

The next gate is a live PostgreSQL integration using Trigger.dev itself:

```text
Trigger task
  -> create durable governed-effect token
  -> PostgreSQL native guarded effect
  -> kill worker after COMMIT but before token completion
  -> Trigger retry
  -> recover same token
  -> observe exact effect already applied
  -> zero second physical effect
  -> CLOSED
```

Advance toward a fork only if a required invariant cannot be implemented through the public waitpoint/task surface. A fork must be justified by a concrete counterexample, not by preference.
