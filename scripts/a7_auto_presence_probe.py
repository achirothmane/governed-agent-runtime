#!/usr/bin/env python3
"""A7.1 controlled test of explicit tool_choice=auto vs omission.

Same Qwen3, runtime-shaped model input, bound tool schema, terminal functions,
instructions and local Responses endpoint for both conditions.
The prior full native SDK omitted tool_choice, while the context-ablation harness
explicitly set auto. Only this one request field changes.
"""
import argparse
import json
import sys
from a7_context_ablation import body, assess
from a7_tool_choice_probe import request

def main():
    p = argparse.ArgumentParser()
    p.add_argument("--base-url", default="http://127.0.0.1:11434/v1")
    p.add_argument("--model", default="qwen3:1.7b")
    p.add_argument("--output", default="a7-tool-choice-auto-presence.json")
    args = p.parse_args()
    base = args.base_url.rstrip("/")
    if not (base.startswith("http://127.0.0.1:") or base.startswith("http://localhost:")):
        p.error("loopback only")
    # 3 rounds, reverse ordering in middle to reduce systematic order bias.
    schedule = [(1, False), (1, True), (2, True), (2, False), (3, False), (3, True)]
    records = []
    for round_num, explicit in schedule:
        payload = body(args.model, duplicate_schema=True, terminal_functions=True)
        if not explicit:
            payload.pop("tool_choice")
        response, error, seconds = request(base + "/responses", payload, timeout=100)
        result = assess(response)
        result.update({
            "round": round_num,
            "tool_choice_auto_explicit": explicit,
            "transport_error": error, "seconds": seconds,
        })
        records.append(result)
        print(f"round={round_num} tool_choice_auto_explicit={explicit} "
              f"accepted={result['accepted']} calls={result['calls']} "
              f"selection={result['selected']} other={result['nonfunction']} "
              f"error={error} latency={seconds}s", flush=True)
    summary = {
        str(ex): {
            "accepted": sum(x["accepted"] for x in records if x["tool_choice_auto_explicit"] == ex),
            "attempts": sum(x["tool_choice_auto_explicit"] == ex for x in records),
            "transport_errors": sum(x["transport_error"] is not None for x in records
                                    if x["tool_choice_auto_explicit"] == ex),
        }
        for ex in (False, True)
    }
    with open(args.output, "w", encoding="utf-8") as f:
        json.dump({"model": args.model, "results": records, "summary": summary}, f, indent=2)
    print("AUTO_PRESENCE_SUMMARY=" + json.dumps(summary, sort_keys=True), flush=True)
    # Model selection FAIL is evidence, not a CI transport/crash error.
    return 0

if __name__ == "__main__":
    sys.exit(main())
