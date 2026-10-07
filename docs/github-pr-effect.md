# GitHub pull-request governed effect adapter v0

The first real external-effect adapter for the release-engineer vertical slice is
GitHub pull-request creation.

It promotes the bounded protocol proven in
`experiments/trigger-governed-effect/` into the Go runtime codebase without
forking Trigger.dev and without moving GitHub write authority into the model.

## Effect identity

One effect binds:

- repository owner/name;
- exact base ref;
- exact head ref;
- expected 40-character head commit SHA;
- stable runtime `effectId`.

The adapter derives a SHA-256 binding marker and appends it to the pull-request
body:

```text
<!-- governed-agent-runtime:github-pr:v1:<binding-digest> -->
```

The raw effect ID is not embedded in the provider object. Model-supplied body
text may not contain the reserved marker prefix.

## Observation

The observer lists pull requests with `state=all`, constrained by head/base,
then verifies the provider-side binding marker and expected head SHA.

It returns:

- `APPLIED_ONCE`: exactly one bound pull request exists and its current head
  still equals the admitted SHA;
- `ABSENT`: no pull request occupies the target;
- `DIVERGENT`: a target pull request exists without our binding, multiple
  candidates exist, or the bound PR's head SHA has drifted;
- `UNKNOWN`: GitHub could not be queried reliably.

A closed or merged PR is still evidence that the creation effect happened;
`state=all` is intentional.

## Guarded execution

Before POSTing, the adapter reads GitHub's current head ref and compares it to
the admitted SHA. A stale or missing ref is denied.

The create request then uses the exact head/base/title/body/draft values plus
the binding marker.

A successful HTTP 201 means only `DISPATCHED`; the caller must observe GitHub
again before claiming closure.

Transport failures, 5xx responses, and HTTP 422 are ambiguous. In particular,
GitHub documents 422 for validation failure or abuse/rate protection, so this
adapter never turns 422 into retry authority by itself. The next action is
observation, not blind replay.

401/403/404 are treated as denied provider preconditions.

## Proven v0 schedules

The executable corpus covers:

1. no matching PR -> `ABSENT`;
2. one exact provider-side binding -> `APPLIED_ONCE`;
3. existing target PR without our binding -> `DIVERGENT`;
4. bound PR whose head SHA moved -> `DIVERGENT`;
5. stale admitted head SHA -> zero POSTs and `DENIED`;
6. successful create -> provider observation closes the effect;
7. GitHub accepts create but the acknowledgement is lost -> recovery observes
   the already-created PR and performs zero second POSTs;
8. 422 remains ambiguous until a fresh observation;
9. model/body marker injection is rejected.

## Boundary

This adapter does **not** claim generic exactly-once GitHub execution.

The runtime's lease/admission layer must still ensure a single authorized
effect owner. GitHub does not expose a transaction that atomically combines our
runtime lease check with PR creation. Therefore this adapter is safe for the
tested crash/retry schedules, but it does not claim split-brain safety for two
simultaneously authorized writers.

If future work requires that stronger invariant, the next design must introduce
a complete-mediation gateway/fence or prove a provider-native relation that
closes that race. We do not hide that gap with retries or uniqueness claims.
