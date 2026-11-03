# ANE ActionProof — Delivery and Verification Register

**Doc ID:** ANE-ACTIONPROOF-EXEC-001  
**Status:** Planning only — 2026-11-03  
**Applies to:** first independently sellable Developer Edition.  
**Cross-reference:** ANE_ACTIONPROOF_PRODUCT_BLUEPRINT_v0.1.md

This is an **execution checklist**, not an assertion that the items below are done. Mark each PASS only after recording the exact candidate SHA, command/environment, measured result, artifact link and reviewer. BLOCKED if no evidence; FAIL if measured conditions fail.

## Stage owners, dependencies and completion evidence

| Stage | Depends on | Work packages | Expected output | Gate status now |
|---|---|---|---|---|
| S0 Buyer discovery | None | D0.1 buyer incident dataset; D0.2 substitute comparison; D0.3 paid-intent test | discovery dossier with sources | BLOCKED |
| S1 Product contract | S0 evidence or explicitly logged research risk | P1.1 job map; P1.2 full requirements; P1.3 supported platforms and support terms | signed-off product acceptance contract | BLOCKED |
| S2 Outcome semantics | S1 | C2.1 outcome schema; C2.2 status transitions; C2.3 time/consistency rules; C2.4 negative fixtures | spec, JSON schema and golden samples | BLOCKED |
| S3 Architecture | S2 | A3.1 existing code audit; A3.2 adapter interfaces; A3.3 threat model; A3.4 ADR | reviewed interface/architecture decision | BLOCKED |
| S4 Core | S3 | E4.1 Go SDK; E4.2 verifier; E4.3 evidence; E4.4 CLI; E4.5 storage | running reusable core with tests | BLOCKED |
| S5 Adapters | S4 | I5.1 GitHub; I5.2 reference HTTP; I5.3 optional authorized Shopify | provider E2E and fault evidence | BLOCKED |
| S6 UX and DX | S4+S5 | U6.1 dashboard; U6.2 reports; U6.3 docs; U6.4 clean install | fully usable supported workflow | BLOCKED |
| S7 Assurance | S4–S6 | Q7.1 functional; Q7.2 safety; Q7.3 security; Q7.4 performance; Q7.5 competition | immutable test evidence & defects | BLOCKED |
| S8 Independent review | S7 | R8.1 evidence audit; R8.2 defective controls; R8.3 corrective reruns | signed review and release candidate digest | BLOCKED |
| S9 Shopify delivery | S8 | B9.1 rights/license; B9.2 build artifacts; B9.3 storefront delivery test; B9.4 payment/refund policy | purchase-to-use acceptance | BLOCKED |
| S10 Operations | S8+S9 | O10.1 release; O10.2 support; O10.3 monitoring; O10.4 rollback | public release and runbook | BLOCKED |
| S11 Expansion | Verified adoption | X11.1 demand analysis; X11.2 enterprise/privacy; X11.3 scientific benchmark | separately gated new release | BLOCKED |

The gates can be revisited in any order for prototypes; a stage is not COMPLETE until all its required promises have evidence. Initial market investigation need not prevent harmless fixture development but must precede commercial scaling.

## Acceptance matrix: truth table

| Test ID | Stimulus | Expected verdict / behavior | Proof to retain |
|---|---|---|---|
| T-001 | Provider returns HTTP 200 but external value unchanged | REFUTED on qualifying authoritative observation; never VERIFIED | provider response + authoritative before/after read |
| T-002 | Action applied, acknowledgment lost | reconcile to actual observed state without new mutation | effect ID, provider record, mutation counter |
| T-003 | External read unavailable or permission denied | UNKNOWN / REVIEW_REQUIRED; no automatic repeat | error and no-second-write evidence |
| T-004 | Provider read is stale within defined consistency window | OBSERVING/UNKNOWN until allowed window; no premature final success | timestamps, contract window, event log |
| T-005 | Duplicate delivery | one bound effect within proved supported schedule; no broad exactly-once claim | two dispatch attempts and provider mutation count |
| T-006 | Contradictory authorized observations | UNKNOWN/REVIEW_REQUIRED until contradiction resolved | both observations with provenance |
| T-007 | Only 1 of 2 required predicates met | PARTIAL, not VERIFIED | predicate-by-predicate result |
| T-008 | Changed objective contract after verdict | invalidate/reopen affected verdict; prior evidence preserved | contract versions and verdict history |
| T-009 | Unauthorized actor attempts compensation | DENY; no external mutation | policy decision and provider read |
| T-010 | Malicious instruction embedded in observed content | treated as untrusted data; no new tool execution | adversarial fixture and call trace |
| T-011 | Evidence from another tenant reused | hard reject; no data leak | tenant isolation test |
| T-012 | Checksum of archived evidence changed | report integrity failure; do not trust corrupted bundle | original/new digests and verifier |
| T-013 | Slow provider or rate limit | bounded backoff and transparent state; no unsupported success | retries, timing, outcome |
| T-014 | Operator contests verdict | revised evidence/decision with traceability; original not erased | two adjudications and reviewer authority |
| T-015 | Clean user install | end-to-end verified job with documented platform | clean environment log and user steps |
| T-016 | Checkout and digital handoff | successful test purchase, asset receipt, installation and terms | checkout records, delivery record, activated package |
| T-017 | Deliberately defective candidate | required assurance check rejects it | independent challenge output |

## First engineering increment (NOT a diminished commercial scope)

- Inspect the runtime's SDK, effect adapter and storage ownership **before** changing contracts.
- Author formal outcome contract fixtures and a reference verifier for an authorized, low-risk effect.
- Prove T-001, T-002, T-003, T-005 with deterministic fixtures and at least one actual owned adapter.
- Record any API gap as an ADR; reuse existing runtime behavior rather than duplicate it.
- Then expand toward complete first-edition requirements, including UX, packaging and real buyers.

This increment is a testable internal vertical slice; **it is not saleable** until all contracted first-edition functions pass.

## Evidence record template

    Requirement ID:
    Candidate commit SHA:
    Environment / dependency versions:
    Input / fixture:
    Expected:
    Observed:
    Test command:
    Raw artifact path:
    Fault / non-happy-path tested:
    Reviewer / independence:
    Verdict: PASS | FAIL | BLOCKED
    Known limits:
    Next corrective action:

## Decision rights

- Implementation lead may propose changes but cannot relabel failed checks as passed.
- Buyer/problem evidence is assessed separately from technical pass status.
- Product owner authorizes feature promises, public claims, price, refunds and release.
- Independent reviewer must have access to raw results and negative controls.
- Shopify storefront publication is a separate commerce gate. Shopify App Store submission is a distinct future project with its own official compliance verification.

## Stop rules

Do not release a paid downloadable package on Shopify if: unverified advertised core capabilities, critical/high issues, unproven clean installation, unresolved data leakage, unsafe retry claims, incomplete licensing rights, broken payment/delivery path, absent support terms, or misleading competitive/revenue claims.

Planning evidence checked in this step: repository READMEs, documented versioned internal QMS, official current Shopify docs, and current provider documentation. **No code or live-provider tests were performed for ActionProof during drafting.**
