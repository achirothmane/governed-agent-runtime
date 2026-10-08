#!/usr/bin/env python3
"""Finite, no-secret A7.1 tool-choice capability probe.

Measures Ollama's Responses and Chat Completions endpoints independently.
An HTTP 200 or free-form text is never counted as a valid tool call.
This is an inference/protocol test ONLY; it does not invoke MCP or Temporal.
"""
import argparse
import json
import sys
import time
import urllib.error
import urllib.request

SCHEMA = {
    "type": "object",
    "additionalProperties": False,
    "properties": {
        "rows": {"type": "array", "minItems": 2, "items": {
            "type": "object", "properties": {
                "id": {"type": "string"}, "price": {"type": "number"},
            }, "required": ["id", "price"], "additionalProperties": False,
        }},
        "identity_field": {"type": "string"},
    },
    "required": ["rows", "identity_field"],
}
PROMPT = (
    "You must inspect the two source rows through cap_0 before answering. "
    "Call cap_0 now with identity_field 'id' and rows "
    "[{'id':'a','price':20},{'id':'a','price':21}]. "
    "Do not answer from memory. /no_think"
)

def request(url, body, timeout=100):
    payload = json.dumps(body).encode("utf-8")
    req = urllib.request.Request(
        url, payload, {"Content-Type": "application/json"},
        method="POST",
    )
    start = time.monotonic()
    try:
        with urllib.request.urlopen(req, timeout=timeout) as resp:
            data = json.load(resp)
            return data, None, round(time.monotonic() - start, 2)
    except urllib.error.HTTPError as exc:
        return None, "HTTP " + str(exc.code), round(time.monotonic() - start, 2)
    except Exception as exc:
        return None, type(exc).__name__, round(time.monotonic() - start, 2)

def valid_arguments(args):
    if not isinstance(args, dict):
        return False
    if set(args) != {"rows", "identity_field"} or args["identity_field"] != "id":
        return False
    rows = args["rows"]
    if not isinstance(rows, list) or len(rows) != 2:
        return False
    return (
        all(isinstance(row, dict) and set(row) == {"id", "price"} for row in rows)
        and all(row["id"] == "a" for row in rows)
        and sorted(row["price"] for row in rows
                   if type(row["price"]) in (float, int)) == [20, 21]
    )

def extract(protocol, data):
    if protocol == "responses":
        outputs = data.get("output", [])
        calls = [x for x in outputs if x.get("type") == "function_call"]
        others = [x.get("type") for x in outputs if x.get("type") not in ("function_call", "reasoning")]
        return calls, others
    choices = data.get("choices", [])
    if len(choices) != 1:
        return [], ["invalid_chat_choices"]
    message = choices[0].get("message", {})
    calls = [{"name": c.get("function", {}).get("name"),
              "arguments": c.get("function", {}).get("arguments")}
             for c in message.get("tool_calls", [])]
    others = ["plain_text"] if (message.get("content") or "").strip() else []
    return calls, others

def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--base-url", default="http://127.0.0.1:11434/v1")
    parser.add_argument("--model", default="qwen3:1.7b")
    parser.add_argument("--output", default="a7-tool-choice-results.json")
    args = parser.parse_args()
    base = args.base_url.rstrip("/")
    if not (base.startswith("http://127.0.0.1:") or base.startswith("http://localhost:")):
        parser.error("only loopback Ollama is allowed in the no-key probe")

    results = []
    for protocol, choice in [
        ("responses", "auto"), ("responses", "required"),
        ("responses", "forced"), ("chat", "auto"),
        ("chat", "required"), ("chat", "forced"),
    ]:
        if protocol == "responses":
            tool = {"type": "function", "name": "cap_0",
                    "description": "Read-only source row profiler. Always inspect the supplied raw rows.",
                    "parameters": SCHEMA, "strict": False}
            tool_choice = ({"type": "function", "name": "cap_0"}
                           if choice == "forced" else choice)
            body = {
                "model": args.model,
                "input": PROMPT,
                "tools": [tool],
                "tool_choice": tool_choice,
                "store": False,
                "max_output_tokens": 480,
            }
        else:
            tool = {"type": "function", "function": {
                "name": "cap_0",
                "description": "Read-only profiler: inspect the supplied source rows before making a claim.",
                "parameters": SCHEMA,
            }}
            tool_choice = ({"type": "function", "function": {"name": "cap_0"}}
                           if choice == "forced" else choice)
            body = {
                "model": args.model,
                "messages": [
                    {"role": "system", "content": "Select cap_0 to inspect source rows. Do not fabricate a result."},
                    {"role": "user", "content": PROMPT},
                ],
                "tools": [tool],
                "tool_choice": tool_choice,
                "max_tokens": 480,
                "stream": False,
            }
        url = base + ("/responses" if protocol == "responses" else "/chat/completions")
        data, error, seconds = request(url, body)
        item = {
            "model": args.model, "protocol": protocol, "tool_choice": choice,
            "latency_seconds": seconds, "http_error": error,
            "function_calls": 0, "other_output_types": [],
            "tool_name_ok": False, "arguments_ok": False, "pass": False,
        }
        if data is not None:
            calls, others = extract(protocol, data)
            item["function_calls"] = len(calls)
            item["other_output_types"] = others
            if len(calls) == 1 and not others:
                item["tool_name_ok"] = calls[0].get("name") == "cap_0"
                try:
                    decoded = json.loads(calls[0].get("arguments") or "")
                    item["arguments_ok"] = valid_arguments(decoded)
                except (ValueError, TypeError):
                    pass
                item["pass"] = item["tool_name_ok"] and item["arguments_ok"]
        results.append(item)
        print(f"{protocol:9} {choice:8} "
              f"PASS={item['pass']} calls={item['function_calls']} "
              f"args={item['arguments_ok']} error={error} "
              f"latency={seconds}s", flush=True)

    with open(args.output, "w", encoding="utf-8") as handle:
        json.dump({"model": args.model, "results": results}, handle, indent=2)
    chat_forced = next(x for x in results if x["protocol"] == "chat" and x["tool_choice"] == "forced")
    print("CHAT_FORCED_VALID=" + str(chat_forced["pass"]).lower(), flush=True)
    # A red CI result is a legitimate experimental result, never silently converted
    # to PASS by parsing a plain-text answer as a function call.
    return 0 if chat_forced["pass"] else 1

if __name__ == "__main__":
    sys.exit(main())
