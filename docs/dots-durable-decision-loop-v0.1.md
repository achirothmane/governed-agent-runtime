# Dots Durable Decision Loop v0.1

D4 composes the sealed Portfolio Context Snapshot from D2 with the typed Decision Contract from D3 and the durable execution substrate from A6.1.

The durable state is deliberately small:

```text
decision_id
snapshot_digest
        │
        ▼
Temporal workflow
        │
        ▼
reason activity
        │
        ├─ load sealed snapshot by digest
        ├─ produce candidate decision
        ├─ D3 validate
        └─ commit validated record to PostgreSQL
        │
        ▼
committed inert decision
```

No portfolio source file is placed in workflow input or decision persistence. The workflow carries only the decision identity and exact snapshot digest.

The committed document contains:

- decision identity;
- snapshot digest;
- work item identity;
- typed decision kind;
- requested authority/action;
- rationale;
- decision digest.

D4 executes no tools and has no effect adapter. A committed decision remains inert data.

Replay uses the same Temporal workflow identity and PostgreSQL decision identity. Once a decision is committed, retry/rejoin returns the same document without invoking the reasoner again.

A real model provider remains deferred. D4 uses a bounded reasoner contract so the durability and authority boundary can be falsified before paid inference enters the loop.
