# Durable Plan Binding v0

A model plan becomes eligible for governance only after the exact plan document is durably bound to the current work item.

The model still has no execution authority.

```text
event
  ↓
PLANNING
  ↓
ModelProvider
  ↓
Plan
  ↓
canonical JSON document
  ↓
SHA-256 digest
  ↓
durable BindPlan under current lease
  ↓
WAITING_FOR_ADMISSION
```

## Contract

A binding contains:

- plan ID;
- agent ID;
- event ID;
- exact serialized plan document;
- SHA-256 digest of that document;
- durable bind timestamp.

The store verifies that the digest matches the document and that agent/event identity matches the work record.

## Immutability

The first valid binding wins.

Rebinding the exact same plan is idempotent. Attempting to replace it with a different plan fails closed.

This matters because later governance evidence and approval must refer to the exact model output that was reviewed. A worker cannot obtain admission for plan A and then substitute plan B.

## Lifecycle gate

`PLANNING → WAITING_FOR_ADMISSION` is rejected unless a durable plan binding exists.

Therefore:

```text
model output in memory
        ≠
admissible work

durably bound exact plan
        ↓
may enter governance admission
```

## Recovery

The plan binding survives worker restart and lease takeover. If execution later becomes uncertain, reconciliation still has the exact plan document and digest that preceded the effect.

The next milestone can bind governance decisions and evidence to this digest rather than to mutable agent memory.
