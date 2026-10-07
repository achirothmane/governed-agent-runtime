# Temporal Durable Backend A2

A2 makes Temporal a pluggable durable-execution backend behind the public `sdk.ExecutionBackend` contract. Domain engines and agents do not import Temporal directly.

## Version boundary

The runtime remains on Go 1.25. Temporal Go SDK v1.49.0 raised its minimum Go version to 1.26, so A2 pins `go.temporal.io/sdk v1.48.0`, whose module declares Go 1.25.4. We do not raise the runtime toolchain solely to adopt a newer adapter dependency.

## Identity and binding

The runtime `RunID` is used as the Temporal Workflow ID. Every start binds the exact `sdk.RunRequest` using its SHA-256 runtime fingerprint in two places:

- the workflow input (`WorkflowInput.Fingerprint`);
- Temporal Memo (`ai_native_runtime_fingerprint`).

The runtime RunID is also stored in Memo as `ai_native_runtime_run_id`.

`Inspect` refuses to map Temporal state unless these memo bindings can be decoded. Missing or malformed binding evidence yields `UNKNOWN` plus `ErrUnprovenBinding`.

## Duplicate-start recovery

A2 configures:

- `WorkflowExecutionErrorWhenAlreadyStarted = true`;
- `WorkflowIDReusePolicy = REJECT_DUPLICATE`.

If a start acknowledgement is lost and the caller retries, an already-started error triggers observation instead of a second start. The existing execution is accepted only if its stored fingerprint equals the requested fingerprint. A mismatch yields `ErrBindingMismatch`.

## State mapping

After binding proof:

- RUNNING -> `RUNNING`
- COMPLETED -> `SUCCEEDED`
- FAILED / TIMED_OUT / TERMINATED -> `FAILED`
- CANCELED -> `CANCELED`
- CONTINUED_AS_NEW -> `UNKNOWN`
- unrecognized states -> `UNKNOWN`

`CONTINUED_AS_NEW` is deliberately not promoted to success or running in A2. A future slice may support workflow-chain continuity only after the runtime can prove that the binding survives the chain transition.

## Signals and cancellation

Signals target the latest execution for the stable Workflow ID. Cancellation also targets the latest execution. Temporal cancellation does not persist the runtime reason string, so the reason remains an audit/runtime-event responsibility rather than being falsely represented as Temporal-native evidence.

## Data converter boundary

By default A2 uses Temporal's default data converter to decode binding Memo values. Deployments using a custom Temporal data converter must configure the same converter on the backend or inspection will fail closed.

## Non-claims

A2 does not yet provide:

- a registered generic worker workflow implementation;
- activity/tool execution mapping;
- MCP transport;
- Agent Server REST/WebSocket APIs;
- proof of binding continuity across Continue-As-New;
- engine-specific workflow definitions.

Those are later slices. A2 proves the durable backend boundary first.
