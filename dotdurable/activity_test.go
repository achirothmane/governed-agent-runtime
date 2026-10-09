package dotdurable

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sync"
	"testing"

	"github.com/achirothmane/governed-agent-runtime/dotdecision"
	"github.com/achirothmane/governed-agent-runtime/portfoliocontext"
)

type memorySnapshotLoader struct {
	snapshot portfoliocontext.Snapshot
}

func (l memorySnapshotLoader) LoadSnapshot(_ context.Context, digest string) (portfoliocontext.Snapshot, error) {
	if l.snapshot.SnapshotDigest != digest {
		return portfoliocontext.Snapshot{}, ErrSnapshotMismatch
	}
	return l.snapshot, nil
}

type countingReasoner struct {
	mu       sync.Mutex
	calls    int
	decision dotdecision.Decision
}

func (r *countingReasoner) Decide(_ context.Context, _ portfoliocontext.ReasoningView) (dotdecision.Decision, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls++
	return r.decision, nil
}

func (r *countingReasoner) Count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.calls
}

type memoryStore struct {
	mu      sync.Mutex
	records map[string]Record
}

func newMemoryStore() *memoryStore {
	return &memoryStore{records: map[string]Record{}}
}

func (s *memoryStore) GetCommittedDecision(_ context.Context, id string) (Record, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	record, ok := s.records[id]
	if !ok {
		return Record{}, ErrDecisionNotFound
	}
	return record, nil
}

func (s *memoryStore) PutCommittedDecision(_ context.Context, record Record) (Record, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if existing, ok := s.records[record.DecisionID]; ok {
		if !SameRecord(existing, record) {
			return Record{}, false, ErrDecisionConflict
		}
		return existing, false, nil
	}
	s.records[record.DecisionID] = record
	return record, true, nil
}

func durableSnapshotFixture(t *testing.T) portfoliocontext.Snapshot {
	t.Helper()
	doc := map[string]any{
		"schema_version": 1,
		"state":          "EXECUTABLE",
		"reasons":        []any{},
		"source_bindings": []any{
			map[string]any{"path": "SYSTEM-MAP.md", "sha256": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},
			map[string]any{"path": "portfolio/execution-queue.yaml", "sha256": "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"},
		},
		"projection": map[string]any{
			"structural_review": "2026-10-08",
			"now_projects":      []any{"portfolio-dot"},
			"now_project_state": []any{
				map[string]any{"id": "portfolio-dot", "freshness": "FRESH", "completeness": "COMPLETE"},
			},
			"runnable_items": []any{
				map[string]any{
					"id":                "dots-durable-decision-loop-004",
					"project":           "portfolio-dot",
					"authority":         "PREPARE",
					"objective":         "durably commit validated decision",
					"evidence_required": []any{"snapshot binding", "replay"},
					"stop_conditions":   []any{"authority escape"},
				},
			},
			"verified_contracts":  []any{},
			"human_final_on":      []any{"merge", "release-or-publication", "paid-spend"},
			"execution_principle": "priority-does-not-equal-execution-authority",
			"wip":                 map[string]any{"now_cap": 3, "now_count": 1, "next_cap": 6, "next_count": 0},
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
	snapshot, err := portfoliocontext.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	return snapshot
}

func validDurableDecision(snapshot portfoliocontext.Snapshot) dotdecision.Decision {
	return dotdecision.Decision{
		Kind:               dotdecision.ProposeNextGate,
		SnapshotDigest:     snapshot.SnapshotDigest,
		WorkItemID:         "dots-durable-decision-loop-004",
		RequestedAuthority: "PREPARE",
		RequestedAction:    "prepare-durable-decision-loop",
		Rationale:          "The current sealed snapshot admits exactly one PREPARE D4 work item.",
	}
}

func TestDecideActivityCommitsOnceAndReusesCommittedRecord(t *testing.T) {
	snapshot := durableSnapshotFixture(t)
	reasoner := &countingReasoner{decision: validDurableDecision(snapshot)}
	store := newMemoryStore()
	activity := DecideActivity{
		Snapshots: memorySnapshotLoader{snapshot: snapshot},
		Reasoner:  reasoner,
		Store:     store,
	}
	req := Request{DecisionID: "decision-004", SnapshotDigest: snapshot.SnapshotDigest}

	first, err := activity.Execute(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	second, err := activity.Execute(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if !SameRecord(first, second) {
		t.Fatalf("replay changed committed record: first=%#v second=%#v", first, second)
	}
	if reasoner.Count() != 1 {
		t.Fatalf("reasoner calls=%d want=1", reasoner.Count())
	}
}

func TestInvalidDecisionNeverCommits(t *testing.T) {
	snapshot := durableSnapshotFixture(t)
	decision := validDurableDecision(snapshot)
	decision.RequestedAuthority = "COMMIT_EXTERNAL"
	reasoner := &countingReasoner{decision: decision}
	store := newMemoryStore()
	activity := DecideActivity{
		Snapshots: memorySnapshotLoader{snapshot: snapshot},
		Reasoner:  reasoner,
		Store:     store,
	}
	req := Request{DecisionID: "decision-invalid", SnapshotDigest: snapshot.SnapshotDigest}

	if _, err := activity.Execute(context.Background(), req); err == nil {
		t.Fatal("authority escalation was committed")
	}
	if _, err := store.GetCommittedDecision(context.Background(), req.DecisionID); !errors.Is(err, ErrDecisionNotFound) {
		t.Fatalf("invalid decision reached store: %v", err)
	}
}

func TestRequestRejectsMalformedSnapshotDigest(t *testing.T) {
	if err := (Request{DecisionID: "x", SnapshotDigest: "not-a-digest"}).Validate(); err == nil {
		t.Fatal("malformed snapshot digest accepted")
	}
}
