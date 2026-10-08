#!/usr/bin/env python3
"""A7.2 feasibility test: structured terminal decision AFTER a proven observation.

Never treats free-form assistant text as execution authority. A synthetic,
normalized read-only MCP result is provided for this isolated model-only gate.
This is NOT the real Temporal/Data Engine end-to-end proof.
"""
import argparse
import json
import sys

from a7_context_ablation import DESCRIPTION, MISSION, SCHEMA, USER_INPUT
from a7_tool_choice_probe import request

TERMINAL_SCHEMA = {
    "type": "object",
    "additionalProperties": False,
    "required": ["kind", "message"],
    "properties": {
        "kind": {"type": "string", "enum": ["FINISH", "ASK", "FAIL"]},
        "message": {"type": "string"},
    },
}
OBSERVATION = {
    "step": 1,
    "tool": "data.profile",
    "is_error": False,
    "needs_input": False,
    "structured_content": {
        "stage": "PRE_SEMANTIC_PROFILE",
        "decision": "CONFLICTING",
        "reason": "same identity a has price 20 and price 21",
    },
}
INSTRUCTIONS = (
    "You have already received a read-only tool observation. "
    "Do not propose another tool or claim independent tool execution. "
    "Return exactly one structured terminal decision. "
    "Use FINISH only if the normalized observation supports the answer, "
    "ASK only if essential user input is still missing, or FAIL if the "
    "evidence is not sufficient. Do not invent evidence or external effects. "
    "Do not expose hidden reasoning, endpoints or internal identifiers."
)

def make_request(model):
    return {
        "model": model,
        "input": json.dumps({
            "mission": MISSION, "user_input": USER_INPUT, "step": 2,
            "available_tools": [{
                "name": "data.profile", "title": "Profile raw data",
                "description": DESCRIPTION, "input_schema": SCHEMA,
            }],
            "observations": [OBSERVATION],
        }, separators=(",", ":")),
        "instructions": INSTRUCTIONS,
        "text": {"format": {
            "type": "json_schema", "name": "a7_terminal_after_observation",
            "strict": True, "schema": TERMINAL_SCHEMA,
        }},
        "max_output_tokens": 512, "temperature": 0,
        "store": False,
    }

def check(response):
    if not isinstance(response, dict):
        return {"accepted": False, "reason": "invalid_provider_response"}
    outputs = response.get("output", [])
    output_types = [x.get("type") for x in outputs]
    # The Responses API may include a separate reasoning item. Like the
    # production native adapter, disregard it without persisting its content.
    visible = [x for x in outputs if x.get("type") != "reasoning"]
    if len(visible) != 1 or visible[0].get("type") != "message":
        return {"accepted": False, "reason": "not_exactly_one_terminal_message",
                "output_types": output_types}
    content = visible[0].get("content", [])
    if len(content) != 1 or content[0].get("type") != "output_text":
        return {"accepted": False, "reason": "not_exactly_one_output_text",
                "output_types": output_types}
    try:
        d = json.loads(content[0].get("text", ""))
    except (TypeError, ValueError):
        return {"accepted": False, "reason": "invalid_terminal_json", "output_types": output_types}
    if not isinstance(d, dict) or set(d) != {"kind", "message"}:
        return {"accepted": False, "reason": "invalid_terminal_shape", "output_types": output_types}
    if d.get("kind") not in ("FINISH", "ASK", "FAIL") or not isinstance(d.get("message"), str):
        return {"accepted": False, "reason": "invalid_terminal_fields", "output_types": output_types}
    semantic_correct = d["kind"] == "FINISH" and "conflict" in d["message"].lower()
    return {
        "accepted": semantic_correct,
        "terminal_kind": d["kind"],
        "semantic_correct": semantic_correct,
        "reason": "ok" if semantic_correct else "wrong_terminal_decision",
        "output_types": output_types,
    }

def main():
    p = argparse.ArgumentParser()
    p.add_argument("--base-url", default="http://127.0.0.1:11434/v1")
    p.add_argument("--model", default="qwen3:1.7b")
    p.add_argument("--output", default="a7-terminal-synthesis-results.json")
    args = p.parse_args()
    base = args.base_url.rstrip("/")
    if not (base.startswith("http://127.0.0.1:") or base.startswith("http://localhost:")):
        p.error("loopback local inference only")
    results = []
    for iteration in range(1, 3):
        res, error, seconds = request(base+"/responses", make_request(args.model), timeout=120)
        outcome = check(res)
        outcome.update({"attempt": iteration, "transport_error": error, "seconds": seconds})
        results.append(outcome)
        print(f"attempt={iteration} terminal_accepted={outcome['accepted']} "
              f"kind={outcome.get('terminal_kind')} output_types={outcome.get('output_types')} "
              f"reason={outcome.get('reason')} error={error} latency={seconds}s", flush=True)
    with open(args.output, "w", encoding="utf-8") as f:
        json.dump({"model": args.model, "results": results}, f, indent=2)
    print("A7_TERMINAL_SYNTHESIS_ACCEPTED=" + str(sum(x["accepted"] for x in results)) + "/2")
    return 0 if all(x["accepted"] for x in results) else 1

if __name__ == "__main__":
    sys.exit(main())
