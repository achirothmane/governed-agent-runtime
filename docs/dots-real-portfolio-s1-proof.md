# Dots S1 — actual public Portfolio through D4 (local, non-AI)

**Date:** 2026-10-09  
**Scope:** actual human-authorized OBSERVE task `dots-runtime-evidence-observe-006`, not a synthetic READY task.  
**Portfolio source commit:** [604ce014077c9cf93bd8faa184b7a757cd454d5b](https://github.com/achirothmane/achirothmane/commit/604ce014077c9cf93bd8faa184b7a757cd454d5b).  
**Observed proof:** [GitHub Actions run 37907122454 — PASS](https://github.com/achirothmane/governed-agent-runtime/actions/runs/37907122454) with sanitized `dots-real-portfolio-d4-proof` artifact, artifact ID `11604179459` (short-term retention).

## What was actually verified

- Checked out the exact public Portfolio commit, not a fabricated context fixture or an unpinned moving branch.
- The Portfolio-owned exporter rechecked freshness, queue/handoff alignment, human priority and the S0 source digest. The Go runtime independently verified every source binding and its canonical D2 snapshot digest.
- D3 refused a local authority-escalating proposal before persistence.
- An **explicitly deterministic LOCAL reasoner**, with no API/model provider and no tools, proposed one read-only next-evidence gate from the actual restricted Portfolio `ReasoningView`.
- D4 created a Temporal workflow and stored the validated inert decision in an **isolated PostgreSQL 16 CI service**.
- Same-ID replay returned an identical decision with **reasoner call count=1**.
- The test queried the durable tool-invocation ledger and observed **0 tool invocations**.
- A separate Python check mapped the recorded OBSERVE decision to the independent, source-bound S0 policy oracle and received `POLICY_ADMISSIBLE_ONLY`, **not** a claim of sound business judgment.

## Proven boundaries

| Statement | Current state |
| --- | --- |
| Real Portfolio source -> sealed Go D2 context | PASS in pinned CI |
| D2 -> D3 -> D4 Temporal and PostgreSQL commit | PASS with local deterministic reasoner |
| Exact replay without additional reasoner calls | PASS |
| Unauthorized authority escalation | REFUSED |
| Tool/effect invocation count | 0 |
| Provider inference and external API cost | 0 |
| Autonomous AI judgment quality | NOT_MEASURED |
| Human-labeled operational usefulness | NOT_MEASURED |
| D5 paid/OpenAI live test | NOT_RUN |
| Ongoing autonomous portfolio management | NOT_AUTHORIZED |

**This is an authentic integration with actual source data and a real durable decision, but it is not an autonomous AI manager demonstration.** A model-produced candidate and independent outcome labels are different, later gates. Production deployment, payment evidence, and commercial adoption remain UNKNOWN.

## Reproduction

The dedicated `.github/workflows/dots-real-s1-integration.yml` CI workflow pins the exact Portfolio source commit and runs:

```sh
python _portfolio/portfolio/scripts/export_dots_context.py \
  --root _portfolio --as-of 2026-10-09 \
  --output /tmp/dots-real-context.json
go test -tags=integration \
  -run '^TestRealPublicPortfolioD4DecisionPersistsWithoutTools$' \
  -count=1 -timeout=8m -v ./integration/dotsrealcontext
```

After the source freshness deadline, this pinned historical test is expected to **fail closed**. Refreshing to a newer Portfolio commit must be based on an actual revalidation, not by extending dates or bypassing source checks. No `OPENAI_API_KEY`, `ALLOW_PAID_MODEL_TEST`, private repo connection or outbound communication is needed.
