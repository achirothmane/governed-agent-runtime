# PostgreSQL Durable Store v0

PostgreSQL is the first multi-process backend for the runtime `Store` contract.

The database persists immutable events, work state, agent lifecycle state, lease owner, monotonic lease epoch, deadline, and completion evidence.

## Claim path

```text
PENDING or expired LEASED
        ↓
SELECT ... FOR UPDATE SKIP LOCKED
        ↓
single transactional UPDATE
        ↓
LEASED(worker, epoch + 1, deadline)
```

Concurrent workers therefore race at the database boundary rather than in process memory.

If an expired record is still in `EXECUTING`, takeover atomically changes the lifecycle to `UNKNOWN` while advancing the lease epoch. The new worker must reconcile before completion.

## Fencing

Lifecycle transitions and completion validate:

```text
event_id
+ worker_id
+ lease_epoch
+ deadline
```

A superseded owner cannot mutate lifecycle state after a newer epoch exists.

## Time representation

The store persists contract timestamps as Unix nanoseconds supplied by the runtime clock. This avoids database timezone interpretation and preserves event timestamp precision across restart.

## CI proof

The integration suite runs against a real PostgreSQL service and exercises:

- duplicate event idempotence and conflicting-ID rejection;
- concurrent claim with one owner;
- lease persistence and renewal;
- expired-owner takeover;
- `EXECUTING → UNKNOWN` recovery;
- stale-owner fencing;
- reconciliation before `DONE`.

The Linux file store remains a reference single-host backend. PostgreSQL becomes the production-oriented durable coordination backend.
