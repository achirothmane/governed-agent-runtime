# Aegis admission consumer

The runtime imports the independently versioned Aegis-EGE governedaction Go
module at a pinned commit. It uses its exact binding and exclusive expiry
relations without copying kernel implementation.

BeginExecution is the only durable store operation that enters EXECUTING.
Generic Transition refuses this target. BeginExecution locks current ownership,
validates the stored plan bytes, verifies externally signed admission against a
configured Ed25519 trust root, checks current evidence/authority/policy/doctrine
witnesses through a trusted resolver, and persists the exact signed admission
and lifecycle transition atomically.

Admission binds agent, event, worker, lease epoch and SHA-256 plan digest.
Takeover cannot reuse the previous worker's admission. Reentry is rejected.
Loss of ownership during EXECUTING still yields UNKNOWN.

## Trust boundary and limits

The issuer key and current-witness resolver are host configuration, never model
output. Tests have a local signing fixture; production code does not issue
authority. The resolver must return trusted current state, not echo the request.
The boundary clock passed to store operations must be trusted host time.

This change is an executable admission consumer of Aegis shared contracts,
not a deployed connection to an Aegis authorization service. Admission is not
proof that an external effect occurred. No tools are dispatched here. Native
target/profile enforcement, check-to-use fencing, durable effect custody and
receipt reconciliation must be implemented by the eventual Tool Gateway.
Persisted admission is audit material; it is never automatically reused as
execution authority on restart.

Both file and PostgreSQL stores run the same acceptance/rejection corpus.
