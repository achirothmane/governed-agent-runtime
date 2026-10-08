#!/usr/bin/env python3
"""A7.1 counterfactual tool_choice tests.

Positive examples alone cannot distinguish model compliance from tool_choice
enforcement. These conflict prompts make that distinction observable.

Never execute model-proposed tool calls. This is a wire/protocol experiment.
"""
import argparse
import json
import sys

from a7_tool_choice_probe import SCHEMA, PROMPT, request, extract

NO_TOOL_PROMPT = "Reply with only the word hello. No source data is given and you must not call any tool."

def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--base-url", default="http://127.0.0.1:11434/v1")
    parser.add_argument("--model", default="qwen3:1.7b")
    parser.add_argument("--output", default="a7-tool-choice-counterfactual.json")
    args = parser.parse_args()
    base = args.base_url.rstrip("/")
    if not (base.startswith("http://127.0.0.1:") or base.startswith("http://localhost:")):
        parser.error("local loopback provider required")
    records = []
    for protocol, choice in [
        ("responses", "required"), ("responses", "forced"), ("responses", "none"),
        ("chat", "required"), ("chat", "forced"), ("chat", "none"),
    ]:
        prompt = PROMPT if choice == "none" else NO_TOOL_PROMPT
        if protocol == "responses":
            tool = {"type": "function", "name": "cap_0",
                    "description": "Read-only profile of source rows",
                    "parameters": SCHEMA, "strict": False}
            selection = {"type": "function", "name": "cap_0"} if choice == "forced" else choice
            body = {
                "model": args.model, "input": prompt,
                "tools": [tool], "tool_choice": selection,
                "store": False, "max_output_tokens": 360,
            }
            endpoint = "/responses"
        else:
            tool = {"type": "function", "function": {
                "name": "cap_0", "description": "Read-only profile of source rows",
                "parameters": SCHEMA,
            }}
            selection = {"type": "function", "function": {"name": "cap_0"}} if choice == "forced" else choice
            body = {
                "model": args.model, "stream": False,
                "messages": [
                    {"role": "system", "content": "Obey the user's stated request and the provider's tool-choice constraint."},
                    {"role": "user", "content": prompt},
                ],
                "tools": [tool], "tool_choice": selection,
                "max_tokens": 360,
            }
            endpoint = "/chat/completions"
        data, error, elapsed = request(base + endpoint, body, timeout=100)
        calls, others = extract(protocol, data) if data is not None else ([], [])
        correct = (error is None and (
            len(calls) == 0 if choice == "none"
            else len(calls) == 1 and calls[0].get("name") == "cap_0"
        ))
        record = {
            "model": args.model, "endpoint": protocol, "choice": choice,
            "opposing_prompt": True, "tool_calls": len(calls),
            "other_output_types": others, "http_error": error,
            "seconds": elapsed, "constraint_observed": correct,
        }
        records.append(record)
        print(f"{protocol:9} {choice:8} ENFORCED={correct} "
              f"calls={len(calls)} HTTP={error} latency={elapsed}s", flush=True)
    with open(args.output, "w", encoding="utf-8") as handle:
        json.dump({"model": args.model, "controls": records}, handle, indent=2)
    # Negative results are evidence, not a harness crash; do not equate
    # process exit zero with protocol enforcement.
    print("OBSERVED_ENFORCEMENT=" + str(sum(r["constraint_observed"] for r in records))
          + "/6", flush=True)
    return 0

if __name__ == "__main__":
    sys.exit(main())
