# A7.1 — Context and tool-choice ablation (2026-10-08)

## Controlled question
Why did the simple Qwen3 1.7B OpenAI-compatible function-call probe succeed, while the actual `NativeReasoner` with the complete bound-run input sometimes produced an ordinary message instead of a native tool call?

For this first ablation the model and provider were pinned: `qwen3:1.7b` on CPU GitHub Actions with loopback `ollama/ollama:0.13.3`, `/v1/responses`. The exact user input, agent mission, `cap_0` schema, native instructions, `max_output_tokens=768`, `store=false` and explicit `tool_choice=auto` were held constant. There was **no tool execution**, only extraction of proposed calls.

The 2×2 factorial changed only:
- Whether `available_tools` in the text input repeated the exact JSON Schema already present in the native `tools` field.
- Whether terminal `runtime_finish`, `runtime_ask`, and `runtime_fail` function definitions were offered beside `cap_0`.

We ran each condition twice, reversing the order for the second round. Conditions with no terminal definitions retained the identical native instructions; the experiment measures *presence of those definitions* rather than a complete production-ready alternate prompt.

## Evidence

| Duplicate schema in input | Terminal definitions offered | Correct `cap_0` proposals / trials |
|---|---|---|
| Yes | Yes — current full design | **2/2** |
| No | Yes | **1/2** |
| Yes | No | **0/2** |
| No | No | **1/2** |

GitHub Actions [run 37746705939](https://github.com/achirothmane/data-engine/actions/runs/37746705939), artifact `a7-context-ablation-evidence`; source `scripts/a7_context_ablation.py`.

Every accepted call in this tiny corpus had exactly one `cap_0` proposal with the two correct rows, identity field and numeric values. Rejected calls were ordinary messages without native function calls, not provider HTTP failures. Some responses took over 50 seconds on CPU.

## Interpretation, not causal overclaim

- `KNOWN`: Qwen3 can choose the correct native function under the full runtime-shaped input at least twice; thus a universal incompatibility with the full input is falsified.
- `NOT SUPPORTED`: Removing duplicated schemas or terminal choices consistently improves selection. This two-run-per-condition sample is much too small to establish a reliable effect.
- `CONFLICTING OBSERVATION`: the previous full SDK-connected `NativeReasoner` test returned zero calls and one ordinary message, while the direct HTTP ablation with all fields present but explicit `tool_choice=auto` returned valid calls 2/2. It is not yet known whether explicit-vs-omitted `tool_choice`, SDK wire details, nondeterministic sampling, or runner conditions explain the discrepancy.
- `UNKNOWN`: Actual Qwen3 TOOL → **real Temporal/PostgreSQL/Data Engine MCP** → FINISH acceptance. This ablation does not execute MCP or Temporal.
- `NOT AN AUTHORITY BOUNDARY`: Neither provider-side `required` nor `none` demonstrated reliable enforcement in earlier counterfactual tests. The runtime must still validate tool binding and JSON Schema locally and reject unexpected outputs.

Next finite experiment: keep the **full baseline condition** constant and compare `tool_choice` omitted vs explicit `auto` three times each, with reversed trial order. Do not change `NativeReasoner` behavior until the outcome is measured.


## Follow-up single-field test: explicit auto vs omission

The first baseline included explicit `tool_choice=auto`, whereas the production-shaped Go `NativeReasoner` omits `tool_choice`. A second, separate finite six-call test kept the **entire full-context baseline request identical** and changed only the presence of that field.

| Tool choice sent | Correct bound `cap_0` proposals / trials |
|---|---|
| Omitted (matching SDK default) | **3/3** |
| Explicit `"auto"` | **1/3** |

GitHub Actions [run 37747438210](https://github.com/achirothmane/data-engine/actions/runs/37747438210), machine-readable artifact `a7-tool-choice-auto-presence-evidence`; source `scripts/a7_auto_presence_probe.py`.

Both conditions returned HTTP success every time. The rejected calls were ordinary model messages, not transport failures. This small sample is **not proof** that explicit `auto` causes harm: the earlier 2×2 ablation returned **2/2** accepted with explicit `auto` and full context, while the earlier direct `NativeReasoner` live test had returned zero calls with omission. The discrepancies establish significant model/provider sampling instability or uncontrolled differences across executions; the causal role of `tool_choice` remains **UNKNOWN**.

**Decision:** no production change to tool-choice semantics, schema duplication, or terminal-function set. Keep the original read-only local allowlist, JSON Schema validation, and fail-closed rejection. A next test must make generation settings (temperature and provider-supported seed) explicit and measure repeated request reliability, not infer readiness from individual lucky calls. All CPU-heavy experiments are disabled from automatic PR triggers after the finite runs.


## Replication — an independent second 2×2 run

An automatic PR trigger had already started a second identical ablation before CPU-heavy workflows were switched to manual-only. The additional run [37747438124](https://github.com/achirothmane/data-engine/actions/runs/37747438124) completed with no transport errors. It returned, in the same condition order reversed in its own second round:
- Duplicate schema + terminal definitions: **2/2**
- No duplicated schema + terminal definitions: **1/2**
- Duplicated schema + no terminal definitions: **0/2**
- Neither: **2/2**

Combining the **two completed, independent runs** (four observations per condition) yields:

| Duplicate schema | Terminal definitions | Accepted `cap_0` first-step proposals |
|---|---|---|
| Yes | Yes (current full design) | **4/4** |
| No | Yes | **2/4** |
| Yes | No | **0/4** |
| No | No | **3/4** |

Interpretation: in this fixed Qwen3/Ollama corpus, the full current choice set performed best, and removing terminal functions **while leaving duplicate schema** consistently failed. The two factors appear to interact; this is **not** evidence that either factor should be removed globally. Four observations per cell are insufficient for production reliability claims, even when an observed rate is 4/4. This direct HTTP experiment included explicit `tool_choice=auto`; a separate 3+3 trial showed unexplained reversals across runs and cannot establish deterministic model behavior. None of these calls invoked the real Data Engine MCP or Temporal workflows.

Decision remains: preserve the current fail-closed runtime contracts, do not change prompt/schema/terminal policy yet, and only attempt further live-model E2E after controlling SDK-wire framing and sampling reproducibility.


## Exact official Go SDK wire: independently verified

The original SDK-vs-HTTP ambiguity has been isolated with an actual **same-request loopback capture proxy** and Qwen3 inference through the official OpenAI Go SDK v3.73.0. This does not marshal a Python approximation of the Go request; the Go client actually POSTs to a local proxy, which captures only the synthetic request JSON, then forwards the same bytes to Ollama 0.13.3.

The comparison parses the nested JSON `input` and recursively compares all request fields, tool schemas and function descriptions to an independent direct-HTTP reference, with the **same full agent mission and synthetic rows** and `tool_choice` omitted. The only test-only additional request field was `temperature=0`.

- [Run 37749073091](https://github.com/achirothmane/data-engine/actions/runs/37749073091): actual SDK model first-step **TOOL PASS** in 64.624 seconds. Original comparator flagged `0.0` (Python float) versus `0` (JSON numeric integer), a **false-positive type difference**, not a semantic difference.
- Numeric comparison corrected so `0 == 0.0` while bool remains distinct.
- [Run 37749643693](https://github.com/achirothmane/data-engine/actions/runs/37749643693): actual SDK model first-step **TOOL PASS** in 29.425 seconds, independent **SDK wire vs reference = PASS**, `SDK_HTTP_FIELD_DIFFERENCES=[]`.

Code: `integration/a7_live/sdk_wire_test.go`, `scripts/a7_sdk_wire_compare.py`. Synthetic request evidence is stored only as a workflow artifact; the test never persists model text, secrets, auth headers, runtime invocation IDs or MCP responses. These tests do **not** invoke Data Engine MCP, so the separate live durable contract remains a different acceptance gate.

**Revised diagnosis:** there is no demonstrated SDK request-serialization incompatibility on the tested synthetic fixture. Temperature-zero may have helped, but two successful independent invocations are insufficient to prove a reliable general causal improvement over default sampling, and temperature zero is **not a provider-reproducibility guarantee**. The Ollama 0.13.3 Responses compatibility documentation does not list `seed` as supported for this endpoint. No production NativeReasoner default or authorization logic was changed.
