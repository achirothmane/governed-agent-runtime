#!/usr/bin/env python3
"""Generate deterministic, SYNTHETIC D4/D5 sealed snapshots solely for CI.

Not a live user's portfolio, a genuine ready work item, or a real Dots decision.
"""
import argparse
import hashlib
import json
from pathlib import Path

ITEMS = {
    "d4": ("dots-durable-decision-loop-004", "prove durable commit and replay with local reasoner"),
    "d5": ("dots-model-provider-adapter-005", "prove structured model adapter with fake HTTP response"),
}


def main():
    p = argparse.ArgumentParser()
    p.add_argument("--mode", required=True, choices=sorted(ITEMS))
    p.add_argument("--output", required=True, type=Path)
    args = p.parse_args()
    item_id, objective = ITEMS[args.mode]
    snapshot = {
        "schema_version": 1,
        "state": "EXECUTABLE",
        "reasons": [],
        "source_bindings": [
            {"path": "synthetic://ci/fixture", "sha256": "b" * 64},
        ],
        "projection": {
            "structural_review": "synthetic-fixture-only",
            "now_projects": ["portfolio-dot"],
            "now_project_state": [
                {"id": "portfolio-dot", "freshness": "FRESH", "completeness": "COMPLETE"},
            ],
            "runnable_items": [{
                "id": item_id,
                "project": "portfolio-dot",
                "authority": "PREPARE",
                "objective": objective,
                "evidence_required": ["local CI assertions"],
                "stop_conditions": ["model output invalid", "authority exceeded"],
            }],
            "verified_contracts": [],
            "human_final_on": ["merge", "release-or-publication", "paid-spend"],
            "execution_principle": "priority-does-not-equal-execution-authority",
            "wip": {"now_cap": 3, "now_count": 1, "next_cap": 6, "next_count": 0},
        },
    }
    # encoding/json of a string-key Go map uses sorted keys and compact JSON.
    encoded = json.dumps(snapshot, sort_keys=True, separators=(",", ":"), ensure_ascii=True).encode("utf-8")
    snapshot["snapshot_digest"] = hashlib.sha256(encoded).hexdigest()
    args.output.parent.mkdir(parents=True, exist_ok=True)
    args.output.write_text(json.dumps(snapshot, sort_keys=True, separators=(",", ":")) + "\n", encoding="utf-8")
    print("Generated", args.mode, "SYNTHETIC snapshot SHA256:", snapshot["snapshot_digest"])


if __name__ == "__main__":
    main()
