package openaireasoner

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	openai "github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"

	"github.com/achirothmane/governed-agent-runtime/agentloop"
	"github.com/achirothmane/governed-agent-runtime/mcptransport"
	runtimesdk "github.com/achirothmane/governed-agent-runtime/sdk"
)

func testTurn() agentloop.Turn {
	return agentloop.Turn{
		Run: runtimesdk.RunRequest{
			ID: "run-1",
			Conversation: runtimesdk.ConversationRef{
				ID:          "conv-1",
				AgentID:     "data-profiler",
				WorkspaceID: "data-engine",
			},
			Workspace: runtimesdk.WorkspaceSpec{
				ID:   "data-engine",
				Kind: runtimesdk.WorkspaceRemote,
			},
			Input: "Inspect the supplied rows and report whether the raw evidence conflicts.",
			Tools: []runtimesdk.ToolDescriptor{{
				Name:        "data.profile",
				Protocol:    runtimesdk.ToolProtocolMCP,
				Endpoint:    "http://private-data-engine.example/mcp",
				Title:       "Profile raw data",
				Description: "Profile raw rows without transforming them.",
				InputSchema: json.RawMessage(`{
					"type":"object",
					"properties":{
						"identity_field":{"type":"string"},
						"rows":{"type":"array","items":{"type":"object"}}
					},
					"required":["rows"]
				}`),
				ReadOnly:       true,
				SnapshotDigest: strings.Repeat("a", 64),
			}},
		},
		AgentMission: "Inspect evidence before answering.",
		Step:         1,
	}
}

func TestReasonerMapsBoundToolCallWithoutLeakingAuthorityMetadata(t *testing.T) {
	var requestBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/responses" {
			t.Fatalf("path=%q", r.URL.Path)
		}
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(raw, &requestBody); err != nil {
			t.Fatal(err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"resp_test","output":[{"type":"function_call","id":"fc_test","call_id":"call_1","name":"runtime_tool_000","arguments":"{\"identity_field\":\"id\",\"rows\":[{\"id\":\"a\",\"price\":20}]}","status":"completed"}]}`)
	}))
	defer server.Close()

	reasoner, err := New(Config{Model: "test-model"},
		option.WithAPIKey("test"),
		option.WithBaseURL(server.URL+"/v1"),
	)
	if err != nil {
		t.Fatal(err)
	}
	decision, err := reasoner.Decide(context.Background(), testTurn())
	if err != nil {
		t.Fatal(err)
	}
	if decision.Kind != agentloop.DecisionTool || decision.Tool != "data.profile" {
		t.Fatalf("decision=%#v", decision)
	}
	if decision.Arguments["identity_field"] != "id" {
		t.Fatalf("arguments=%#v", decision.Arguments)
	}

	bodyJSON, _ := json.Marshal(requestBody)
	bodyText := string(bodyJSON)
	if strings.Contains(bodyText, "private-data-engine.example") {
		t.Fatalf("provider request leaked MCP endpoint: %s", bodyText)
	}
	if strings.Contains(bodyText, strings.Repeat("a", 64)) {
		t.Fatalf("provider request leaked capability digest: %s", bodyText)
	}
	if requestBody["store"] != false {
		t.Fatalf("store=%#v want false", requestBody["store"])
	}
	if requestBody["parallel_tool_calls"] != false {
		t.Fatalf("parallel_tool_calls=%#v want false", requestBody["parallel_tool_calls"])
	}
	tools, ok := requestBody["tools"].([]any)
	if !ok || len(tools) != 4 {
		t.Fatalf("tools=%#v", requestBody["tools"])
	}
}

func TestReasonerMapsTerminalFinish(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"resp_test","output":[{"type":"function_call","id":"fc_test","call_id":"call_1","name":"runtime_finish","arguments":"{\"message\":\"The rows conflict.\"}","status":"completed"}]}`)
	}))
	defer server.Close()

	reasoner, err := New(Config{Model: "test-model"},
		option.WithAPIKey("test"),
		option.WithBaseURL(server.URL+"/v1"),
	)
	if err != nil {
		t.Fatal(err)
	}
	decision, err := reasoner.Decide(context.Background(), testTurn())
	if err != nil {
		t.Fatal(err)
	}
	if decision.Kind != agentloop.DecisionFinish || decision.Message != "The rows conflict." {
		t.Fatalf("decision=%#v", decision)
	}
}

func TestReasonerRejectsPlainTextOrMultipleCalls(t *testing.T) {
	tests := []struct {
		name     string
		response string
	}{
		{
			name:     "plain text",
			response: `{"id":"resp_test","output":[{"type":"message","id":"msg_1","role":"assistant","content":[{"type":"output_text","text":"done","annotations":[]}],"status":"completed"}]}`,
		},
		{
			name:     "multiple calls",
			response: `{"id":"resp_test","output":[{"type":"function_call","id":"fc_1","call_id":"c1","name":"runtime_finish","arguments":"{\"message\":\"one\"}","status":"completed"},{"type":"function_call","id":"fc_2","call_id":"c2","name":"runtime_finish","arguments":"{\"message\":\"two\"}","status":"completed"}]}`,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, tc.response)
			}))
			defer server.Close()

			reasoner, err := New(Config{Model: "test-model"},
				option.WithAPIKey("test"),
				option.WithBaseURL(server.URL+"/v1"),
			)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := reasoner.Decide(context.Background(), testTurn()); err == nil {
				t.Fatal("expected fail-closed response")
			}
		})
	}
}

func TestBuildToolsRejectsMissingSchemaAndExcludesMutatingTools(t *testing.T) {
	tools := []runtimesdk.ToolDescriptor{
		{
			Name:        "safe",
			Protocol:    runtimesdk.ToolProtocolMCP,
			Endpoint:    "http://example/mcp",
			ReadOnly:    true,
			InputSchema: json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`),
		},
		{
			Name:        "write",
			Protocol:    runtimesdk.ToolProtocolMCP,
			Endpoint:    "http://example/mcp",
			ReadOnly:    false,
			InputSchema: json.RawMessage(`{"type":"object","properties":{}}`),
		},
	}
	params, bindings, err := buildTools(tools)
	if err != nil {
		t.Fatal(err)
	}
	if len(params) != 4 {
		t.Fatalf("tool count=%d want 4", len(params))
	}
	if len(bindings) != 1 || bindings["runtime_tool_000"] != "safe" {
		t.Fatalf("bindings=%#v", bindings)
	}

	tools[0].InputSchema = nil
	if _, _, err := buildTools(tools); err == nil {
		t.Fatal("missing model-visible schema must fail")
	}
}

func TestPromptContainsObservationButNotSnapshotDigest(t *testing.T) {
	turn := testTurn()
	turn.Step = 2
	turn.Observations = []agentloop.Observation{{
		Step:         1,
		InvocationID: "a6-01-abc",
		Tool:         "data.profile",
		Result: mcptransport.Result{
			Tool:           "data.profile",
			SnapshotDigest: strings.Repeat("b", 64),
			StructuredContent: json.RawMessage(
				`{"stage":"PRE_SEMANTIC_PROFILE","decision":"CONFLICTING"}`,
			),
		},
	}}
	prompt, err := buildPrompt(turn)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(prompt, "PRE_SEMANTIC_PROFILE") || !strings.Contains(prompt, "CONFLICTING") {
		t.Fatalf("prompt=%s", prompt)
	}
	if strings.Contains(prompt, strings.Repeat("b", 64)) {
		t.Fatalf("prompt leaked snapshot digest: %s", prompt)
	}
}

func TestNewRequiresModelAndReasonerCapsContext(t *testing.T) {
	if _, err := New(Config{}); err == nil {
		t.Fatal("missing model must fail")
	}
	reasoner, err := New(Config{Model: "test-model", MaxContextBytes: 4096},
		option.WithAPIKey("test"),
		option.WithBaseURL("http://127.0.0.1:1/v1"),
	)
	if err != nil {
		t.Fatal(err)
	}
	turn := testTurn()
	turn.Run.Input = strings.Repeat("x", 5000)
	if _, err := reasoner.Decide(context.Background(), turn); err == nil {
		t.Fatal("oversized context must fail before network")
	}
}

func TestSDKCurrentVersionSupportsConfiguredClient(t *testing.T) {
	_ = openai.NewClient
}
