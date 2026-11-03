# ANE ActionProof — Full Product Blueprint

**Document ID:** ANE-ACTIONPROOF-PRD-001  
**Version:** 0.1 (working draft)  
**Date:** 2026-11-03  
**Owner:** AI Native Engineering  
**Status:** DRAFT — NOT A RELEASE / NOT MARKET-VALIDATED  
**Primary delivery:** independently licensable developer product; distribution through the owner's Shopify store only after release approval.  
**Theoretical background:** Institutional Epistemic Correctability Theory (IECT), unvalidated research program. IECT is not a substitute for tests, operational controls or independent evidence.

## 0. Executive decision and product promise

**Promise:** For a defined external action performed by an AI agent or automation, ANE ActionProof will bind an explicit expected outcome to an independently sourced observation, return a defensible result, preserve minimum necessary evidence, and control ambiguous retries or corrective actions without silently claiming success.

This is a **product plan**, not a claim of implementation. A successful API response, a completed workflow, an LLM statement or an uploaded screenshot is not sufficient proof of the intended business outcome.

**Commercial thesis (unproven):** Developers and operators will pay for auditable outcome verification where existing logging, retries and observability leave a consequential uncertainty. Hypothesized initial price: **USD 99 per individual license, one time**, subject to market validation, actual licensing terms, payment and tax review.

**Primary user:** AI agent / workflow developer with a side-effecting tool.  
**Economic buyer:** solo developer, automation team or technical lead responsible for expensive silent failures.  
**First business outcome:** Detect a consequential mismatch between an agent's asserted success and the authoritative external state, and provide a reproducible explanation and safe next action.  
**Not a promise:** generic infallibility, exactly-once execution, legal compliance certification, regulatory assurance, guaranteed refunds, generic causal inference or automatic repair of arbitrary systems.

## 1. Existing-assets audit and anti-duplication rule

Examined repository documentation on 2026-11-03:

- governed-agent-runtime: Go SDK, durable runtime identity, fingerprinting, temporal adapter, MCP binding, agent server, PostgreSQL store and bounded GitHub effect adapter. The GitHub adapter already uses dispatch + observation + reconciliation and handles lost acknowledgements conservatively.
- agent-action-guard: deterministic pre-action policy gate; it does **not** verify the resulting outcome.
- conversion-truth-auditor: one-to-one reconciliation of reported GA4 purchases and actual paid-order evidence; different domain, reusable design insights.
- Dogmatique: Quality Constitution v4.0 working draft; production-ready by design / Shopify release criteria.

**Product architecture decision:** do not recreate a scheduler, general-purpose agent runtime, generic governance framework or full telemetry stack. ActionProof is a distinct **outcome-contract and evidence-verdict product layer** on top of the existing owned substrate, published through stable public interfaces. The exact code placement and dependency direction require an ADR and repository-level compatibility review before coding.

Existing code statements are from READMEs and are **not independently re-tested within this planning task**. Reuse is conditional on current test evidence and license/dependency audit.

## 2. Buyer discovery and competition — Stage S0

Questions to answer before commercial commitment:
- Which incident cost money or engineering hours because an agent or automation reported success but the required state did not materialize?
- What source of truth can the buyer access? How do they currently reconcile it?
- Does Langfuse, a custom test, Temporal, Inngest or a simple postcondition query already solve the use case adequately?
- Can a buyer integrate verification in one work session without risky permissions or hosted infrastructure?
- Would the buyer pay USD 99 for the exact completed deliverable?

Required artifacts: 12–20 documented buyer/problem observations, a competitor capability matrix with tested versions, at least three genuine error datasets or simulations labeled as such, quantified replacement friction, and a willingness-to-pay test with attributable provenance.

**S0 gate:** identify a narrow repeatable buyer job, a measurable gap to an available alternative, and a credible access path to buyers. If evidence is absent, maintain RESEARCH status; no commercial-ready claim. Buyer interviews need not involve calls: opt-in asynchronous written feedback, reproducible issue reports, public communities and marketplace search evidence may be used, respecting consent and platform rules.

## 3. Product scope and explicit boundaries — Stage S1

### Complete product vision

1. Objective contracts: explicit expected external state, measurement source, deadline, allowed observation lag, tolerance and impact level.
2. Provenance: actor/run/action identity, tool and version, input digest, policy decision, provider receipt, observation event, verifier version and evidence digests.
3. Outcome verifier: deterministic postconditions whenever possible; independently read provider state; support time-window and eventual-consistency rules.
4. Verdict engine: distinguish verified success, demonstrated mismatch, insufficient evidence, policy/permission block, and partial/late outcomes.
5. Replay and reconciliation: duplicate delivery, lost acknowledgment, crash recovery, stale observations and competing effects.
6. Controlled correction: suggestion, user approval, evidence-based retry eligibility, compensation where technically reversible, escalation otherwise.
7. Human review and contestation: annotations, challenge/reopen, versioned decision history and auditable reviewer authority.
8. Developer integration: stable Go API/SDK and CLI first; language adapters through versioned protocol; local/self-hostable option.
9. Professional user interface: configuration, runs, evidence inspection, timeline, incident classification, review and export; accessible states and clear uncertainty.
10. Reports: JSON plus legible HTML/Markdown evidence bundle, signed manifest when a verified signing authority exists.
11. Operational controls: observability, alerts, redaction, retention, secret isolation, access control, backups and recovery.
12. Commercial product: docs, examples, license handling, packaging, buyer onboarding, support, release notes and reproducible installation.
13. Extensions: selected integrations for GitHub, Shopify, n8n/webhooks and other agent stacks, through well-scoped adapters.
14. Enterprise path if demand is proven: policy packs, multi-tenant administration, SSO/RBAC, security audit logs, and deployment options.

### First independently sellable edition

**ActionProof Developer Edition** must completely deliver the core promised job in its stated supported contexts: one developer can install, define an outcome, run observation, obtain a truthful verdict/evidence, review a failed/unknown outcome, and make a controlled retry/repair decision. All claimed adapters must pass real integration tests. The first edition is **not** a stub, trial-only demonstration or unfinished enterprise platform.

**Initial adapter candidates:** existing GitHub PR creation/observation (best owned substrate), HTTP/webhook outcome contract with a controllable reference service; Shopify product/price state observation as an optional adapter only after authorized access and relevant API testing. A mock adapter proves contract semantics, not production integration.

**Out of scope for the first edition:** autonomous refunds or money movement, broad universal-agent compatibility, unapproved cloud hosting, full enterprise SSO, binding legal attestations, and guarantees of an unobserved real-world effect. These remain explicit future tracks, not hidden promises.

## 4. Formal contract and finite state model — Stage S2

A verification record must minimally include:
- objective_id, run_id, effect_id, target identity and tenant boundary;
- expected state predicate and deterministic comparison semantics;
- authorized action identity and policy reference;
- observation source identity/authority, observed_at and observed revision;
- observation window and consistency contract;
- evidence references, digests, confidence/coverage qualifier;
- verdict, reason codes, verifier version, adjudicator and history pointer.

**Outcome verdicts** (do not conflate with execution states):
- VERIFIED: required postcondition established by qualifying evidence within contract.
- REFUTED: qualifying evidence establishes a mismatch.
- UNKNOWN: evidence absent, stale, contradictory, incomplete or inaccessible.
- PARTIAL: some, not all, explicitly required predicates verified.
- NOT_APPLICABLE: evaluation genuinely irrelevant under a documented approved condition.

**Execution states:** PROPOSED -> AUTHORIZED or DENIED -> DISPATCHED -> OBSERVING -> CLOSED / REVIEW_REQUIRED. A denied operation cannot be interpreted as an unsuccessful externally applied operation. Observation may continue after a network timeout. Timeouts do **not** prove absence.

**Retry invariant:** no mutation retry based solely on timeout or non-2xx response; first reconcile the external effect identity against authoritative state, then apply idempotency and explicit authority. For irreversible effects, fail closed and require human intervention where uncertainty remains.

**Causality limitation:** observed-after-action does not by itself prove the action caused the effect. Product claims should use language such as 'postcondition observed' or 'outcome verified against defined source'; stronger attribution requires independent causal evidence.

## 5. Engineering architecture — Stage S3

    Client Agent / Workflow
           |
      ActionProof SDK / CLI / API
           |
      Objective Contract + Policy Boundary
           |
      Existing Governed Runtime (run identity, durable scheduling, effect control)
           |
      Provider Adapter ----> External System
           |
      Independent Observer ----> Authoritative State
           |
      Evidence Store (append-only logical history, digests, bounded PII)
           |
      Verification / Reconciliation Engine
           |
      Verdict + Evidence Report + Human Review
           |
      Authorized Correction / Escalation

**Language/stack proposal:** Go library and CLI aligned with existing Go runtime; TypeScript UI and typed client when needed; PostgreSQL for server installation, minimal local embedded store only if proven necessary; OpenTelemetry-compatible trace correlation (without treating traces as truth). Avoid paid LLM API dependence for core verification. Deterministic evaluation remains functional offline given observation inputs.

**Security architecture:** least privilege, tenant-isolated identifiers, allowlisted provider targets, no provider credentials in exports, server-side policy enforcement, secrets management, bounds on all inputs, authenticated webhook validation, retention/deletion policy, signed or separately anchored evidence where tamper-resistance is promised, and authorization revocation. A hash chain by itself does not prove honest external state.

**Ownership:** ActionProof owns outcome contracts, verdict semantics, evidence bundles, acceptance corpus and user experience. Runtime owns scheduling/recovery and authorized effects. Adapters own provider-specific observation contracts. No duplicate effects framework.

**ADR requirements:** API boundaries, runtime coupling, storage migration, chosen verifier language, bounded semantics, privacy/retention, plugin signing strategy, license obligations, Shopify distribution boundaries.

## 6. Detailed stage plan with acceptance gates

| Stage | Work | Deliverable | Gate (must be proven, not declared) |
|---|---|---|---|
| S0 Discovery | Buyer, problem, alternatives, pricing evidence | Market evidence dossier | Real buyer/job and credible differentiated gap |
| S1 Product definition | Full vision, scope, persona, promises, non-goals | PRD + contractual acceptance | All advertised features map to user outcomes and tests |
| S2 Formal semantics | Objective schema, verdicts, conflict, temporal rules | Protocol/spec + fixtures | No contradiction between states; unknown is never success |
| S3 Architecture & threats | Dependency inventory, interfaces, threat model, ADRs | Architecture and security plan | Independent review of critical assumptions |
| S4 Core engineering | Engine, immutable outcome contracts, evidence, CLI/SDK | Runnable local core | Deterministic golden tests, corrupted/ambiguous input tests |
| S5 Integrations | GitHub, reference HTTP, approved Shopify adapter | Working connectors + fixtures | Real integration and fault/recovery evidence per advertised adapter |
| S6 UX & packaging | Dashboard, CLI help, reports, documentation, migration | User-facing product | Representative buyer completes full workflow without hidden steps |
| S7 Verification & benchmark | Unit/integration/E2E/security/load/fault and baseline comparison | Evidence-linked verification matrix | Critical acceptance tests pass; no critical/high open defects |
| S8 Independent release review | Reviewer challenges evidence and negative controls | Review report, repaired defects, candidate digest | Required independent checks have valid evidence |
| S9 Commercial delivery | License, release archive, install/update, Shopify purchase, refund/support terms | Buy/download/install/use journey | Real production-like checkout and delivery test for actual artifact |
| S10 Launch & operations | Publication, issue response, analytics, rollback | Release and operational runbook | Release authorized, incident and support paths operational |
| S11 Expansion | Multi-tenant/enterprise features and scientific benchmarking | Evidence-driven new version | Customer adoption and operating economics justify expansion |

Work may iterate between stages; a gate blocks *release claims*, not harmless internal exploration. No fixed completion date is promised without audited implementation effort and available resources.

## 7. Acceptance contract — Stage S1/S7

The following are initial test requirements, subject to versioned approval. Targets are engineering acceptance thresholds, **not measured performance**.

- **AC-001 Integrity:** 100% of previously specified false-success corpus cases must **not** yield VERIFIED. Corpus contains simulated HTTP 200 with unchanged state, lost acknowledgment, stale read and partial state.
- **AC-002 Unknown honesty:** API timeout, malformed authoritative response, missing credential, contradictory evidence and stale snapshot must produce UNKNOWN or an explicitly justified non-success outcome.
- **AC-003 Retry safety:** in the supported crash/retry schedules, zero duplicate side effects in golden integration corpus; never infer universal exactly-once semantics.
- **AC-004 Provenance:** every VERIFIED verdict has a resolvable observation source, timestamp, contract, verifier version and integrity-checked evidence reference.
- **AC-005 Authorization:** correction/compensation requires current actor permission; direct client mutation cannot bypass server policy.
- **AC-006 Contract versioning:** changing a material predicate, evidence or verifier invalidates/reopens all affected verdicts; immutable prior versions preserved.
- **AC-007 Error recovery:** an interrupted run can reconcile after restart without fabricating applied operations.
- **AC-008 Privacy:** no provider tokens, confidential raw payloads or personal identifiers in default exported evidence; scoped retention/deletion verified.
- **AC-009 UX:** a new user installs the supported edition, verifies one end-to-end outcome and interprets UNKNOWN/REFUTED with documentation alone in a representative environment.
- **AC-010 Operability:** logs/metrics, explicit error messages, cancellation, timeout handling, backup and restore tested where applicable.
- **AC-011 Packaging:** clean-machine install, version check, example, uninstall, entitlement behavior, actual user documentation and archive integrity verified.
- **AC-012 Commerce:** test order, payment state, digital delivery, license entitlement, download, refund/cancellation handling and customer support policy validated in the chosen Shopify setup.
- **AC-013 Competitive claim:** no 'better/faster/safer' assertion without an identical input corpus and reproducible comparison against the named product version.
- **AC-014 Scope completeness:** all mandatory promises of the advertised edition have evidence; mock or simulated results are clearly labeled.
- **AC-015 Review independence:** an independent check must fail at least one intentionally defective candidate (negative control); self-test alone does not qualify for high-risk assurance.

Gate states: PASS / FAIL / BLOCKED, with NOT_APPLICABLE only on documented approved rationale. Overall release: BLOCK whenever a material gate is FAIL/BLOCKED or evidence does not match the exact candidate digest.

## 8. Adversarial/failure corpus

Run all relevant cases, preserving raw results:
1. HTTP 200 and zero external change.
2. External effect applied but HTTP acknowledgment lost.
3. A duplicate webhook or queue delivery.
4. External state changes again between two observations.
5. API eventually consistent; early state stale.
6. A provider account deleted or permissions revoked mid-run.
7. Two concurrent writers targeting the same object.
8. Partial success across multiple target objects.
9. An expired observation or changed verification predicate.
10. Malicious content inside an observed provider payload requesting new tool actions.
11. Compensating action itself fails or is irreversible.
12. Evidence tampered with, wrong tenant or wrong credential.
13. Offline restart / power failure while saving verdict.
14. Timezone/currency/units/decimal precision mismatch.
15. Legitimate user contests verdict with stronger contradictory evidence.
16. Missing truth source (proper verdict UNKNOWN rather than REFUTED).

Every failure case needs expected status, expected *non-action*, environment, deterministic fixture or authorized integration, and evidence location.

## 9. User journey and professional UI

1. Install / connect (local-first or authorized service).
2. Select supported integration and give minimum scopes.
3. Define outcome contract via readable template and preview.
4. Inspect effect boundaries and approve eligible mutating action separately.
5. Follow timeline of dispatched work and read-only observations.
6. See VERIFIED, REFUTED, PARTIAL or UNKNOWN with reason and data-source confidence; never an unexplained green checkmark.
7. Review evidence provenance and exact differences.
8. Decide safe reconcile, approved retry, manual correction or escalation.
9. Export evidence bundle and compare later runs.
10. Revoke integration / delete retained records / get assistance.

Critical states to design: no data, offline, permission denied, timed out, provider rate-limit, in-flight observation, divergence, partial, resolved, archived. Accessibility: keyboard, visual contrast, readable differences and screen-reader labels.

## 10. Benchmark and differentiation

Comparison baselines must include (as applicable) Langfuse evaluation and tracing, Temporal and Inngest durable execution / idempotency, and simple handcrafted postcondition verification. **Our claim is not that these products lack the feature.** Benchmark must specify whether ActionProof improves: false-success detection, evidence auditability, integration effort, unknown handling, retry safety, time to root cause and operating cost.

Use identical incidents and controlled access. Report all failures, false positives, unknowns and exceptions. A product is not commercially differentiated until independent data and buyer adoption support that conclusion.

## 11. Licensing, Shopify and the revenue path

**Route A: Shopify storefront as sales channel.** Sell a downloadable, finished and verified Developer Edition (e.g. USD 99 per-user license), with delivery assets: binaries/packages for supported platforms, checksums, install instructions, docs, integration reference, sample project, license and support/refund policy. Verify current Shopify digital delivery, tax, payment and licensing terms before publication.

**Route B: Shopify App Store (future optional distinct app).** Merchant-facing embedded UI, authenticated GraphQL Admin API, mandatory applicable privacy/compliance events, scopes, billing with Shopify-provided solutions, real development store tests, production-ready app submission. App Store pricing/review requirements are **not** interchangeable with selling a downloadable program on a Shopify storefront.

**Economic measurement:** qualified buyer visits -> interested installs -> activated core job -> paid orders -> net collected revenue after fees, refunds and chargebacks -> support/hosting costs -> contribution margin. A listing, wishlist or simulated checkout is not a sale.

**Commercial assumptions to test:** a one-time USD 99 license is understandable and sustainable; integration cost is low enough; result risk is material to buyer; third-party alternatives insufficient. Reconsider pricing if distribution economics, developer support or market evidence contradict assumptions.

**No release of fake/placeholder product.** Do not publish a pay-to-download listing until the candidate passes requirements and the delivery/rights checks.

## 12. Support, operation and product economics

Own the runtime and product lifecycle: incident severity, update cadence, changelog, compatibility table, release reproducibility, alert/rollback, backup/restore, vuln disclosure, dependency monitoring, license compliance and support in written asynchronous channels.

Track business outcomes separately from product quality: paid customers, activation and verified real outcomes, retention, refund rate, support hours, distribution spend, infrastructure spend and net contribution. Marketing claims may state only measured facts with scope, sample and dates.

Stop/pivot policy: if credible buyer validation does not identify paid willingness or a measurable gap, retain the research and reusable runtime improvements, but do not fund full platform expansion. If harmful false-success cases remain, block sales regardless of demand.

## 13. IECT: scientific relation without commercial overclaiming

ActionProof can operationalize part of IECT as a research instrument: gap between model/claim and observed external reality, auditability of corrective evidence, actual response to contestation, and verified correction latency. It does **not** establish the entire institutional theory or a general theory of political legitimacy.

Research records must separate simulated tests from independent deployments, anecdotal feedback from systematic evidence, and correlation from causal effect. Publish negative findings. Any claim to a novel general framework needs separate literature and peer review.

## 14. Baseline sources (checked 2026-11-03)

- Dogmatique internal: docs/2026_11_02_ANE_QUALITY_CONSTITUTION_V4_WORKING_DRAFT.md (status: working draft; not ISO certification).
- Dogmatique internal: docs/SHOPIFY_PRODUCTION_READY_BY_DESIGN_2026_10_25.md.
- governed-agent-runtime internal: README.md and docs/github-pr-effect.md, docs/ai-native-runtime.md.
- Langfuse evaluation: https://langfuse.com/docs/evaluation/overview
- Temporal durable AI and retry semantics: https://docs.temporal.io/ai and https://docs.temporal.io/encyclopedia/retry-policies
- Shopify Digital Products delivery: https://help.shopify.com/en/manual/products/digital-service-product/digital-downloads
- Shopify app review / public apps: https://shopify.dev/docs/apps/launch/app-store-review/pass-app-review
- Shopify app billing: https://shopify.dev/docs/apps/launch/billing
- Shopify App Store requirements: https://shopify.dev/docs/apps/launch/shopify-app-store/app-store-requirements
- OpenTelemetry semantics: https://opentelemetry.io/docs/specs/semconv/

Source review does not prove market demand or product readiness. Recheck external requirements before implementation and release.

## 15. Decision and open risks log

| ID | Decision / risk | State |
|---|---|---|
| D-001 | Build ActionProof as outcome-verification product layer, reuse owned governed runtime | PROPOSED; pending code-interface review |
| D-002 | Start commercial channel as downloadable developer license via Shopify storefront | PROPOSED; delivery and payment untested |
| D-003 | USD 99 one-time developer license | PRICE HYPOTHESIS |
| D-004 | First supported proofs: GitHub and reference HTTP; Shopify adapter if authorized and tested | PROPOSED |
| R-001 | Some buyer problems solved cheaply by ordinary postcondition queries | UNRESOLVED |
| R-002 | Provider APIs cannot prove physical fulfillment or remote causal attribution | KNOWN LIMITATION |
| R-003 | Multi-writer split-brain / irreversible effects can defeat safe retry | UNRESOLVED for universal promise |
| R-004 | Open-source dependency and commercial licensing of distribution artifacts | MUST AUDIT |
| R-005 | Buyer access and commercial viability not yet evidenced | UNVERIFIED |
| R-006 | Independent product benchmark and external user test absent | UNVERIFIED |
| R-007 | Shopify channel integration and real checkout absent | UNVERIFIED |

**Current handoff status:** This document defines the plan only. No ActionProof product binary, license, production test, Shopify product listing, collected revenue or independent verification is claimed. Next executable work is (a) detailed repository interface audit, (b) buyer/competitor evidence collection, (c) contract and golden-corpus design, then (d) bounded implementation and testing following the gates.
