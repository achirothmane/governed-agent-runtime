# Remote admission transport

The host-configured admission service exposes POST /v1/admissions and
POST /v1/admissions/current. The runtime supplies exact durable plan bytes and
the current worker identity/lease epoch, not evidence, policy or authority.
The host Authorize callback evaluates the request against trusted policy and
returns target/profile, current governance witnesses and finite expiry.

The issuer signs the exact document using its configured Ed25519 key. The
runtime verifies that signature against its independently configured trust
root, then queries the authenticated current-witness endpoint while holding
durable ownership. A revoked authority prevents execution even after issuance.
BeginRemoteExecution completes this round trip for either durable store.

The service has a bounded volatile issuance ledger. Restart fails closed for
previous admission IDs; no automatic resurrection or silent reissue occurs.
This is not yet a durable production issuer. Host callbacks must connect to
trusted policy/evidence/authority state; there is no default allow policy.

Remote endpoints require HTTPS; plain HTTP is restricted to numeric loopback
addresses for local tests. The client rejects redirects, bounds response size
and uses a finite timeout. Authentication credentials are host configuration.
The handler bounds request size and rejects unknown fields/trailing documents.
Production hosting must supply TLS and server/request timeouts.

## Proven scope

Tests use a real loopback HTTP server and independent client/issuer keys with
both file and PostgreSQL stores. They demonstrate admission, authentication
denial, revocation between issuance and entry, and fail-closed issuer restart.
They do not prove deployment, an existing Aegis daemon integration or external
tool execution. Aegis's existing domain permits have a different wire contract;
this service is an explicit runtime-admission host built on its shared module.
