# Release Engineer GitHub Effect v1

This milestone wires the first consequential Release Engineer effect through
the runtime's durable plan, admission, provider-effect, observation, and
recovery boundaries.

The mutating path is intentionally **not** part of the A6/A6.1 read-only MCP
tool plane. A model may propose the effect in a plan, but it never receives
GitHub write authority.

## Normal path

```text
durable Event
    ↓
PLANNING
    ↓
exact model Plan bytes + SHA-256
    ↓
durable BindPlan
    ↓
WAITING_FOR_ADMISSION
    ↓
fresh signed Admission
(agent + event + worker + lease epoch + plan digest)
    ↓
EXECUTING
    ↓
GitHub PR observer
    ↓
ABSENT
    ↓
guarded GitHub create
    ↓
fresh GitHub observation
    ↓
APPLIED_ONCE
    ↓
VERIFYING
    ↓
SLEEPING
    ↓
runtime Complete → DONE
```

The handler accepts one bounded v1 effect only:

- tool: `github.open_pull_request`;
- action: `open_pull_request`;
- exact repository owner/name;
- base ref;
- head ref;
- expected head commit SHA;
- title/body/draft arguments.

The stable provider effect ID is `<event-id>:<plan-effect-id>`.

## Admission binding

`BeginRemoteExecution` obtains new externally signed authority for the current
worker and lease epoch. The durable store independently verifies signature,
current governance witness, exact plan digest, live lease, and expiry before
entering `EXECUTING`.

The Release Engineer handler then additionally requires:

```text
admission.target  = github:<owner>/<repo>
admission.profile = github-pr-effect-v1
```

A target/profile mismatch is not allowed to reach GitHub and moves the active
execution to `REVOKED`.

Persisted admission is audit evidence. It is never reused after ownership
changes.

## Provider effect boundary

The existing `internal/effects/githubpr` adapter remains the GitHub boundary.

Before creation it verifies the admitted head SHA. After creation, or after an
ambiguous transport outcome, GitHub is observed again. HTTP success alone is
not treated as proof that the effect is closed.

Observation states remain:

- `APPLIED_ONCE`;
- `ABSENT`;
- `UNKNOWN`;
- `DIVERGENT`.

`UNKNOWN` and `DIVERGENT` never grant retry authority.

## Takeover and durable reconciliation

If a worker loses its lease while lifecycle state is `EXECUTING`, the next
owner receives the work as `UNKNOWN`.

The new worker observes GitHub before doing anything else.

### Effect already happened

```text
UNKNOWN
   ↓
GitHub = APPLIED_ONCE
   ↓
durable EffectResolution(APPLIED_ONCE)
   ↓
VERIFYING
   ↓
SLEEPING
```

No new admission is requested and no second GitHub create is attempted.

### Effect provably did not happen

A proof-grade `ABSENT` observation makes another attempt eligible, but it does
**not** itself authorize execution.

```text
UNKNOWN
   ↓
GitHub = ABSENT
   ↓
durable EffectResolution(ABSENT)
   ↓
WAITING_FOR_ADMISSION
   ↓
NEW signed admission
bound to current worker + current lease epoch
   ↓
EXECUTING
   ↓
GitHub effect
```

This closes a lifecycle gap discovered while wiring the provider. The old
admission cannot be reused by the takeover worker.

### Still uncertain or conflicting

```text
UNKNOWN
   ↓
UNKNOWN | DIVERGENT
   ↓
remain UNKNOWN
   ↓
no create
```

## EffectResolution contract

Leaving `UNKNOWN` is not a generic lifecycle transition.

Only the specialized durable `ResolveUnknown` store operation can do it, and
the proof must contain:

- exact effect ID;
- `APPLIED_ONCE` or `ABSENT`;
- current worker ID;
- current lease epoch;
- exact durable plan digest;
- SHA-256 digest of the provider observation evidence;
- observation timestamp not in the future.

The proof is persisted in both FileStore and PostgreSQL.

Therefore callers cannot perform:

```text
UNKNOWN → WAITING_FOR_ADMISSION
```

or:

```text
UNKNOWN → VERIFYING
```

through the generic `Transition` API.

## Executable evidence

The repository test suite exercises both FileStore and PostgreSQL resolution
semantics, including:

- takeover from `EXECUTING` to `UNKNOWN`;
- `APPLIED_ONCE → VERIFYING`;
- `ABSENT → WAITING_FOR_ADMISSION`;
- fresh admission under the takeover worker and incremented lease epoch;
- rejection of stale-epoch resolution;
- rejection of wrong-plan resolution;
- rejection of future-dated observation evidence.

The Release Engineer integration tests compose:

- durable FileStore lifecycle;
- the real HTTP `governance.Service`;
- the real `governance.Client` and verifier;
- the real GitHub PR adapter;
- a GitHub-compatible HTTP test server.

They prove three schedules:

1. normal admission → create → observe → verify → complete;
2. takeover after the PR already exists → observe and close with zero second
   create and no new admission;
3. takeover after the old worker was admitted but died before the GitHub call →
   prove absence, persist the proof, obtain fresh admission for the new lease
   epoch, then create exactly once.

CI runs the full repository under `go test -race ./...` with PostgreSQL 16.

## Boundary and non-claims

This milestone does **not** claim generic exactly-once GitHub execution.

GitHub does not expose a transaction that atomically combines the runtime's
lease check with Create Pull Request. Therefore simultaneous split-brain
writers are outside the proven boundary. The runtime minimizes that risk with
lease-bound authority and provider observation, but does not rename the
remaining race as exactly-once.

The integration tests use a GitHub-compatible local HTTP server rather than
writing to a real GitHub repository. The provider adapter itself is tested
against the exact REST-shaped contract; production credentials and a deployed
Release Engineer remain separate deployment work.
