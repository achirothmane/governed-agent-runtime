# Trigger.dev Governed Effect Experiment v0

**Disposition: EXTENSION-FIRST PASS / HARD FORK REJECTED AT V0**

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

The fork gate is now closed at v0: no required invariant in this experiment needs a Trigger.dev core fork.

## Boundary and non-claims

Only calls explicitly routed through `governedEffect()` receive this behavior. Direct provider calls inside arbitrary task code remain unmanaged.

The experiment now has three evidence tiers:

1. **Pinned upstream contract** — exact Trigger.dev source blobs and SDK 4.7.3 waitpoint semantics are verified in CI.
2. **Native destination proof** — PostgreSQL 16 exercises atomic physical effect + causal receipt, stale-generation denial, authority revocation, state drift, and replay cardinality.
3. **Pinned Trigger RunEngine recovery** — Trigger.dev's real RunEngine is built through its Turbo pipeline and exercised with real PostgreSQL/Redis Testcontainers. Two restart cases pass:
   - RunEngine reconstruction after PostgreSQL COMMIT but before waitpoint completion;
   - a separate effect-worker OS process is killed with `SIGKILL` immediately after PostgreSQL COMMIT and before any waitpoint closure, followed by RunEngine reconstruction and reconciliation with zero redispatch.

The physical ledger deliberately has no `UNIQUE(effect_id)`, so a duplicate physical execution would remain observable rather than being hidden by a uniqueness constraint.

This is strong integration evidence for the bounded protocol, but it is not a generic exactly-once claim. It does not yet prove complete mediation for arbitrary task code, nor a full deployed webapp/supervisor/SDK network path. Direct provider calls that bypass `governedEffect()` remain outside the guarantee.

## Fork rule after v0

Do **not** fork Trigger.dev for this capability. The current public/internal surfaces are sufficient for the tested invariants:

```text
Trigger durable waitpoint
        +
stable effectId
        +
destination-native guarded effect
        +
destination observation/reconciliation
        =
recover after hard worker death without blind redispatch
```

Re-open the hard-fork decision only if a future required invariant is demonstrated to be impossible through the supported task/waitpoint surface. That decision requires a concrete failing counterexample, not architectural preference.

## Next production gate

Move from the generic experiment to one real adapter used by our own system. The adapter must preserve the same contract:

```text
REGISTERED EFFECT
  -> native preconditions
  -> effect + causal receipt
  -> crash/retry reconciliation
  -> CLOSED | UNKNOWN | DIVERGENT | DENIED
```

A provider that cannot supply a trustworthy observer or native replay guard remains unsupported rather than being wrapped with a false exactly-once claim.
