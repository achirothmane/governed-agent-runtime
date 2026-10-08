# A7.2 — Native Tool Calls + Evidence-Bound Typed Terminal Synthesis

## Problem found in real-model execution

Under Ollama 0.13.3 with local Qwen3 1.7B, the original native `NativeReasoner` can propose a valid `data.profile` function call with correct arguments. When shown the normalized observation that the same identity has two conflicting values, the model has sometimes emitted an ordinary assistant message instead of a `runtime_finish` function call. The runtime correctly rejected that output; no free text was elevated to an executable or terminal decision.

Actual evidence:
- Official Go SDK/independent HTTP wire are semantically identical for the synthetic input: [37749643693](https://github.com/achirothmane/data-engine/actions/runs/37749643693), zero differences, native first-step TOOL passed with test-only `temperature=0`.
- First-then-second live native smoke: [37749643525](https://github.com/achirothmane/data-engine/actions/runs/37749643525), first TOOL passed in 67.24s; second ordinary message rejected after 30.7s.
- Test-only structured terminal synthesis: [37750706805](https://github.com/achirothmane/data-engine/actions/runs/37750706805), **2/2** Qwen3 FINISH outputs correctly described conflicting evidence using a JSON Schema output. Responses returned `reasoning` plus `message`; the harness disregarded the separate reasoning item and rejected all unstructured terminal text. An earlier 0/2 result was a **harness shape false negative**, not evidence that the typed output failed.

## Explicit experimental architecture

`ObservedTerminalReasoner` is an **opt-in policy for a single read-only observation**, not a silent replacement for `NativeReasoner` or a claim about general multi-tool agents.

- **Before any observation:** only the existing provider-native function-call reasoner may propose one exact run-bound read-only tool. It does not execute the tool.
- **After exactly one bound, normalized observation:** the provider is given **no tool functions**, and a strict `FINISH | ASK | FAIL` JSON Schema. A local parser rejects unsupported decision types, unknown fields, trailing JSON, empty messages and plain text.
- **FINISH additionally requires** a normalized, non-error, no-input-needed observation. The observed step must be immediately prior, tool identity must match a run-bound read-only tool and any supplied snapshot digest must match the bound descriptor.
- **Multi-observation workflows are refused** in this experimental policy. They remain on the general `NativeReasoner` until a separate multistep design is proven.
- Provider text never becomes execution authority; no tool endpoint, invocation ID, run ID, snapshot digest or credential is included in model-visible context.
- Provider API retries remain disabled, `store=false`; inference costs and probabilistic correctness are not hidden behind automatic retries.

## Acceptance matrix

| Gate | Requirement |
|---|---|
| Local | Go unit tests reject forged/unbound/error observations, invalid terminal responses and free text |
| Native tool | Real model proposes valid bound TOOL with typed input |
| Terminal | Model returns strict JSON terminal decision supported by normalized evidence |
| Durable real end-to-end | PostgreSQL and real Temporal run persist one bound TOOL, real Data Engine MCP profile observation, and one typed FINISH; no retry or duplicate external effect |
| Publication | Do not merge or claim production until these gates pass and failures have been falsified |

The first three gates are separate from the actual durable E2E proof. Two successful model responses do not establish broad reliability, determinism, or marketplace readiness. All expensive Qwen3 workflows are manual-only after initial validation.
