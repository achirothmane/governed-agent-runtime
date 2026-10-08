# Dots Decision Contract v0.1

D3 separates **reasoning output** from **execution authority**.

A model or deterministic reasoner may emit a candidate decision, but the runtime accepts it only through `dotdecision.Validate`.

Every decision is bound to:

- the exact Portfolio Context `snapshot_digest`;
- one READY `work_item_id`;
- a requested authority that cannot exceed the work item's admitted authority;
- an explicit decision kind: `PROPOSE_NEXT_GATE`, `ASK_HUMAN`, or `REFUSE`.

Human-final actions such as merge, release/publication, or paid spend cannot be represented as ordinary proposals. They require `ASK_HUMAN`.

The validator is pure: it executes no tools and performs no external action.

A real model-provider adapter is intentionally deferred until this contract passes.
