# Governed Agent Runtime

A persistent agent runtime for AI workers whose real-world effects are governed outside the model.

The runtime owns agent identity, lifecycle, durable work, model/tool integration, and recovery. It does **not** let a model execute effects directly. Proposed effects must cross an external governance boundary before they can reach a real system.

## Core boundary

```text
event
  ↓
persistent agent
  ↓
plan
  ↓
proposed effect
  ↓
governance boundary
  ↓
admission + execution authority
  ↓
effect boundary
  ↓
real system
  ↓
receipt + observation + reconciliation
```

This repository is intentionally separate from Aegis-EGE, EASL, assumption-gate, and token-governance-protocol. Those remain independent primitives and are consumed through explicit contracts.

## First vertical slice

The first target is a persistent release engineer that wakes on GitHub events, diagnoses CI failures, prepares a repair branch, runs verification, and opens a pull request. Merge and deployment authority stay outside the agent.

## Status

Implemented and tested:

- Durable event ownership, fenced takeover and lease-bound agent lifecycle.
- File and transactional PostgreSQL stores.
- Model plans bound to exact persisted bytes and SHA-256 digests.
- Externally signed admission verified against a host-configured trust root.
- Authenticated HTTP admission issuance and live witness revalidation.

The admission service is exercised over real loopback HTTP in file/PostgreSQL
integration tests. It uses a bounded volatile issuance ledger and host-owned
policy callbacks. It is not a deployed production authority service, and the
release engineer does not yet dispatch GitHub tools.

See [Aegis admission](docs/aegis-admission.md) and
[remote admission](docs/remote-admission.md) for contracts and proof limits.

