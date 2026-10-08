#!/usr/bin/env python3
"""A7.1 four-cell, same-prompt native-function ablation.

Controls: pinned Ollama and model; OpenAI-compatible Responses endpoint;
same mission, data, native decision instructions, actual runtime tool schema,
max output tokens, and tool_choice=auto. Exactly one factor changes per cell:
(a) duplicate JSON Schema in the input; (b) three terminal-function options.
No runtime effect, no MCP invocation, no credentials, no model text persisted.
"""
import argparse
import json
import sys

from a7_tool_choice_probe import request

# Match integration/a7_live/live_model_test.go, not the simpler prior probe.
SCHEMA = {
    "type": "object",
    "additionalProperties": False,
    "properties": {
        "rows": {"type": "array", "items": {"type": "object"}},
        "identity_field": {"type": "string"},
    },
    "required": ["rows", "identity_field"],
}
TERMINAL_SCHEMA = {
    "type": "object",
    "additionalProperties": False,
    "properties": {"message": {"type": "string"}},
    "required": ["message"],
}
DESCRIPTION = "Analyze raw rows and detect conflicting values for identical identities; read-only."
USER_INPUT = (
    'Call data.profile with identity_field="id" and '
    'rows=[{"id":"a","price":20},{"id":"a","price":21}]. '
    'After the profile returns, tell me whether the raw values conflict. '
    'Do not FINISH before calling the tool.'
)
MISSION = "Use data.profile to inspect raw evidence before answering; never guess results."
INSTRUCTIONS = (
    "You are a decision component, not a tool executor. Choose exactly one available function. "
    "To inspect data, call the matching cap_N tool using JSON arguments. "
    "Only after receiving evidence, call runtime_finish with a concise message. "
    "Use runtime_ask only when essential user input is missing, and runtime_fail if work cannot proceed. "
    "Never claim a function ran yourself. Never invent an endpoint or authority. "
    "Do not put JSON inside a string. Do not reveal credentials, internal identifiers or hidden reasoning."
)

def body(model, duplicate_schema, terminal_functions):
    visible_tool = {
        "name": "data.profile",
        "title": "Profile raw data",
        "description": DESCRIPTION,
    }
    if duplicate_schema:
        visible_tool["input_schema"] = SCHEMA
    payload = {
        "mission": MISSION,
        "user_input": USER_INPUT,
        "step": 1,
        "available_tools": [visible_tool],
    }
    tools = [{
        "type": "function",
        "name": "cap_0",
        "description": "Read-only data.profile: " + DESCRIPTION,
        "parameters": SCHEMA,
        "strict": False,
    }]
    if terminal_functions:
        tools += [
            {"type": "function", "name": "runtime_finish",
             "description": "Finish after adequate evidence. Provide final answer in message.",
             "parameters": TERMINAL_SCHEMA, "strict": True},
            {"type": "function", "name": "runtime_ask",
             "description": "Ask for essential missing information only.",
             "parameters": TERMINAL_SCHEMA, "strict": True},
            {"type": "function", "name": "runtime_fail",
             "description": "Refuse or report inability to proceed correctly.",
             "parameters": TERMINAL_SCHEMA, "strict": True},
        ]
    return {
        "model": model, "input": json.dumps(payload, separators=(",", ":")),
        "instructions": INSTRUCTIONS, "store": False,
        "max_output_tokens": 768, "tools": tools, "tool_choice": "auto",
    }

def assess(response):
    if not isinstance(response, dict):
        return {"calls": 0, "nonfunction": [], "selected": [], "valid_args": False,
                "match_intent": False, "accepted": False}
    outputs = response.get("output", [])
    calls = [x for x in outputs if x.get("type") == "function_call"]
    other = [x.get("type") for x in outputs if x.get("type") not in ("function_call", "reasoning")]
    names = [x.get("name") for x in calls]
    valid_args = False
    match_intent = False
    if len(calls) == 1 and names == ["cap_0"]:
        try:
            args = json.loads(calls[0].get("arguments", ""))
            valid_args = (
                isinstance(args, dict)
                and set(args) == {"identity_field", "rows"}
                and isinstance(args.get("identity_field"), str)
                and isinstance(args.get("rows"), list)
                and all(isinstance(row, dict) for row in args.get("rows", []))
            )
            match_intent = (
                valid_args and args["identity_field"] == "id"
                and len(args["rows"]) == 2
                and args["rows"] == [
                    {"id": "a", "price": 20},
                    {"id": "a", "price": 21},
                ]
            )
        except (ValueError, TypeError):
            pass
    return {
        "calls": len(calls), "nonfunction": other, "selected": names,
        "valid_args": valid_args, "match_intent": match_intent,
        "accepted": len(calls) == 1 and not other and names == ["cap_0"] and match_intent,
    }

def main():
    p = argparse.ArgumentParser()
    p.add_argument("--base-url", default="http://127.0.0.1:11434/v1")
    p.add_argument("--model", default="qwen3:1.7b")
    p.add_argument("--output", default="a7-context-ablation.json")
    args = p.parse_args()
    base = args.base_url.rstrip("/")
    if not (base.startswith("http://127.0.0.1:") or base.startswith("http://localhost:")):
        p.error("local loopback only; no outbound model provider in this probe")
    # First sequence and reversed second sequence mitigate order/cold-start effects.
    conditions = [(True, True), (False, True), (True, False), (False, False)]
    runs = [(round_num, dup, terminal)
            for round_num, series in ((1, conditions), (2, list(reversed(conditions))))
            for dup, terminal in series]
    results = []
    for round_num, duplicate, terminal in runs:
        req = body(args.model, duplicate, terminal)
        response, error, elapsed = request(base + "/responses", req, timeout=100)
        result = assess(response)
        result.update({
            "round": round_num, "duplicate_input_schema": duplicate,
            "terminal_functions": terminal, "model": args.model,
            "seconds": elapsed, "transport_error": error,
            "input_bytes": len(req["input"].encode("utf-8")),
            "offered_functions": len(req["tools"]),
        })
        results.append(result)
        print(
            f"round={round_num} duplicated={duplicate} terminals={terminal} "
            f"accepted={result['accepted']} calls={result['calls']} "
            f"selection={result['selected']} other={result['nonfunction']} "
            f"args_valid={result['valid_args']} intent={result['match_intent']} "
            f"error={error} latency={elapsed}s", flush=True
        )
    summary = {}
    for duplicate, terminal in conditions:
        group = [r for r in results
                 if r["duplicate_input_schema"] == duplicate
                 and r["terminal_functions"] == terminal]
        summary[f"dup_{int(duplicate)}_terminal_{int(terminal)}"] = {
            "accepted": sum(r["accepted"] for r in group),
            "attempts": len(group),
            "errors": sum(r["transport_error"] is not None for r in group),
        }
    with open(args.output, "w", encoding="utf-8") as f:
        json.dump({"model": args.model, "rounds": 2, "results": results,
                   "summary": summary}, f, indent=2)
    print("ABLATION_SUMMARY=" + json.dumps(summary, sort_keys=True), flush=True)
    # Intentional always-0: failures of capability are scientific findings.
    # CI must separately distinguish harness failure from model refusal.
    return 0

if __name__ == "__main__":
    sys.exit(main())
