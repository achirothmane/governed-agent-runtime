#!/usr/bin/env python3
"""Compare the *actual* official Go SDK request to our independent HTTP control.

The SDK request is captured by the real-model integration test, not produced by
marshaling SDK Params in Python. Input strings are parsed as JSON for semantic
comparison. This script makes no provider requests and runs without credentials.
"""
import argparse
import json
import sys

from a7_context_ablation import body

def diffs(expected, actual, where="$"):
    if type(expected) != type(actual):
        return [{"path": where, "kind": "type", "expected_type": type(expected).__name__,
                 "actual_type": type(actual).__name__}]
    if isinstance(expected, dict):
        result = []
        for key in sorted(set(expected) | set(actual)):
            at = where + "." + key
            if key not in expected:
                result.append({"path": at, "kind": "extra_in_sdk"})
            elif key not in actual:
                result.append({"path": at, "kind": "missing_from_sdk"})
            else:
                result.extend(diffs(expected[key], actual[key], at))
        return result
    if isinstance(expected, list):
        result = []
        if len(expected) != len(actual):
            result.append({"path": where, "kind": "different_count",
                           "expected": len(expected), "actual": len(actual)})
        for idx, (a, b) in enumerate(zip(expected, actual)):
            result.extend(diffs(a, b, where + f"[{idx}]"))
        return result
    return [] if expected == actual else [{"path": where, "kind": "different_value"}]

def main():
    p = argparse.ArgumentParser()
    p.add_argument("sdk_request", help="synthetic JSON request captured at Go SDK boundary")
    p.add_argument("--model", default="qwen3:1.7b")
    p.add_argument("--output", default="a7-sdk-wire-comparison.json")
    args = p.parse_args()
    with open(args.sdk_request, encoding="utf-8") as f:
        sdk = json.load(f)
    expected = body(args.model, duplicate_schema=True, terminal_functions=True)
    expected.pop("tool_choice")  # production NativeReasoner does not request a mode
    expected["temperature"] = 0.0  # experiment-only SDK client override
    if not isinstance(sdk.get("input"), str):
        raise SystemExit("SDK wire has no string input")
    expected["input"] = json.loads(expected["input"])
    sdk["input"] = json.loads(sdk["input"])

    problems = diffs(expected, sdk)
    # No request values printed: avoid even fixture-specific provider prompt
    # leakage. Only exact difference paths/kinds are kept in CI evidence.
    report = {
        "comparison": "synthetic_official_go_sdk_vs_independent_http_reference",
        "model": args.model, "reference_fields": sorted(expected),
        "sdk_fields": sorted(sdk), "equal": not problems,
        "differences": problems, "difference_count": len(problems),
    }
    with open(args.output, "w", encoding="utf-8") as f:
        json.dump(report, f, indent=2)
    print("SDK_HTTP_WIRE_EQUIVALENT=" + str(not problems).lower(), flush=True)
    print("SDK_HTTP_FIELD_DIFFERENCES=" + json.dumps(problems), flush=True)
    return 0 if not problems else 1

if __name__ == "__main__":
    sys.exit(main())
