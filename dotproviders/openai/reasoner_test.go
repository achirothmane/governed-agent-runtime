package openai

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/achirothmane/governed-agent-runtime/dotdecision"
	"github.com/achirothmane/governed-agent-runtime/dotdurable"
	"github.com/achirothmane/governed-agent-runtime/portfoliocontext"
)

func reasonerViewFixture() portfoliocontext.ReasoningView {
	return portfoliocontext.ReasoningView{
		SnapshotDigest:   "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		StructuralReview: "2026-10-08",
		NowProjects:      []string{"portfolio-dot"},
		NowProjectState: []portfoliocontext.ProjectState{
			{ID: "portfolio-dot", Freshness: "FRESH", Completeness: "COMPLETE"},
		},
		RunnableItems: []portfoliocontext.RunnableItem{
			{
				ID:               "dots-model-provider-adapter-005",
				Project:          "portfolio-dot",
				Authority:        "PREPARE",
				Objective:        "bind one provider adapter",
				EvidenceRequired: []string{"typed output", "D3 validation"},
				StopConditions:   []string{"authority escape"},
			},
		},
		HumanFinalOn:       []string{"merge", "release-or-publication", "paid-spend"},
		ExecutionPrinciple: "priority-does-not-equal-execution-authority",
		WIP:                portfoliocontext.WIP{NowCap: 3, NowCount: 1, NextCap: 6},
	}
}

func responseJSON(t *testing.T, decision map[string]any) []byte {
	t.Helper()
	rawDecision, err := json.Marshal(decision)
	if err != nil {
		t.Fatal(err)
	}
	envelope := map[string]any{
		"status": "completed",
		"output": []any{
			map[string]any{
				"type": "message",
				"role": "assistant",
				"content": []any{
					map[string]any{"type": "output_text", "text": string(rawDecision)},
				},
			},
		},
	}
	raw, err := json.Marshal(envelope)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func validProviderDecision() map[string]any {
	return map[string]any{
		"kind":                "PROPOSE_NEXT_GATE",
		"snapshot_digest":     "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		"work_item_id":        "dots-model-provider-adapter-005",
		"requested_authority": "PREPARE",
		"requested_action":    "prepare-provider-adapter",
		"rationale":           "The sealed view admits D5 with PREPARE authority.",
	}
}

func TestReasonerUsesResponsesStructuredOutputAndBoundedViewOnly(t *testing.T) {
	var captured []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.Method != http.MethodPost {
			t.Fatalf("method=%s", req.Method)
		}
		if req.Header.Get("Authorization") != "Bearer test-key" {
			t.Fatalf("authorization=%q", req.Header.Get("Authorization"))
		}
		var err error
		captured, err = io.ReadAll(req.Body)
		if err != nil {
			t.Fatal(err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(responseJSON(t, validProviderDecision()))
	}))
	defer server.Close()

	reasoner, err := New(Config{
		Endpoint:   server.URL,
		Model:      "test-model",
		APIKey:     "test-key",
		HTTPClient: server.Client(),
	})
	if err != nil {
		t.Fatal(err)
	}
	decision, err := reasoner.Decide(context.Background(), reasonerViewFixture())
	if err != nil {
		t.Fatal(err)
	}
	if decision.Kind != dotdecision.ProposeNextGate ||
		decision.WorkItemID != "dots-model-provider-adapter-005" ||
		decision.RequestedAuthority != "PREPARE" {
		t.Fatalf("decision=%#v", decision)
	}

	body := string(captured)
	for _, forbidden := range []string{"source_bindings", "SYSTEM-MAP.md", "portfolio/execution-queue.yaml", "\"tools\""} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("request leaked forbidden surface %q: %s", forbidden, body)
		}
	}
	var decoded map[string]any
	if err := json.Unmarshal(captured, &decoded); err != nil {
		t.Fatal(err)
	}
	textBlock := decoded["text"].(map[string]any)
	format := textBlock["format"].(map[string]any)
	if format["type"] != "json_schema" || format["strict"] != true || format["name"] != "portfolio_dot_decision" {
		t.Fatalf("format=%#v", format)
	}
}

func TestReasonerRejectsMalformedOrRefusedOutput(t *testing.T) {
	tests := []struct {
		name string
		body string
		want error
	}{
		{
			name: "malformed decision",
			body: `{"status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"not-json"}]}]}`,
			want: ErrInvalidOutput,
		},
		{
			name: "refusal",
			body: `{"status":"completed","output":[{"type":"message","content":[{"type":"refusal","refusal":"cannot comply"}]}]}`,
			want: ErrProviderRefusal,
		},
		{
			name: "incomplete",
			body: `{"status":"incomplete","output":[]}`,
			want: ErrProvider,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, tc.body)
			}))
			defer server.Close()
			reasoner, err := New(Config{Endpoint: server.URL, Model: "test-model", APIKey: "x", HTTPClient: server.Client()})
			if err != nil {
				t.Fatal(err)
			}
			_, err = reasoner.Decide(context.Background(), reasonerViewFixture())
			if !errors.Is(err, tc.want) {
				t.Fatalf("error=%v want=%v", err, tc.want)
			}
		})
	}
}

type snapshotLoader struct {
	snapshot portfoliocontext.Snapshot
}

func (l snapshotLoader) LoadSnapshot(_ context.Context, digest string) (portfoliocontext.Snapshot, error) {
	if digest != l.snapshot.SnapshotDigest {
		return portfoliocontext.Snapshot{}, dotdurable.ErrSnapshotMismatch
	}
	return l.snapshot, nil
}

type decisionStore struct {
	mu      sync.Mutex
	records map[string]dotdurable.Record
}

func (s *decisionStore) GetCommittedDecision(_ context.Context, id string) (dotdurable.Record, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	record, ok := s.records[id]
	if !ok {
		return dotdurable.Record{}, dotdurable.ErrDecisionNotFound
	}
	return record, nil
}

func (s *decisionStore) PutCommittedDecision(_ context.Context, record dotdurable.Record) (dotdurable.Record, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if previous, ok := s.records[record.DecisionID]; ok {
		if !dotdurable.SameRecord(previous, record) {
			return dotdurable.Record{}, false, dotdurable.ErrDecisionConflict
		}
		return previous, false, nil
	}
	s.records[record.DecisionID] = record
	return record, true, nil
}

func snapshotForProviderTest(t *testing.T) portfoliocontext.Snapshot {
	t.Helper()
	doc := map[string]any{
		"schema_version": 1,
		"state":          "EXECUTABLE",
		"reasons":        []any{},
		"source_bindings": []any{
			map[string]any{"path": "private-source", "sha256": "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"},
		},
		"projection": map[string]any{
			"structural_review": "2026-10-08",
			"now_projects":      []any{"portfolio-dot"},
			"now_project_state": []any{
				map[string]any{"id": "portfolio-dot", "freshness": "FRESH", "completeness": "COMPLETE"},
			},
			"runnable_items": []any{
				map[string]any{
					"id":                "dots-model-provider-adapter-005",
					"project":           "portfolio-dot",
					"authority":         "PREPARE",
					"objective":         "bind provider",
					"evidence_required": []any{"typed output"},
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

func TestAuthorityEscalatingProviderDecisionNeverCommits(t *testing.T) {
	snapshot := snapshotForProviderTest(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		decision := map[string]any{
			"kind":                "PROPOSE_NEXT_GATE",
			"snapshot_digest":     snapshot.SnapshotDigest,
			"work_item_id":        "dots-model-provider-adapter-005",
			"requested_authority": "COMMIT_EXTERNAL",
			"requested_action":    "merge",
			"rationale":           "Escalate authority.",
		}
		_, _ = w.Write(responseJSON(t, decision))
	}))
	defer server.Close()
	reasoner, err := New(Config{Endpoint: server.URL, Model: "test-model", APIKey: "x", HTTPClient: server.Client()})
	if err != nil {
		t.Fatal(err)
	}
	store := &decisionStore{records: map[string]dotdurable.Record{}}
	activity := dotdurable.DecideActivity{
		Snapshots: snapshotLoader{snapshot: snapshot},
		Reasoner:  reasoner,
		Store:     store,
	}
	_, err = activity.Execute(context.Background(), dotdurable.Request{
		DecisionID:     "provider-escalation",
		SnapshotDigest: snapshot.SnapshotDigest,
	})
	if err == nil {
		t.Fatal("authority escalation was committed")
	}
	if _, getErr := store.GetCommittedDecision(context.Background(), "provider-escalation"); !errors.Is(getErr, dotdurable.ErrDecisionNotFound) {
		t.Fatalf("invalid provider decision reached store: %v", getErr)
	}
}

func TestNewRequiresModelAndAPIKey(t *testing.T) {
	if _, err := New(Config{Model: "x"}); !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("missing API key error=%v", err)
	}
	if _, err := New(Config{APIKey: "x"}); !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("missing model error=%v", err)
	}
}
