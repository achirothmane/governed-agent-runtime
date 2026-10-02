# Lease-Bound Agent Lifecycle v0

Persistent agent state is now fenced by the same ownership token that owns the durable work item.

A worker cannot advance an agent execution merely because it previously owned the task. Every lifecycle mutation must present the currently valid lease owner and lease epoch before the deadline.

```text
durable event
    ↓
claim
    ↓
LEASE(worker=A, epoch=1)
    ↓
SLEEPING → WAKING → PLANNING → WAITING_FOR_ADMISSION → EXECUTING
                                              │
                                      lease expires
                                              ↓
                                worker B takes ownership
                                              ↓
                                     epoch 1 → epoch 2
                                              ↓
                            EXECUTING is rewritten to UNKNOWN
                                              ↓
                              VERIFYING → SLEEPING → DONE
```

## Why EXECUTING becomes UNKNOWN on takeover

If ownership is lost while the persisted lifecycle says `EXECUTING`, the new worker cannot know from runtime state alone whether the external effect occurred immediately before the previous worker disappeared.

The runtime therefore does not replay the effect and does not declare success. It records `UNKNOWN`, forcing reconciliation through `VERIFYING`.

## Invariants

1. Lifecycle state mutations require the current lease owner and epoch.
2. An expired lease cannot mutate lifecycle state.
3. A superseded worker cannot mutate state after takeover.
4. Takeover from `EXECUTING` becomes `UNKNOWN`.
5. Work cannot be marked `DONE` while lifecycle state is active or unknown.
6. Completion requires the lifecycle to return to `SLEEPING`.
7. The lifecycle state and its monotonic version are persisted with the durable work record.

This binds worker ownership to agent progress before any model or tool execution is added.
