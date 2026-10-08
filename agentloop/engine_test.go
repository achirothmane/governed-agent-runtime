package agentloop

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/achirothmane/governed-agent-runtime/mcptransport"
	runtimesdk "github.com/achirothmane/governed-agent-runtime/sdk"
)

type scriptedReasoner struct {
	decisions []Decision
	turns     []Turn
}

func (r *scriptedReasoner) Decide(_ context.Context, turn Turn) (Decision, error) {
	r.turns = append(r.turns, turn)
	if len(r.decisions) == 0 {
		return Decision{}, errors.New("no scripted decision")
	}
	d := r.decisions[0]
	r.decisions = r.decisions[1:]
	return d, nil
}

type recordingExecutor struct {
	calls []struct {
		runID        runtimesdk.RunID
		conversation runtimesdk.ConversationID
		invocationID string
		tool         runtimesdk.ToolDescriptor
		args         map[string]any
	}
	result mcptransport.Result
	err    error
}

func (e *recordingExecutor) Execute(
	_ context.Context,
	runID runtimesdk.RunID,
	conversationID runtimesdk.ConversationID,
	invocationID string,
	tool runtimesdk.ToolDescriptor,
	args map[string]any,
) (mcptransport.Result, error) {
	e.calls = append(e.calls, struct {
		runID        runtimesdk.RunID
		conversation runtimesdk.ConversationID
		invocationID string
		tool         runtimesdk.ToolDescriptor
		args         map[string]any
	}{runID, conversationID, invocationID, tool, args})
	if e.err != nil {
		return mcptransport.Result{}, e.err
	}
	result := e.result
	if result.Tool == "" {
		result.Tool = tool.Name
	}
	if result.SnapshotDigest == "" {
		result.SnapshotDigest = tool.SnapshotDigest
	}
	return result, nil
}

func boundRun(readOnly bool) runtimesdk.RunRequest {
	return runtimesdk.RunRequest{
		ID: "run-1",
		Conversation: runtimesdk.ConversationRef{
			ID:          "conv-1",
			AgentID:     "data-profiler",
			WorkspaceID: "ws-1",
		},
		Workspace: runtimesdk.WorkspaceSpec{ID: "ws-1", Kind: runtimesdk.WorkspaceRemote},
		Input:     "inspect these rows and explain whether they conflict",
		Tools: []runtimesdk.ToolDescriptor{{
			Name:           "data.profile",
			Protocol:       runtimesdk.ToolProtocolMCP,
			Endpoint:       "http://data-engine.test/mcp",
			ReadOnly:       readOnly,
			SnapshotDigest: strings.Repeat("a", 64),
		}},
	}
}

func TestEngineToolObservationThenFinish(t *testing.T) {
	reasoner := &scriptedReasoner{decisions: []Decision{
		{
			Kind: DecisionTool,
			Tool: "data.profile",
			Arguments: map[string]any{
				"identity_field": "id",
				"rows": []any{
					map[string]any{"id": "a", "price": 20.0},
					map[string]any{"id": "a", "price": 21.0},
				},
			},
		},
		{Kind: DecisionFinish, Message: "The profile reports a raw conflict for identity a."},
	}}
	executor := &recordingExecutor{result: mcptransport.Result{
		StructuredContent: json.RawMessage(`{"stage":"PRE_SEMANTIC_PROFILE","decision":"CONFLICTING"}`),
	}}
	engine := Engine{Reasoner: reasoner, Tools: executor, MaxSteps: 4}

	outcome, err := engine.Run(context.Background(), boundRun(true), "Use the Data Engine to inspect evidence before answering.")
	if err != nil {
		t.Fatal(err)
	}
	if outcome.Kind != OutcomeFinished || outcome.Steps != 2 {
		t.Fatalf("outcome=%#v", outcome)
	}
	if len(executor.calls) != 1 || len(outcome.Observations) != 1 {
		t.Fatalf("calls=%d observations=%d", len(executor.calls), len(outcome.Observations))
	}
	call := executor.calls[0]
	if call.tool.Name != "data.profile" || !call.tool.ReadOnly {
		t.Fatalf("tool=%#v", call.tool)
	}
	if !strings.HasPrefix(call.invocationID, "a6-01-") {
		t.Fatalf("invocation id=%q", call.invocationID)
	}
	if len(reasoner.turns) != 2 || len(reasoner.turns[1].Observations) != 1 {
		t.Fatalf("turns=%#v", reasoner.turns)
	}
	if string(reasoner.turns[1].Observations[0].Result.StructuredContent) == "" {
		t.Fatal("second reasoning turn did not receive tool observation")
	}
}

func TestEngineRejectsUnboundToolBeforeExecution(t *testing.T) {
	reasoner := &scriptedReasoner{decisions: []Decision{{
		Kind: DecisionTool,
		Tool: "data.delete",
		Arguments: map[string]any{},
	}}}
	executor := &recordingExecutor{}
	_, err := (Engine{Reasoner: reasoner, Tools: executor}).Run(context.Background(), boundRun(true), "Inspect only.")
	if !errors.Is(err, ErrUnboundTool) {
		t.Fatalf("error=%v", err)
	}
	if len(executor.calls) != 0 {
		t.Fatalf("executor calls=%d", len(executor.calls))
	}
}

func TestEngineRejectsMutatingToolBeforeExecution(t *testing.T) {
	reasoner := &scriptedReasoner{decisions: []Decision{{
		Kind: DecisionTool,
		Tool: "data.profile",
		Arguments: map[string]any{},
	}}}
	executor := &recordingExecutor{}
	_, err := (Engine{Reasoner: reasoner, Tools: executor}).Run(context.Background(), boundRun(false), "Inspect only.")
	if !errors.Is(err, ErrMutatingTool) {
		t.Fatalf("error=%v", err)
	}
	if len(executor.calls) != 0 {
		t.Fatalf("executor calls=%d", len(executor.calls))
	}
}

func TestEngineAskStopsWithoutTool(t *testing.T) {
	reasoner := &scriptedReasoner{decisions: []Decision{{
		Kind:    DecisionAsk,
		Message: "Which dataset should I inspect?",
	}}}
	executor := &recordingExecutor{}
	outcome, err := (Engine{Reasoner: reasoner, Tools: executor}).Run(context.Background(), boundRun(true), "Inspect data.")
	if err != nil {
		t.Fatal(err)
	}
	if outcome.Kind != OutcomeWaitingInput || outcome.Message == "" || len(executor.calls) != 0 {
		t.Fatalf("outcome=%#v calls=%d", outcome, len(executor.calls))
	}
}

func TestEngineStopsAtMaxSteps(t *testing.T) {
	reasoner := &scriptedReasoner{decisions: []Decision{
		{Kind: DecisionTool, Tool: "data.profile", Arguments: map[string]any{"rows": []any{}}},
		{Kind: DecisionTool, Tool: "data.profile", Arguments: map[string]any{"rows": []any{}}},
	}}
	executor := &recordingExecutor{}
	outcome, err := (Engine{Reasoner: reasoner, Tools: executor, MaxSteps: 2}).Run(context.Background(), boundRun(true), "Inspect data.")
	if err != nil {
		t.Fatal(err)
	}
	if outcome.Kind != OutcomeMaxSteps || outcome.Steps != 2 || len(executor.calls) != 2 {
		t.Fatalf("outcome=%#v calls=%d", outcome, len(executor.calls))
	}
}

func TestInvocationIDStableForSameBoundDecision(t *testing.T) {
	decision := Decision{Kind: DecisionTool, Tool: "data.profile", Arguments: map[string]any{"b": 2, "a": 1}}
	a, err := invocationID("run-1", 3, decision)
	if err != nil {
		t.Fatal(err)
	}
	b, err := invocationID("run-1", 3, Decision{Kind: DecisionTool, Tool: "data.profile", Arguments: map[string]any{"a": 1, "b": 2}})
	if err != nil {
		t.Fatal(err)
	}
	if a != b {
		t.Fatalf("stable decision produced different invocation IDs: %q != %q", a, b)
	}
}
