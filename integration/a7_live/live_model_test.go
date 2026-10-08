//go:build integration

// A7 live-inference smoke test. It is intentionally separate from the A7
// deterministic, real-Temporal/Data-Engine protocol proof.
package a7live

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	openai "github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
	"github.com/openai/openai-go/v3/responses"

	"github.com/achirothmane/governed-agent-runtime/agentloop"
	"github.com/achirothmane/governed-agent-runtime/mcptransport"
	openairesponses "github.com/achirothmane/governed-agent-runtime/reasoners/openairesponses"
	runtimesdk "github.com/achirothmane/governed-agent-runtime/sdk"
)

type localResponsesClient struct{ client openai.Client }

func (c localResponsesClient) Create(ctx context.Context, params responses.ResponseNewParams) (string, error) {
	res, err := c.client.Responses.New(ctx, params)
	if err != nil {
		return "", err
	}
	if res == nil {
		return "", fmt.Errorf("model returned nil response")
	}
	return res.OutputText(), nil
}

func TestActualLocalModelProposesProfileThenFinishes(t *testing.T) {
	baseURL := strings.TrimSpace(os.Getenv("A7_LIVE_MODEL_BASE_URL"))
	model := strings.TrimSpace(os.Getenv("A7_LIVE_MODEL"))
	if baseURL == "" || model == "" {
		t.Skip("set A7_LIVE_MODEL_BASE_URL and A7_LIVE_MODEL to enable real inference")
	}
	// This flag is explicit: it enables plaintext connections to the GitHub
	// runner's local Ollama instance; never use this for a remote provider.
	if !strings.HasPrefix(baseURL, "http://127.0.0.1:") &&
		!strings.HasPrefix(baseURL, "http://localhost:") {
		t.Fatal("A7 smoke only accepts loopback local inference")
	}

	client := openai.NewClient(
		option.WithAPIKey("local-ollama-no-remote-secret"),
		option.WithUnsafeAllowHTTP(),
		option.WithBaseURL(baseURL),
		option.WithMaxRetries(0),
	)
	reasoner, err := openairesponses.NewWithClient(
		openairesponses.Config{Model: model, MaxOutputTokens: 768},
		localResponsesClient{client: client},
	)
	if err != nil {
		t.Fatal(err)
	}
	schema := json.RawMessage(`{"type":"object","additionalProperties":false,"properties":{"rows":{"type":"array","items":{"type":"object"}},"identity_field":{"type":"string"}},"required":["rows","identity_field"]}`)
	run := runtimesdk.RunRequest{
		ID: "local-model-smoke",
		Conversation: runtimesdk.ConversationRef{
			ID: "local-conversation", AgentID: "profile-agent", WorkspaceID: "data",
		},
		Workspace: runtimesdk.WorkspaceSpec{ID: "data", Kind: runtimesdk.WorkspaceRemote},
		Input: `Call data.profile with identity_field="id" and rows=[{"id":"a","price":20},{"id":"a","price":21}]. After the profile returns, tell me whether the raw values conflict. Do not FINISH before calling the tool.`,
		Tools: []runtimesdk.ToolDescriptor{{
			Name: "data.profile", Protocol: runtimesdk.ToolProtocolMCP,
			Endpoint: "http://127.0.0.1:8090/mcp",
			Title: "Profile raw data", Description: "Analyze raw rows and detect conflicting values for identical identities; read-only.",
			InputSchema: schema, ReadOnly: true, SnapshotDigest: strings.Repeat("a", 64),
		}},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	start := time.Now()
	first, err := reasoner.Decide(ctx, agentloop.Turn{
		Run: run, AgentMission: "Use data.profile to inspect raw evidence before answering; never guess results.", Step: 1,
	})
	t.Logf("live provider=%s first-step latency=%s kind=%s", model, time.Since(start).Round(time.Millisecond), first.Kind)
	if err != nil {
		t.Fatalf("real-model step 1 failed: %v", err)
	}
	if first.Kind != agentloop.DecisionTool || first.Tool != "data.profile" {
		t.Fatalf("model did not select bound read-only profile tool: kind=%s tool=%q", first.Kind, first.Tool)
	}
	if first.Arguments["identity_field"] != "id" {
		t.Fatalf("model selected wrong identity field: %v", first.Arguments["identity_field"])
	}
	rows, ok := first.Arguments["rows"].([]any)
	if !ok || len(rows) != 2 {
		t.Fatalf("model did not supply exactly two rows: %#v", first.Arguments["rows"])
	}
	// Isolated model decision test: synthetic observation intentionally replaces
	// the real MCP call. The separate A7 private contract test invokes real MCP.
	observation := agentloop.Observation{
		Step: 1, InvocationID: "local-inference-smoke-observation", Tool: "data.profile",
		Result: mcptransport.Result{
			Tool: "data.profile", SnapshotDigest: strings.Repeat("a", 64),
			StructuredContent: json.RawMessage(`{"stage":"PRE_SEMANTIC_PROFILE","decision":"CONFLICTING","reason":"same identity a has price 20 and price 21"}`),
		},
	}
	start = time.Now()
	second, err := reasoner.Decide(ctx, agentloop.Turn{
		Run: run, AgentMission: "Use data.profile to inspect raw evidence before answering; never guess results.", Step: 2,
		Observations: []agentloop.Observation{observation},
	})
	t.Logf("live provider=%s second-step latency=%s kind=%s", model, time.Since(start).Round(time.Millisecond), second.Kind)
	if err != nil {
		t.Fatalf("real-model step 2 failed: %v", err)
	}
	if second.Kind != agentloop.DecisionFinish || !strings.Contains(strings.ToLower(second.Message), "conflict") {
		t.Fatalf("model did not accurately finish from an observation: kind=%s message=%q", second.Kind, second.Message)
	}
}
