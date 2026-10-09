# Dots Data Reconciliation Bridge v0.1

Status: **candidate until cross-repository CI passes**

This slice moves the Portfolio Dot from a map-only concept toward an executable coordinator over already-governed capabilities.

## Path under test

Portfolio Dot agent binding -> explicit AgentSpec -> immutable MCP capability snapshot -> Temporal durable tool workflow -> PostgreSQL invocation ledger -> real Data Engine MCP -> data.reconcile -> MATCH | DIVERGED | UNKNOWN.

## Authority

The Data Engine provider admits tools from the explicit AgentSpec.RequiredTools list only when each name is also present in the runtime-owned allowlist.

Remote MCP discovery never expands Dot authority automatically.

Currently admitted Data Engine tool names:

- data.profile
- data.reconcile

Both are read-only at the durable retry boundary.

## Proof requirements

The integration test uses the real Data Engine MCP endpoint and proves:

1. the run binds an immutable capability snapshot;
2. data.reconcile is bound only because the Dot AgentSpec explicitly requires it;
3. Temporal executes it durably;
4. PostgreSQL records invocation and result digest;
5. same invocation ID reuses the durable result;
6. DIVERGED evidence is preserved;
7. raw reconciliation evidence is not copied into Agent Server events;
8. unsupported comparison returns UNKNOWN;
9. the runtime does not reinterpret UNKNOWN.

## Boundary

This bridge does not authorize Dots to repair data, launch migrations, approve cutover, merge, publish, spend, or introduce hard dependencies.
