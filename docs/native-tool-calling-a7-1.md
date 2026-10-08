# A7.1 — Native Function Calls and Bound Argument Validation

## Motivation
The existing OpenAI Responses reasoner asks the model to produce a structured envelope containing a JSON object encoded inside the string `arguments_json`. In no-key local-model tests, Qwen2.5 0.5B, 1.5B and 3B failed for different reasons (invalid envelope, ASK in place of TOOL, malformed nested JSON). Provider protocol success was not model capability success.

## Candidate: provider-native function calls

The A7.1 `NativeReasoner` implements `agentloop.Reasoner` without changing A6.1's durable decision, tool invocation, fencing or Temporal retry contracts.

- Every runtime-admitted read-only tool becomes an inert provider function `cap_0`, `cap_1`, ... with its exact run-bound MCP JSON Schema and human description.
- Provider function names are aliases; model output never determines a new tool binding or executable endpoint. Mutating/unbound tools are not listed.
- `runtime_finish`, `runtime_ask`, `runtime_fail` are reserved terminal-decision functions, not runnable tools.
- Exactly one completed function call is required; empty, mixed text/tool, multi-call, incomplete and unknown function outputs fail closed.
- Bound tool arguments are parsed as a JSON object and validated against the exact run-bound JSON Schema using `github.com/google/jsonschema-go/jsonschema`. Unknown fields, missing required keys and wrong nested types are rejected when the bound schema prohibits them.
- A passing function call only produces a proposed `agentloop.Decision`. Runtime execution remains behind A6.1's durable preparation and read-only tool path.
- OpenAI SDK retries are disabled (`WithMaxRetries(0)`); provider requests set `store=false`. Only one model request is issued per reasoner invocation.

## Provider privacy
Only user input, agent mission, read-only names/descriptions/input schemas, normalized observations and current step may enter the provider prompt. Run ID, conversation ID, MCP endpoint, snapshot digest, invocation ID and transport credentials remain local. Models do not receive authority from their descriptions.

## Explicit boundaries
1. An OpenAI native tool call still carries JSON-formatted arguments on the wire; it removes the **extra string-encoded JSON layer** of the legacy structured-decision envelope. It does not guarantee a small model will select the correct tool or produce valid arguments.
2. The MCP schema itself must be valid and locally resolvable. Unsupported external schema references fail before a provider request.
3. The model can propose the wrong permissible action, such as ASK/FINISH without sufficient evidence; tests must detect premature terminal decisions.
4. Schema validation is an advisory admission boundary for read-only tools, not authorization for mutating actions. Mutating tools remain unsupported in this reasoner.
5. As with A6.1, if a worker crashes after a provider response but before committing the decision, another provider call and charge may occur.
6. Local CPU model performance and success are not assumed from an SDK wire test.

## Evidence gates
- `go test -race ./...`: unit tests for typed arguments, invalid data, unbound tools, mixed/multiple output, credentials/endpoint exclusion and official SDK wire mapping.
- `go test -tags=integration -run TestOpenAIAdapterDrivesDurableDataEngineLoop ./integration/a7`: deterministic Responses wire contract with real PostgreSQL, Temporal and Data Engine MCP.
- `go test -tags=integration -run TestActualLocalNativeModelProposesProfileThenFinishes ./integration/a7_live`: **actual local model**, first TOOL then FINISH from a synthetic observation; intentionally falsifiable.
- `go test -tags=integration -run TestActualLocalNativeModelDrivesDurableDataEngineLoop ./integration/a7`: optional **actual local model** + PostgreSQL + Temporal + real Data Engine MCP. Requires `DATA_ENGINE_MCP_URL`, `DATABASE_URL`, `A7_LIVE_MODEL_BASE_URL`, `A7_LIVE_MODEL`. Database fixture is destructive and must use an isolated test database.

Decision: A7.1 remains a candidate until real-model behavior is measured. No cross-test aggregation is accepted as proof of complete live-model end-to-end operation.

## Actual local-model results — 2026-10-08

The live experiment used Ollama v0.13.3 and the official OpenAI Go SDK against a real local Responses endpoint on GitHub Actions. No paid API or user credential was used.

| Model | Legacy structured envelope | Native function-call candidate |
|---|---|---|
| Qwen2.5 0.5B | FAIL: invalid mixed terminal/tool envelope | Not run |
| Qwen2.5 1.5B | FAIL: chose ASK instead of the bound profile tool | **FAIL:** ordinary message, zero function calls (10.195 s) |
| Qwen2.5 3B | FAIL: malformed nested arguments_json | **FAIL:** ordinary message, zero function calls (34.098 s) |

Both native outputs were **rejected by the adapter before durable commit or tool dispatch**. A real model returning ordinary text is not proof that native function calling works. Unit/wire tests pass, but model decision quality for these CPU candidates is not proven.

**No A7.1 live-model PASS.** Do not broaden privileges, silently parse an ordinary message as executable authority, or bypass schema validation to improve a score. The next experiment should compare a proven tool-capable model and supported provider tool-choice semantics on the exact same corpus with explicit token, time and failure budgets. Do not automatically rerun large CPU inference on every PR update.
