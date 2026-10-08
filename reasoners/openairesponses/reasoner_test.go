package openairesponses

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	openai "github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
	"github.com/openai/openai-go/v3/responses"

	"github.com/achirothmane/governed-agent-runtime/agentloop"
	"github.com/achirothmane/governed-agent-runtime/mcptransport"
	runtimesdk "github.com/achirothmane/governed-agent-runtime/sdk"
)

type fakeResponses struct {
	output string
	err    error
	calls  int
	params responses.ResponseNewParams
}

func (f *fakeResponses) Create(_ context.Context, params responses.ResponseNewParams) (string, error) {
	f.calls++
	f.params = params
	if f.err != nil {
		return "", f.err
	}
	return f.output, nil
}

func testRun() runtimesdk.RunRequest {
	return runtimesdk.RunRequest{
		ID: "run-secret-id",
		Conversation: runtimesdk.ConversationRef{
			ID:          "conversation-secret-id",
			AgentID:     "data-profiler",
			WorkspaceID: "data-engine",
		},
		Workspace: runtimesdk.WorkspaceSpec{
			ID:   "data-engine",
			Kind: runtimesdk.WorkspaceRemote,
		},
		Input: "Profile these rows and tell me whether the raw evidence conflicts.",
		Tools: []runtimesdk.ToolDescriptor{{
			Name:           "data.profile",
			Protocol:       runtimesdk.ToolProtocolMCP,
			Endpoint:       "https://private-data-engine.example/mcp",
			Title:          "Profile raw data",
			Description:    "Profile raw rows without transforming source values.",
			InputSchema:    json.RawMessage(`{"type":"object","additionalProperties":false,"properties":{"rows":{"type":"array"},"identity_field":{"type":"string"}},"required":["rows"]}`),
			ReadOnly:       true,
			SnapshotDigest: strings.Repeat("a", 64),
		}},
	}
}

func TestReasonerReturnsBoundToolDecisionWithoutExposingAuthorityFields(t *testing.T) {
	client := &fakeResponses{output: `{"kind":"TOOL","tool":"data.profile","arguments_json":"{\"rows\":[{\"id\":\"a\",\"price\":20}]}","message":""}`}
	reasoner, err := NewWithClient(Config{Model: "test-model"}, client)
	if err != nil {
		t.Fatal(err)
	}
	decision, err := reasoner.Decide(context.Background(), agentloop.Turn{
		Run:          testRun(),
		AgentMission: "Profile evidence without transforming it.",
		Step:         1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if decision.Kind != agentloop.DecisionTool || decision.Tool != "data.profile" {
		t.Fatalf("decision=%#v", decision)
	}
	rows, ok := decision.Arguments["rows"].([]any)
	if !ok || len(rows) != 1 {
		t.Fatalf("arguments=%#v", decision.Arguments)
	}
	if client.calls != 1 {
		t.Fatalf("client calls=%d", client.calls)
	}

	payload, err := json.Marshal(client.params)
	if err != nil {
		t.Fatal(err)
	}
	wire := string(payload)
	for _, forbidden := range []string{
		"https://private-data-engine.example/mcp",
		strings.Repeat("a", 64),
		"run-secret-id",
		"conversation-secret-id",
	} {
		if strings.Contains(wire, forbidden) {
			t.Fatalf("provider request leaked authority/internal field %q: %s", forbidden, wire)
		}
	}
	for _, required := range []string{
		"data.profile",
		"Profile raw rows without transforming source values.",
		"ai_native_runtime_decision",
		"test-model",
	} {
		if !strings.Contains(wire, required) {
			t.Fatalf("provider request missing %q: %s", required, wire)
		}
	}
}

func TestReasonerFeedsNormalizedObservationWithoutInvocationIdentity(t *testing.T) {
	client := &fakeResponses{output: `{"kind":"FINISH","tool":"","arguments_json":"{}","message":"The raw evidence conflicts."}`}
	reasoner, err := NewWithClient(Config{Model: "test-model"}, client)
	if err != nil {
		t.Fatal(err)
	}
	_, err = reasoner.Decide(context.Background(), agentloop.Turn{
		Run:          testRun(),
		AgentMission: "Profile evidence.",
		Step:         2,
		Observations: []agentloop.Observation{{
			Step:         1,
			InvocationID: "private-invocation-id",
			Tool:         "data.profile",
			Result: mcptransport.Result{
				Tool:              "data.profile",
				SnapshotDigest:    strings.Repeat("b", 64),
				StructuredContent: json.RawMessage(`{"stage":"PRE_SEMANTIC_PROFILE","decision":"CONFLICTING"}`),
			},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	payload, _ := json.Marshal(client.params)
	wire := string(payload)
	if strings.Contains(wire, "private-invocation-id") || strings.Contains(wire, strings.Repeat("b", 64)) {
		t.Fatalf("provider request leaked durable execution identity: %s", wire)
	}
	if !strings.Contains(wire, "CONFLICTING") {
		t.Fatalf("provider request omitted normalized observation: %s", wire)
	}
}

func TestReasonerRejectsUnboundToolOutput(t *testing.T) {
	client := &fakeResponses{output: `{"kind":"TOOL","tool":"data.delete","arguments_json":"{}","message":""}`}
	reasoner, err := NewWithClient(Config{Model: "test-model"}, client)
	if err != nil {
		t.Fatal(err)
	}
	_, err = reasoner.Decide(context.Background(), agentloop.Turn{
		Run: testRun(), AgentMission: "Profile evidence.", Step: 1,
	})
	if !errors.Is(err, agentloop.ErrInvalidDecision) {
		t.Fatalf("error=%v want ErrInvalidDecision", err)
	}
}

func TestReasonerRejectsMissingSchemaBeforeProviderCall(t *testing.T) {
	client := &fakeResponses{}
	reasoner, err := NewWithClient(Config{Model: "test-model"}, client)
	if err != nil {
		t.Fatal(err)
	}
	run := testRun()
	run.Tools[0].InputSchema = nil
	_, err = reasoner.Decide(context.Background(), agentloop.Turn{
		Run: run, AgentMission: "Profile evidence.", Step: 1,
	})
	if !errors.Is(err, agentloop.ErrInvalidReasoningContext) || client.calls != 0 {
		t.Fatalf("error=%v client calls=%d", err, client.calls)
	}
}

func TestReasonerBoundsProviderInputBeforeAPICall(t *testing.T) {
	client := &fakeResponses{}
	reasoner, err := NewWithClient(Config{
		Model:         "test-model",
		MaxInputBytes: 1024,
	}, client)
	if err != nil {
		t.Fatal(err)
	}
	run := testRun()
	run.Input = strings.Repeat("x", 4096)
	_, err = reasoner.Decide(context.Background(), agentloop.Turn{
		Run: run, AgentMission: "Profile evidence.", Step: 1,
	})
	if !errors.Is(err, agentloop.ErrInvalidReasoningContext) ||
		!errors.Is(err, ErrInputTooLarge) ||
		client.calls != 0 {
		t.Fatalf("error=%v client calls=%d", err, client.calls)
	}
}

func TestOfficialSDKResponsesWireContract(t *testing.T) {
	var captured []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.URL.Path != "/responses" || req.Method != http.MethodPost {
			t.Fatalf("request=%s %s", req.Method, req.URL.Path)
		}
		if got := req.Header.Get("Authorization"); got != "Bearer test-key" {
			t.Fatalf("authorization=%q", got)
		}
		var err error
		captured, err = io.ReadAll(req.Body)
		if err != nil {
			t.Fatal(err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{
			"id":"resp_test",
			"object":"response",
			"created_at":1,
			"status":"completed",
			"model":"test-model",
			"output":[{
				"id":"msg_test",
				"type":"message",
				"role":"assistant",
				"status":"completed",
				"content":[{
					"type":"output_text",
					"text":"{\"kind\":\"FINISH\",\"tool\":\"\",\"arguments_json\":\"{}\",\"message\":\"done\"}",
					"annotations":[]
				}]
			}]
		}`)
	}))
	defer server.Close()

	client := openai.NewClient(
		option.WithUnsafeAllowHTTP(),
		option.WithBaseURL(server.URL),
		option.WithAPIKey("test-key"),
		option.WithMaxRetries(0),
	)
	reasoner, err := NewWithClient(
		Config{Model: "test-model"},
		sdkResponseClient{client: client},
	)
	if err != nil {
		t.Fatal(err)
	}
	decision, err := reasoner.Decide(context.Background(), agentloop.Turn{
		Run: testRun(), AgentMission: "Profile evidence.", Step: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if decision.Kind != agentloop.DecisionFinish || decision.Message != "done" {
		t.Fatalf("decision=%#v", decision)
	}

	wire := string(captured)
	for _, required := range []string{
		`"store":false`,
		`"type":"json_schema"`,
		`"strict":true`,
		"ai_native_runtime_decision",
	} {
		if !strings.Contains(wire, required) {
			t.Fatalf("wire request missing %q: %s", required, wire)
		}
	}
	for _, forbidden := range []string{
		"https://private-data-engine.example/mcp",
		strings.Repeat("a", 64),
		"run-secret-id",
		"conversation-secret-id",
	} {
		if strings.Contains(wire, forbidden) {
			t.Fatalf("wire request leaked %q: %s", forbidden, wire)
		}
	}
}

func TestNewRequiresExplicitModelAndAPIKey(t *testing.T) {
	if _, err := New(Config{APIKey: "x"}); err == nil {
		t.Fatal("missing model must fail")
	}
	if _, err := New(Config{Model: "test-model"}); err == nil {
		t.Fatal("missing API key must fail")
	}
}
