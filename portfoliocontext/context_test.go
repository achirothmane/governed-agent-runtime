package portfoliocontext

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"testing"
)

func fixtureSnapshot(t *testing.T, state string) []byte {
	t.Helper()
	doc := map[string]any{
		"schema_version": 1,
		"state": state,
		"reasons": []any{},
		"source_bindings": []any{
			map[string]any{"path": "SYSTEM-MAP.md", "sha256": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},
		},
		"projection": map[string]any{
			"structural_review": "2026-10-08",
			"now_projects": []any{"portfolio-dot"},
			"now_project_state": []any{
				map[string]any{"id": "portfolio-dot", "freshness": "FRESH", "completeness": "COMPLETE"},
			},
			"runnable_items": []any{
				map[string]any{
					"id": "dots-context-002",
					"project": "portfolio-dot",
					"authority": "PREPARE",
					"objective": "build context",
					"evidence_required": []any{"digest"},
					"stop_conditions": []any{"missing source"},
				},
			},
			"verified_contracts": []any{},
			"human_final_on": []any{"merge", "publish"},
			"execution_principle": "priority-does-not-equal-execution-authority",
			"wip": map[string]any{"now_cap": 3, "now_count": 1, "next_cap": 6, "next_count": 0},
		},
	}
	canonical, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(canonical)
	doc["snapshot_digest"] = hex.EncodeToString(sum[:])
	raw, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestParseExecutableSnapshot(t *testing.T) {
	snapshot, err := Parse(fixtureSnapshot(t, "EXECUTABLE"))
	if err != nil {
		t.Fatal(err)
	}
	view := snapshot.ReasoningView()
	if len(view.RunnableItems) != 1 || view.RunnableItems[0].ID != "dots-context-002" {
		t.Fatalf("view=%#v", view)
	}
	if !snapshot.RequiresHuman("merge") || snapshot.RequiresHuman("prepare") {
		t.Fatal("human authority boundary not preserved")
	}
}

func TestParseRejectsNonExecutableSnapshot(t *testing.T) {
	_, err := Parse(fixtureSnapshot(t, "NON_EXECUTABLE"))
	if !errors.Is(err, ErrNonExecutable) {
		t.Fatalf("error=%v want ErrNonExecutable", err)
	}
}

func TestParseRejectsDigestMutation(t *testing.T) {
	raw := fixtureSnapshot(t, "EXECUTABLE")
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	doc["state"] = "NON_EXECUTABLE"
	mutated, _ := json.Marshal(doc)
	_, err := Parse(mutated)
	if !errors.Is(err, ErrDigestMismatch) {
		t.Fatalf("error=%v want ErrDigestMismatch", err)
	}
}
