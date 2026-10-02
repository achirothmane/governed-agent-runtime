# Agent Contract v0

The runtime treats an agent as a durable identity with a mission, explicit tool capability references, governance policy reference, budget reference, and lifecycle state.

## Boundary

An agent may reason about work and propose an effect. It does not acquire real-world execution authority merely because a model produced a plan.

```text
CREATED
  ↓
SLEEPING
  ↓ event
WAKING
  ↓
PLANNING
  ↓
WAITING_FOR_ADMISSION
  ↓
EXECUTING
  ↓
VERIFYING
  ↓
SLEEPING
```

Fail-closed states:

- `BLOCKED`: work cannot currently proceed.
- `REVOKED`: previously available execution authority is no longer valid.
- `UNKNOWN`: the runtime cannot yet prove the real-world outcome.

`UNKNOWN` may only move back through `VERIFYING` before the agent can sleep again. `BLOCKED` and `REVOKED` do not automatically resume in v0.

## Invariants

1. The runtime must not permit `PLANNING -> EXECUTING`; admission is mandatory.
2. A terminal fail-closed state cannot automatically return to normal work.
3. An unknown outcome cannot be treated as completion.
4. The lifecycle contract contains no provider-specific, GitHub-specific, browser-specific, or business-domain semantics.
5. A model plan is data, not authority.

Later milestones will bind these states to durable events, execution leases, Aegis-EGE admission, receipts, observation, and reconciliation.
