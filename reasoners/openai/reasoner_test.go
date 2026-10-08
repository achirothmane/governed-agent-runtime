package openaireasoner

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/openai/openai-go/v3/option"
	"github.com/openai/openai-go/v3/responses"

	"github.com/achirothmane/governed-agent-runtime/agentloop"
	"github.com/achirothmane/governed-agent-runtime/mcptransport"
	runtimesdk "github.com/achirothmane/governed-agent-runtime/sdk"
)

func testTurn() agentloop.Turn {
	return agentloop.Turn{
		Run: runtimesdk.RunRequest{
			ID: "run-private-id",
			Conversation: runtimesdk.ConversationRef{
				ID:          "conversation-private-id",
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

func newTestReasoner(t *testing.T, response string, inspect func(map[string]any)) *Reasoner {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/responses" {
			t.Fatalf("path=%q", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer test" {
			t.Fatalf("authorization=%q", got)
		}
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatal(err)
		}
		if inspect != nil {
			var body map[string]any
			if err := json.Unmarshal(raw, &body); err != nil {
				t.Fatal(err)
			}
			inspect(body)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, response)
	}))
	t.Cleanup(server.Close)

	reasoner, err := New(
		Config{Model: "test-model"},
		option.WithAPIKey("test"),
		option.WithUnsafeAllowHTTP(),
		option.WithBaseURL(server.URL+"/v1"),
	)
	if err != nil {
		t.Fatal(err)
	}
	return reasoner
}

func TestReasonerMapsBoundToolCallWithoutLeakingRuntimeAuthority(t *testing.T) {
	var requestBody map[string]any
	reasoner := newTestReasoner(t,
		`{"id":"resp_test","output":[{"type":"function_call","id":"fc_test","call_id":"call_1","name":"runtime_tool_000","arguments":"{\"identity_field\":\"id\",\"rows\":[{\"id\":\"a\",\"price\":20}]}","status":"completed"}]}`,
		func(body map[string]any) { requestBody = body },
	)

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
	for _, forbidden := range []string{
		"private-data-engine.example",
		strings.Repeat("a", 64),
		"run-private-id",
		"conversation-private-id",
	} {
		if strings.Contains(bodyText, forbidden) {
			t.Fatalf("provider request leaked %q: %s", forbidden, bodyText)
		}
	}
	if requestBody["store"] != false {
		t.Fatalf("store=%#v want false", requestBody["store"])
	}
	if requestBody["parallel_tool_calls"] != false {
		t.Fatalf("parallel_tool_calls=%#v want false", requestBody["parallel_tool_calls"])
	}
	if got, ok := requestBody["max_tool_calls"].(float64); !ok || got != 1 {
		t.Fatalf("max_tool_calls=%#v want 1", requestBody["max_tool_calls"])
	}
	tools, ok := requestBody["tools"].([]any)
	if !ok || len(tools) != 4 {
		t.Fatalf("tools=%#v", requestBody["tools"])
	}
}

func TestReasonerMapsTerminalFinish(t *testing.T) {
	reasoner := newTestReasoner(t,
		`{"id":"resp_test","output":[{"type":"function_call","id":"fc_test","call_id":"call_1","name":"runtime_finish","arguments":"{\"message\":\"The rows conflict.\"}","status":"completed"}]}`,
		nil,
	)
	decision, err := reasoner.Decide(context.Background(), testTurn())
	if err != nil {
		t.Fatal(err)
	}
	if decision.Kind != agentloop.DecisionFinish || decision.Message != "The rows conflict." {
		t.Fatalf("decision=%#v", decision)
	}
}

func TestReasonerRejectsPlainTextOrMultipleCallsAsInvalidDecision(t *testing.T) {
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
			reasoner := newTestReasoner(t, tc.response, nil)
			if _, err := reasoner.Decide(context.Background(), testTurn()); !errors.Is(err, agentloop.ErrInvalidDecision) {
				t.Fatalf("error=%v want ErrInvalidDecision", err)
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
	if _, _, err := buildTools(tools); !errors.Is(err, agentloop.ErrInvalidReasoningContext) {
		t.Fatalf("error=%v want ErrInvalidReasoningContext", err)
	}
}

func TestPromptContainsObservationButNotDurableExecutionIdentity(t *testing.T) {
	turn := testTurn()
	turn.Step = 2
	turn.Observations = []agentloop.Observation{{
		Step:         1,
		InvocationID: "private-invocation-id",
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
	for _, forbidden := range []string{"private-invocation-id", strings.Repeat("b", 64), "run-private-id"} {
		if strings.Contains(prompt, forbidden) {
			t.Fatalf("prompt leaked %q: %s", forbidden, prompt)
		}
	}
}

func TestReasonerCapsContextBeforeNetwork(t *testing.T) {
	reasoner, err := New(
		Config{Model: "test-model", MaxContextBytes: 4096},
		option.WithAPIKey("test"),
		option.WithUnsafeAllowHTTP(),
		option.WithBaseURL("http://127.0.0.1:1/v1"),
	)
	if err != nil {
		t.Fatal(err)
	}
	turn := testTurn()
	turn.Run.Input = strings.Repeat("x", 5000)
	if _, err := reasoner.Decide(context.Background(), turn); !errors.Is(err, agentloop.ErrInvalidReasoningContext) {
		t.Fatalf("error=%v want ErrInvalidReasoningContext", err)
	}
}

func TestDecisionFromCallRejectsUnknownFunction(t *testing.T) {
	_, err := decisionFromCall(
		responses.ResponseFunctionToolCallItem{
			Name:      "invented_tool",
			Arguments: "{}",
		},
		map[string]runtimesdk.ToolName{},
	)
	if err == nil {
		t.Fatal("unknown provider function must fail")
	}
}
