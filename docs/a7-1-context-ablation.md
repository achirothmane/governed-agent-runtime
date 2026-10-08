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
