# Durable Event Loop v0

The runtime must survive worker loss without granting two workers valid ownership of the same work at the same logical time.

## Contract

```text
event
  ↓
durable enqueue
  ↓
PENDING
  ↓ claim
LEASED(worker, epoch, deadline)
  ├─ renew → same epoch, later deadline
  ├─ complete before deadline → DONE
  └─ deadline expires
       ↓
     claim by another worker
       ↓
     epoch + 1
```

The monotonically increasing lease epoch is a fencing token. A worker that returns after its lease expired cannot complete work after a newer worker has taken ownership.

## Recovery properties proven in this milestone

- event delivery is idempotent when the same event ID carries the same immutable content;
- reusing an event ID with different content fails closed;
- work survives reopening the durable store;
- a second worker cannot claim work before the current lease expires;
- an expired lease can be taken over after restart;
- takeover increments the fencing epoch;
- a stale worker cannot complete after takeover;
- completed work never becomes claimable again;
- corrupt persisted state is an error, not an empty queue;
- a handler failure leaves leased work recoverable after lease expiry instead of silently marking it complete.

## Scope

The Linux file store is a reference durability implementation for the contract and test corpus. It uses an advisory process lock, atomic replacement, file fsync, and directory fsync. It is not the intended multi-node production backend.

A PostgreSQL backend can implement the same `Store` contract using transactional row locking / compare-and-swap while preserving lease epochs. The agent lifecycle, model providers, tools, governance admission, and real-world effects remain outside this milestone.
