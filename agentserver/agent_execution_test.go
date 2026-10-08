package agentserver

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/achirothmane/governed-agent-runtime/agentloop"
	runtimesdk "github.com/achirothmane/governed-agent-runtime/sdk"
)

type fakeAgentRunner struct {
	calls   int
	run     runtimesdk.RunRequest
	mission string
	outcome agentloop.Outcome
	err     error
}

func (r *fakeAgentRunner) Run(_ context.Context, run runtimesdk.RunRequest, mission string) (agentloop.Outcome, error) {
	r.calls++
	r.run = run
	r.mission = mission
	if r.err != nil {
		return agentloop.Outcome{}, r.err
	}
	return r.outcome, nil
}

func agentExecutionService(t *testing.T, runner AgentRunner) (*Service, *MemoryStore, map[runtimesdk.AgentID]runtimesdk.Runtime) {
	t.Helper()
	backend := newFakeBackend()
	catalog, err := runtimesdk.NewToolCatalog([]runtimesdk.ToolDescriptor{{
		Name:           "data.profile",
		Protocol:       runtimesdk.ToolProtocolMCP,
		Endpoint:       "http://data-engine.test/mcp",
		ReadOnly:       true,
		SnapshotDigest: strings.Repeat("a", 64),
	}})
	if err != nil {
		t.Fatal(err)
	}
	runtime := runtimesdk.Runtime{
		Agent: runtimesdk.AgentSpec{
			ID:            "data-profiler",
			Mission:       "Inspect raw evidence before answering.",
			RequiredTools: []runtimesdk.ToolName{"data.profile"},
			Workspace:     runtimesdk.WorkspaceSpec{ID: "data-engine", Kind: runtimesdk.WorkspaceRemote},
		},
		Tools:   catalog,
		Backend: backend,
	}
	runtimes := map[runtimesdk.AgentID]runtimesdk.Runtime{"data-profiler": runtime}
	store := NewMemoryStore()
	now := time.Date(2026, 10, 8, 2, 0, 0, 0, time.UTC)
	service, err := New(Config{
		Provider:    StaticProvider{Runtimes: runtimes},
		AgentRunner: runner,
		Store:       store,
		BearerToken: "secret",
		Clock: func() time.Time {
			now = now.Add(time.Millisecond)
			return now
		},
		HeartbeatInterval: time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	return service, store, runtimes
}

func startAgentRun(t *testing.T, service *Service) {
	t.Helper()
	if rr := request(t, service, "POST", "/v1/conversations", `{"id":"c1","agent_id":"data-profiler"}`, true); rr.Code != 201 {
		t.Fatalf("create conversation status=%d body=%s", rr.Code, rr.Body.String())
	}
	if rr := request(t, service, "POST", "/v1/conversations/c1/runs", `{"id":"r1","input":"inspect rows"}`, true); rr.Code != 201 {
		t.Fatalf("start run status=%d body=%s", rr.Code, rr.Body.String())
	}
}

func TestAgentExecuteUsesExactBoundRunAndPersistsTerminalEvidence(t *testing.T) {
	runner := &fakeAgentRunner{outcome: agentloop.Outcome{
		Kind:    agentloop.OutcomeFinished,
		Message: "The raw rows conflict.",
		Steps:   2,
		Observations: []agentloop.Observation{{
			Step:         1,
			InvocationID: "a6-01-abc",
			Tool:         "data.profile",
		}},
	}}
	service, store, _ := agentExecutionService(t, runner)
	startAgentRun(t, service)

	rr := request(t, service, "POST", "/v1/runs/r1/execute", "", true)
	if rr.Code != 200 {
		t.Fatalf("execute status=%d body=%s", rr.Code, rr.Body.String())
	}
	if runner.calls != 1 || runner.run.ID != "r1" || runner.run.Input != "inspect rows" {
		t.Fatalf("runner calls=%d run=%#v", runner.calls, runner.run)
	}
	if runner.mission != "Inspect raw evidence before answering." {
		t.Fatalf("mission=%q", runner.mission)
	}
	if len(runner.run.Tools) != 1 || runner.run.Tools[0].SnapshotDigest != strings.Repeat("a", 64) {
		t.Fatalf("runner tool binding=%#v", runner.run.Tools)
	}

	events, err := store.ListEvents(context.Background(), "c1", 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 || events[0].Type != runtimesdk.EventRunStarted || events[1].Type != runtimesdk.EventRunCompleted {
		t.Fatalf("events=%#v", events)
	}
	if strings.Contains(string(events[1].Payload), "PRE_SEMANTIC_PROFILE") {
		t.Fatalf("terminal evidence leaked observation payload: %s", events[1].Payload)
	}
	if !strings.Contains(string(events[1].Payload), `"observation_count":1`) {
		t.Fatalf("terminal evidence=%s", events[1].Payload)
	}
}

func TestAgentExecuteRejectsCapabilityChangeBeforeReasoning(t *testing.T) {
	runner := &fakeAgentRunner{outcome: agentloop.Outcome{Kind: agentloop.OutcomeFinished, Message: "done", Steps: 1}}
	service, _, runtimes := agentExecutionService(t, runner)
	startAgentRun(t, service)

	current := runtimes["data-profiler"]
	changed, err := runtimesdk.NewToolCatalog([]runtimesdk.ToolDescriptor{{
		Name:           "data.profile",
		Protocol:       runtimesdk.ToolProtocolMCP,
		Endpoint:       "http://data-engine.test/mcp",
		ReadOnly:       true,
		SnapshotDigest: strings.Repeat("b", 64),
	}})
	if err != nil {
		t.Fatal(err)
	}
	current.Tools = changed
	runtimes["data-profiler"] = current

	rr := request(t, service, "POST", "/v1/runs/r1/execute", "", true)
	if rr.Code != 409 {
		t.Fatalf("execute status=%d body=%s", rr.Code, rr.Body.String())
	}
	if runner.calls != 0 {
		t.Fatalf("runner calls=%d want 0", runner.calls)
	}
}

func TestAgentAskProducesWaitingEvent(t *testing.T) {
	runner := &fakeAgentRunner{outcome: agentloop.Outcome{
		Kind:    agentloop.OutcomeWaitingInput,
		Message: "Which dataset should I inspect?",
		Steps:   1,
	}}
	service, store, _ := agentExecutionService(t, runner)
	startAgentRun(t, service)

	rr := request(t, service, "POST", "/v1/runs/r1/execute", "", true)
	if rr.Code != 200 {
		t.Fatalf("execute status=%d body=%s", rr.Code, rr.Body.String())
	}
	events, err := store.ListEvents(context.Background(), "c1", 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	if events[len(events)-1].Type != runtimesdk.EventRunWaiting {
		t.Fatalf("events=%#v", events)
	}
}
