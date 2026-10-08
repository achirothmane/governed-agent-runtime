# A7.1 — Tool-choice capability and enforcement are different claims

**Environment:** 2026-10-08, GitHub Actions Ubuntu CPU, pinned `ollama/ollama:0.13.3`, published `qwen3:1.7b`, local unauthenticated loopback OpenAI-compatible endpoints. No paid API, remote inference or runtime tool effect in these protocol probes.

## Positive capability probe

A single read-only function `cap_0` was provided with an explicit user request to profile two rows whose same identity has differing numeric values. Six trials were executed and independently checked for exactly one function-call output and a correctly structured argument object.

| Endpoint | Auto | Required | Forced `cap_0` |
|---|---|---|---|
| `/v1/responses` | 1 correct call | 1 correct call | 1 correct call |
| `/v1/chat/completions` | 1 correct call | 1 correct call | 1 correct call |

Evidence: workflow run [37739343048](https://github.com/achirothmane/governed-agent-runtime/actions/runs/37739343048); machine-readable artifact `a7-tool-choice-evidence`. Code: `scripts/a7_tool_choice_probe.py`.

**What it proves:** this specific model/runtime combination can emit a valid function call for the explicit example through either endpoint.

**What it does not prove:** that `required`, `forced`, or `none` constrains model output. The task instructions themselves favored the tool.

## Counterfactual enforcement probe

Two opposing user instructions were used:
- `required` / forced `cap_0`: user explicitly asks for a plain hello and says not to call a tool.
- `none`: user explicitly asks to invoke `cap_0` with the two rows.

| Endpoint | Required under conflicting prompt | Forced under conflicting prompt | None under conflicting prompt |
|---|---|---|---|
| Responses | 0 calls, **not enforced** | 0 calls, **not enforced** | 0 calls; observed match (one trial) |
| Chat Completions | 0 calls, **not enforced** | 0 calls, **not enforced** | 1 call, **not enforced** |

Evidence: run [37739739150](https://github.com/achirothmane/governed-agent-runtime/actions/runs/37739739150); artifact `a7-choice-negative-controls`. Code: `scripts/a7_choice_negative_controls.py`.

Only **1 of 6** outcomes matched the requested provider-side constraint, and a single match is not proof of enforcement. Both endpoints returned HTTP 200 even when violating required/forced/none behavior.

The published Ollama Responses compatibility implementation explicitly describes `tool_choice` as not fully supported. This test **does not** generalize to the official OpenAI API or other Ollama releases/models.

## Binding decision and boundaries

1. **KNOWN**: Qwen3 1.7B on pinned Ollama 0.13.3 can produce one schema-shaped function call when clearly requested.
2. **KNOWN**: the tested provider's `tool_choice` is not reliable under conflicting instructions, including `none`. An accepted HTTP response is not enforcement evidence.
3. **UNKNOWN**: broad reliability across prompts, models, versions, latency distributions and all failure modes. Six positive and six adversarial trials are *not* statistical validation.
4. **REFUSED**: giving provider-side `required` or `none` runtime authorization significance. Model tools must remain inert proposals with local read-only allowlisting, local exact run-bound JSON Schema validation, and durable commit before invocation.
5. **STOP RULE**: Do not add hidden retry loops or parse plain assistant text as executable authority to inflate tool-use success.
6. **E2E FIRST ATTEMPT — FAIL (2026-10-08):** The opt-in private Data Engine run [37739678215](https://github.com/achirothmane/data-engine/actions/runs/37739678215) started PostgreSQL, actual Data Engine MCP and Ollama Qwen3 1.7B. Its first test, `TestActualLocalNativeModelProposesProfileThenFinishes`, returned **zero function calls and one ordinary message** at reasoning step 1 (40.019 seconds). The adapter correctly raised `ErrInvalidDecision` and **stopped before tool dispatch**. Therefore the real-model Temporal/Data Engine step was **not run**; this is *not* a real-model E2E PASS. The difference versus the positive protocol probe is material: this test includes the full bound-run context and all three terminal functions, rather than a single offered function. The root cause is not yet isolated.
7. **NEXT GATE:** Falsify whether repeated tool schemas, terminal-function alternatives, and verbose model context explain the difference; then require actual Qwen3 TOOL→PostgreSQL/Temporal→real Data Engine MCP observation→Qwen3 FINISH without widening model authority.

No commercial-readiness or exactly-once billing claim follows from these tests.
