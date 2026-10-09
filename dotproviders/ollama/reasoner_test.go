package ollama

import (
	"context"
	"encoding/json"
	"github.com/achirothmane/governed-agent-runtime/dotdecision"
	"github.com/achirothmane/governed-agent-runtime/portfoliocontext"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func view() portfoliocontext.ReasoningView {
	return portfoliocontext.ReasoningView{
		SnapshotDigest: strings.Repeat("a", 64),
		NowProjects:    []string{"governed-agent-runtime"},
		RunnableItems: []portfoliocontext.RunnableItem{{
			ID: "dots-runtime-evidence-observe-006", Project: "governed-agent-runtime", Authority: "OBSERVE",
			Objective: "Request evidence", EvidenceRequired: []string{"real PR statuses", "CI"},
			StopConditions: []string{"side effects"},
		}},
		HumanFinalOn: []string{"merge", "paid-spend"},
	}
}
func validDecision() map[string]any {
	return map[string]any{"kind": "PROPOSE_NEXT_GATE", "snapshot_digest": strings.Repeat("a", 64),
		"work_item_id": "dots-runtime-evidence-observe-006", "requested_authority": "OBSERVE",
		"requested_action": "", "rationale": "Check independently that the merged runtime code and CI match the claimed proof; paid model evaluation and managerial quality remain unknown."}
}
func TestLoopbackAndEndpointRestriction(t *testing.T) {
	for _, s := range []string{"https://127.0.0.1:11434/api/chat", "http://example.org:11434/api/chat", "http://127.0.0.1:11434/exec",
		"http://127.0.0.1:11434/api/chat?x=1", "http://169.254.169.254:11434/api/chat", "http://localhost:11434/api/chat#frag"} {
		if _, e := New(Config{Endpoint: s, Model: "qwen3"}); e == nil {
			t.Fatalf("accepted unsafe endpoint: %s", s)
		}
	}
}
func TestStructuredReadOnlyPromptAndSourcePrivacy(t *testing.T) {
	original := validDecision()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/chat" {
			t.Errorf("method/path %s %s", r.Method, r.URL.Path)
		}
		raw, _ := io.ReadAll(r.Body)
		body := string(raw)
		for _, forbidden := range []string{"source_bindings", "data-engine", "private", "OPENAI_API_KEY", "tools"} {
			if strings.Contains(body, "\""+forbidden+"\"") {
				t.Errorf("leak %q", forbidden)
			}
		}
		var q map[string]any
		if err := json.Unmarshal(raw, &q); err != nil {
			t.Error(err)
		}
		if q["stream"] != false || q["think"] != false || q["model"] != "test" {
			t.Errorf("request mismatch: %#v", q)
		}
		b, _ := json.Marshal(original)
		v, _ := json.Marshal(map[string]any{"done": true, "message": map[string]any{"content": string(b)}})
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(v)
	}))
	defer srv.Close()
	reasoner, e := New(Config{Endpoint: srv.URL + "/api/chat", Model: "test"})
	if e != nil {
		t.Fatal(e)
	}
	got, e := reasoner.Decide(context.Background(), view())
	if e != nil {
		t.Fatal(e)
	}
	if got.Kind != dotdecision.ProposeNextGate || got.RequestedAuthority != "OBSERVE" || got.RequestedAction != "" {
		t.Fatalf("unexpected decision %#v", got)
	}
}
func TestRejectFabricatedAuthorityAndAction(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(map[string]any)
	}{
		{"elevated", func(d map[string]any) { d["requested_authority"] = "COMMIT_EXTERNAL" }},
		{"effect", func(d map[string]any) { d["requested_action"] = "merge" }},
		{"wrong-digest", func(d map[string]any) { d["snapshot_digest"] = strings.Repeat("b", 64) }},
		{"wrong-id", func(d map[string]any) { d["work_item_id"] = "other" }},
		{"empty-rationale", func(d map[string]any) { d["rationale"] = "" }},
		{"human-escape", func(d map[string]any) { d["kind"] = "ASK_HUMAN" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := validDecision()
			tc.mutate(d)
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				raw, _ := json.Marshal(d)
				out, _ := json.Marshal(map[string]any{"done": true, "message": map[string]any{"content": string(raw)}})
				_, _ = w.Write(out)
			}))
			defer srv.Close()
			r, e := New(Config{Endpoint: srv.URL + "/api/chat", Model: "test"})
			if e != nil {
				t.Fatal(e)
			}
			if _, e = r.Decide(context.Background(), view()); e == nil {
				t.Fatal("invalid output was admitted")
			}
		})
	}
}
func TestUnreadySnapshotStopsBeforeProvider(t *testing.T) {
	v := view()
	v.RunnableItems = nil
	r, e := New(Config{Model: "qwen3"})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = r.Decide(context.Background(), v); e == nil {
		t.Fatal("nonready decision did not fail closed")
	}
}
