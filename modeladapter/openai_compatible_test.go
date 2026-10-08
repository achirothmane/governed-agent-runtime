package modeladapter

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/achirothmane/governed-agent-runtime/agentloop"
	runtimesdk "github.com/achirothmane/governed-agent-runtime/sdk"
)

func testTurn() agentloop.Turn {
	return agentloop.Turn{
		Run: runtimesdk.RunRequest{
			ID: "run-1",
			Conversation: runtimesdk.ConversationRef{
				ID: "conv-1", AgentID: "agent-1", WorkspaceID: "workspace-1",
			},
			Workspace: runtimesdk.WorkspaceSpec{
				ID: "workspace-1", Kind: runtimesdk.WorkspaceEphemeral,
			},
			Input: "Profile the imported dataset.",
			Tools: []runtimesdk.ToolDescriptor{
				{
					Name: "data.profile", Protocol: runtimesdk.ToolProtocolMCP,
					Endpoint: "https://internal.secret.example/mcp",
					ReadOnly: true, SnapshotDigest: "snapshot-one",
				},
				{
					Name: "data.delete", Protocol: runtimesdk.ToolProtocolMCP,
					Endpoint: "https://internal.secret.example/mcp",
					ReadOnly: false, SnapshotDigest: "snapshot-one",
				},
			},
		},
		AgentMission: "Analyze imported data; disclose uncertainty.",
		Step: 1,
	}
}

func testConfig(url string) Config {
	return Config{
		BaseURL: url + "/v1",
		Model: "test-model",
		ToolGuides: map[runtimesdk.ToolName]ToolGuide{
			"data.profile": {
				Description: "Profile rows with no mutations.",
				SnapshotDigest: "snapshot-one",
				InputSchema: json.RawMessage(`{"type":"object","properties":{"dataset":{"type":"string"}},"required":["dataset"]}`),
			},
		},
	}
}

func mockProvider(t *testing.T, choice string, check func(*http.Request, []byte)) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/chat/completions" {
			t.Errorf("unexpected provider request: %s %s", r.Method, r.URL.Path)
			http.Error(w, "unexpected request", http.StatusBadRequest)
			return
		}
		var payload any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Errorf("malformed provider request: %v", err)
		}
		raw, _ := json.Marshal(payload)
		if check != nil {
			check(r, raw)
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"choices":[{"finish_reason":"stop","message":{"content":%q}}]}`, choice)
	}))
}

func TestStructuredToolDecisionUsesOnlyBoundReadOnlySurface(t *testing.T) {
	provider := mockProvider(t,
		`{"kind":"TOOL","tool":"data.profile","arguments_json":"{\\"dataset\\":\\"sales\\"}","message":""}`,
		func(r *http.Request, raw []byte) {
			body := string(raw)
			if !strings.Contains(body, "data.profile") || !strings.Contains(body, "input_schema") {
				t.Error("trusted tool schema was not exposed to model")
			}
			if strings.Contains(body, "data.delete") || strings.Contains(body, "internal.secret.example") {
				t.Error("untrusted authority or private endpoint leaked into model prompt")
			}
			if !strings.Contains(body, `"strict":true`) {
				t.Error("strict JSON schema not requested")
			}
		})
	defer provider.Close()

	decision, err := (Adapter{Config: testConfig(provider.URL)}).Decide(context.Background(), testTurn())
	if err != nil {
		t.Fatal(err)
	}
	if decision.Kind != agentloop.DecisionTool || decision.Tool != "data.profile" ||
		decision.Arguments["dataset"] != "sales" {
		t.Fatalf("unexpected structured tool decision: %#v", decision)
	}
}

func TestTerminalDecision(t *testing.T) {
	provider := mockProvider(t,
		`{"kind":"FINISH","tool":"","arguments_json":"","message":"No conflict found."}`, nil)
	defer provider.Close()
	decision, err := (Adapter{Config: testConfig(provider.URL)}).Decide(context.Background(), testTurn())
	if err != nil {
		t.Fatal(err)
	}
	if decision.Kind != agentloop.DecisionFinish || decision.Message != "No conflict found." {
		t.Fatalf("unexpected finish: %#v", decision)
	}
}

func TestRejectsUnboundOrMutatingModelSelections(t *testing.T) {
	for _, tool := range []string{"data.delete", "other.tool"} {
		t.Run(tool, func(t *testing.T) {
			choice := fmt.Sprintf(`{"kind":"TOOL","tool":%q,"arguments_json":"{}","message":""}`, tool)
			provider := mockProvider(t, choice, nil)
			defer provider.Close()
			_, err := (Adapter{Config: testConfig(provider.URL)}).Decide(context.Background(), testTurn())
			if err == nil {
				t.Fatal("expected unbound/mutating tool rejection")
			}
		})
	}
}

func TestRefusesDriftedSchemaBeforeCallingProvider(t *testing.T) {
	calls := 0
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(http.StatusOK)
	}))
	defer provider.Close()
	cfg := testConfig(provider.URL)
	cfg.ToolGuides["data.profile"] = ToolGuide{
		SnapshotDigest: "wrong-snapshot", InputSchema: json.RawMessage(`{"type":"object"}`),
	}
	_, err := (Adapter{Config: cfg}).Decide(context.Background(), testTurn())
	if err == nil || calls != 0 {
		t.Fatalf("expected snapshot mismatch before provider call, error=%v calls=%d", err, calls)
	}
}

func TestRejectsIncompleteOrMalformedModelOutput(t *testing.T) {
	for name, choice := range map[string]string{
		"invalid JSON":       "not-json",
		"bad arguments":      `{"kind":"TOOL","tool":"data.profile","arguments_json":"[]","message":""}`,
		"missing message":    `{"kind":"ASK","tool":"","arguments_json":"","message":""}`,
		"terminal arguments": `{"kind":"FINISH","tool":"","arguments_json":"{}","message":"done"}`,
	} {
		t.Run(name, func(t *testing.T) {
			provider := mockProvider(t, choice, nil)
			defer provider.Close()
			_, err := (Adapter{Config: testConfig(provider.URL)}).Decide(context.Background(), testTurn())
			if err == nil {
				t.Fatal("accepted invalid provider decision")
			}
		})
	}
}

func TestNeverFollowsRemoteProviderRedirect(t *testing.T) {
	otherCalled := false
	other := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		otherCalled = true
	}))
	defer other.Close()
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, other.URL, http.StatusTemporaryRedirect)
	}))
	defer provider.Close()
	_, err := (Adapter{Config: testConfig(provider.URL)}).Decide(context.Background(), testTurn())
	if err == nil || otherCalled {
		t.Fatalf("unexpected redirect follow: error=%v second_called=%t", err, otherCalled)
	}
}

func TestRejectsNonLoopbackPlaintextProvider(t *testing.T) {
	_, err := completionURL("http://example.com/v1")
	if err == nil {
		t.Fatal("accepted remote plaintext provider")
	}
}
