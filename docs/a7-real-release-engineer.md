# A7 — Real Release Engineer GitHub E2E

A7 moves the Release Engineer proof from a GitHub-shaped local HTTP server to
GitHub itself.

The milestone deliberately separates two claims:

1. real-provider **recovery** from an already-applied effect;
2. real-provider **creation + lost acknowledgement + takeover**.

These are not treated as equivalent.

## A7.1 — real provider recovery

Status: **PASS**

The proof uses a real pull request in this repository carrying the exact
`githubpr` provider binding marker. The runtime then reconstructs the state
that exists after a worker was admitted, entered `EXECUTING`, and disappeared
before persisting the provider outcome.

The next worker:

```text
expired EXECUTING lease
        ↓
takeover
        ↓
UNKNOWN
        ↓
real GitHub Observe
        ↓
APPLIED_ONCE
        ↓
durable EffectResolution
        ↓
VERIFYING
        ↓
SLEEPING
        ↓
DONE
```

The passing live run used **read-only GitHub permissions**:

- contents: read;
- pull requests: read.

That matters because the recovery result was not produced by a hidden write
capability.

The executable assertion proves:

- the real GitHub provider object is recognized by the adapter;
- the provider binding marker matches the exact effect/repository/base/head/SHA
  tuple;
- takeover moves `EXECUTING → UNKNOWN`;
- `APPLIED_ONCE` closes UNKNOWN through durable `EffectResolution`;
- no second admission is issued;
- no replay/create call is required.

Live evidence:

- GitHub Actions run: `37740404288`;
- result: `PASS`;
- log assertion:
  `A7 provider recovery PASS: real GitHub APPLIED_ONCE closed UNKNOWN without reauthorization or replay`.

The fixture PR was closed after the proof and its branch was reset to `main`.

## A7.2 — real create + lost ACK

Status: **BLOCKED_BY_PLATFORM_POLICY**

The stronger test is implemented in
`TestRealGitHubReleaseEngineerLostAckTakeover`.

It creates an ephemeral branch and commit, enters the real Release Engineer
handler, and wraps the GitHub HTTP transport so that:

1. GitHub receives Create Pull Request;
2. the HTTP response would be `201 Created`;
3. the transport discards that acknowledgement;
4. the worker process exits immediately;
5. a second worker must recover through GitHub observation without creating a
   second PR.

The repository's Actions token exposes:

- contents: write;
- pull requests: write.

However GitHub returned:

```text
DENIED
GITHUB_CREATE_PULL_HTTP_403
```

before the lost-ACK injection point.

Observed live runs:

- `37739823090` — provider returned 403;
- `37739963835` — repeated with explicit execution diagnostics; same 403.

Therefore A7 does **not** claim that the real lost-ACK create schedule has
passed.

The repository/operator setting that permits GitHub Actions to create pull
requests is an external precondition for this stronger gate. The test remains
in the repository and can be rerun once that policy permits the operation.

## Reusable workflow

`.github/workflows/a7-real-release-engineer.yml` is manual-only.

It exposes two modes:

### provider-recovery

Requires an already-created governed PR and accepts:

- `precreated_head`;
- `precreated_sha`;
- `precreated_event_id`.

It runs with read-only GitHub permissions.

The provider marker must have been derived from:

```text
github-pr-effect-v1
<event-id>:open-pr
<owner lowercase>
<repository lowercase>
main
<head branch>
<head SHA lowercase>
```

joined with newlines and SHA-256 hashed into:

```text
<!-- governed-agent-runtime:github-pr:v1:<digest> -->
```

### lost-ack-create

Creates its own ephemeral branch/commit/PR and injects a process crash after the
provider accepted the create but before the runtime can reconcile it.

It requires write permissions and additionally requires repository policy to
allow Actions-originated pull-request creation.

## Boundary

A7.1 is real-provider evidence for recovery only.

It does not upgrade the system to generic exactly-once semantics and does not
close the simultaneous split-brain race documented by the GitHub effect
adapter.

A7.2 remains the gate for the stronger real-provider lost-ACK claim.
