package dotdecision

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"testing"

	"github.com/achirothmane/governed-agent-runtime/portfoliocontext"
)

func snapshotFixture(t *testing.T) portfoliocontext.Snapshot {
	t.Helper()
	doc := map[string]any{
		"schema_version": 1,
		"state":          "EXECUTABLE",
		"reasons":        []any{},
		"source_bindings": []any{
			map[string]any{"path": "portfolio/execution-queue.yaml", "sha256": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},
		},
		"projection": map[string]any{
			"structural_review": "2026-10-08",
			"now_projects":      []any{"portfolio-dot"},
			"now_project_state": []any{
				map[string]any{"id": "portfolio-dot", "freshness": "FRESH", "completeness": "COMPLETE"},
			},
			"runnable_items": []any{
				map[string]any{
					"id":                "dots-decision-contract-003",
					"project":           "portfolio-dot",
					"authority":         "PREPARE",
					"objective":         "prove typed decision contract",
					"evidence_required": []any{"snapshot digest"},
					"stop_conditions":   []any{"authority escape"},
				},
			},
			"verified_contracts":  []any{},
			"human_final_on":      []any{"merge", "release-or-publication", "paid-spend"},
			"execution_principle": "priority-does-not-equal-execution-authority",
			"wip":                 map[string]any{"now_cap": 3, "now_count": 1, "next_cap": 6, "next_count": 0},
		},
	}
	raw, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(raw)
	doc["snapshot_digest"] = hex.EncodeToString(sum[:])
	raw, err = json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := portfoliocontext.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	return snapshot
}

func validDecision(snapshot portfoliocontext.Snapshot) Decision {
	return Decision{
		Kind:               ProposeNextGate,
		SnapshotDigest:     snapshot.SnapshotDigest,
		WorkItemID:         "dots-decision-contract-003",
		RequestedAuthority: "PREPARE",
		RequestedAction:    "prepare-draft",
		Rationale:          "The work item is READY and its evidence gate is explicit.",
	}
}

func TestValidateDecision(t *testing.T) {
	snapshot := snapshotFixture(t)
	if err := Validate(snapshot, validDecision(snapshot)); err != nil {
		t.Fatal(err)
	}
}

func TestRejectsSnapshotMismatch(t *testing.T) {
	snapshot := snapshotFixture(t)
	decision := validDecision(snapshot)
	decision.SnapshotDigest = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	if !errors.Is(Validate(snapshot, decision), ErrSnapshotMismatch) {
		t.Fatal("expected snapshot mismatch")
	}
}

func TestRejectsUnknownWorkItem(t *testing.T) {
	snapshot := snapshotFixture(t)
	decision := validDecision(snapshot)
	decision.WorkItemID = "hallucinated-work"
	if !errors.Is(Validate(snapshot, decision), ErrWorkItemUnknown) {
		t.Fatal("expected unknown work item")
	}
}

func TestRejectsAuthorityEscalation(t *testing.T) {
	snapshot := snapshotFixture(t)
	decision := validDecision(snapshot)
	decision.RequestedAuthority = "COMMIT_EXTERNAL"
	if !errors.Is(Validate(snapshot, decision), ErrAuthorityExceeded) {
		t.Fatal("expected authority rejection")
	}
}

func TestHumanFinalActionRequiresAskHuman(t *testing.T) {
	snapshot := snapshotFixture(t)
	decision := validDecision(snapshot)
	decision.RequestedAction = "merge"
	if !errors.Is(Validate(snapshot, decision), ErrHumanRequired) {
		t.Fatal("expected human requirement")
	}
	decision.Kind = AskHuman
	if err := Validate(snapshot, decision); err != nil {
		t.Fatal(err)
	}
}
