package agentserver

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/achirothmane/governed-agent-runtime/mcptransport"
	runtimesdk "github.com/achirothmane/governed-agent-runtime/sdk"
)

type fakeToolInvoker struct {
	mu     sync.Mutex
	calls  int
	tool   runtimesdk.ToolDescriptor
	args   map[string]any
	result mcptransport.Result
	err    error
}

func (f *fakeToolInvoker) Invoke(_ context.Context, tool runtimesdk.ToolDescriptor, args map[string]any) (mcptransport.Result, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	f.tool = tool
	f.args = args
	if f.err != nil {
		return mcptransport.Result{}, f.err
	}
	result := f.result
	if result.Tool == "" {
		result.Tool = tool.Name
	}
	if result.SnapshotDigest == "" {
		result.SnapshotDigest = tool.SnapshotDigest
	}
	return result, nil
}

func toolService(t *testing.T, readOnly bool) (*Service, *MemoryStore, *fakeToolInvoker, map[runtimesdk.AgentID]runtimesdk.Runtime) {
	t.Helper()
	backend := newFakeBackend()
	digest := strings.Repeat("a", 64)
	catalog, err := runtimesdk.NewToolCatalog([]runtimesdk.ToolDescriptor{{
		Name:           "data.profile",
		Protocol:       runtimesdk.ToolProtocolMCP,
		Endpoint:       "http://data-engine.test/mcp",
		ReadOnly:       readOnly,
		SnapshotDigest: digest,
	}})
	if err != nil {
		t.Fatal(err)
	}
	runtime := runtimesdk.Runtime{
		Agent: runtimesdk.AgentSpec{
			ID:            "data-profiler",
			Mission:       "Profile data without changing source values.",
			RequiredTools: []runtimesdk.ToolName{"data.profile"},
			Workspace:     runtimesdk.WorkspaceSpec{ID: "data-engine", Kind: runtimesdk.WorkspaceRemote},
		},
		Tools:   catalog,
		Backend: backend,
	}
	runtimes := map[runtimesdk.AgentID]runtimesdk.Runtime{"data-profiler": runtime}
	invoker := &fakeToolInvoker{
		result: mcptransport.Result{
			StructuredContent: json.RawMessage(`{"stage":"PRE_SEMANTIC_PROFILE","decision":"KNOWN"}`),
		},
	}
	store := NewMemoryStore()
	now := time.Date(2026, 10, 7, 21, 0, 0, 0, time.UTC)
	service, err := New(Config{
		Provider:    StaticProvider{Runtimes: runtimes},
		Invoker:     invoker,
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
	return service, store, invoker, runtimes
}

func startToolRun(t *testing.T, service *Service) {
	t.Helper()
	if rr := request(t, service, "POST", "/v1/conversations", `{"id":"c1","agent_id":"data-profiler"}`, true); rr.Code != 201 {
		t.Fatalf("create conversation status=%d body=%s", rr.Code, rr.Body.String())
	}
	if rr := request(t, service, "POST", "/v1/conversations/c1/runs", `{"id":"r1","input":"profile supplied rows"}`, true); rr.Code != 201 {
		t.Fatalf("start run status=%d body=%s", rr.Code, rr.Body.String())
	}
}

func TestReadOnlyToolInvocationPreservesBoundSnapshotAndEvidenceDigests(t *testing.T) {
	service, store, invoker, _ := toolService(t, true)
	startToolRun(t, service)

	rr := request(t, service, "POST", "/v1/runs/r1/tools/data.profile", `{"arguments":{"rows":[{"id":"a","secret":"sensitive-value"}]}}`, true)
	if rr.Code != 200 {
		t.Fatalf("tool status=%d body=%s", rr.Code, rr.Body.String())
	}
	var response toolInvokeResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if !response.EventPersisted || response.Result.Tool != "data.profile" {
		t.Fatalf("response=%#v", response)
	}

	invoker.mu.Lock()
	calls := invoker.calls
	tool := invoker.tool
	invoker.mu.Unlock()
	if calls != 1 {
		t.Fatalf("invoker calls=%d want 1", calls)
	}
	if !tool.ReadOnly || tool.SnapshotDigest != strings.Repeat("a", 64) {
		t.Fatalf("invoked tool=%#v", tool)
	}

	events, err := store.ListEvents(context.Background(), "c1", 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 3 {
		t.Fatalf("events=%#v", events)
	}
	if events[0].Type != runtimesdk.EventRunStarted ||
		events[1].Type != runtimesdk.EventToolCalled ||
		events[2].Type != runtimesdk.EventToolReturned {
		t.Fatalf("event types=%s,%s,%s", events[0].Type, events[1].Type, events[2].Type)
	}
	if strings.Contains(string(events[1].Payload), "sensitive-value") {
		t.Fatalf("tool.called event leaked raw arguments: %s", events[1].Payload)
	}
	if !strings.Contains(string(events[1].Payload), "arguments_digest") {
		t.Fatalf("tool.called event lacks argument digest: %s", events[1].Payload)
	}
	if strings.Contains(string(events[2].Payload), "PRE_SEMANTIC_PROFILE") {
		t.Fatalf("tool.returned event leaked raw result: %s", events[2].Payload)
	}
	if !strings.Contains(string(events[2].Payload), "result_digest") {
		t.Fatalf("tool.returned event lacks result digest: %s", events[2].Payload)
	}
}

func TestToolInvocationRejectsCapabilityChangeBeforeArgumentsLeaveServer(t *testing.T) {
	service, _, invoker, runtimes := toolService(t, true)
	startToolRun(t, service)

	current := runtimes["data-profiler"]
	changedCatalog, err := runtimesdk.NewToolCatalog([]runtimesdk.ToolDescriptor{{
		Name:           "data.profile",
		Protocol:       runtimesdk.ToolProtocolMCP,
		Endpoint:       "http://data-engine.test/mcp",
		ReadOnly:       true,
		SnapshotDigest: strings.Repeat("b", 64),
	}})
	if err != nil {
		t.Fatal(err)
	}
	current.Tools = changedCatalog
	runtimes["data-profiler"] = current

	rr := request(t, service, "POST", "/v1/runs/r1/tools/data.profile", `{"arguments":{"rows":[{"secret":"must-not-leave"}]}}`, true)
	if rr.Code != 409 {
		t.Fatalf("tool status=%d body=%s", rr.Code, rr.Body.String())
	}
	invoker.mu.Lock()
	calls := invoker.calls
	invoker.mu.Unlock()
	if calls != 0 {
		t.Fatalf("invoker calls=%d want 0", calls)
	}
}

func TestToolInvocationRejectsNonReadOnlyCapability(t *testing.T) {
	service, store, invoker, _ := toolService(t, false)
	startToolRun(t, service)

	rr := request(t, service, "POST", "/v1/runs/r1/tools/data.profile", `{"arguments":{"rows":[{"id":"a"}]}}`, true)
	if rr.Code != 403 {
		t.Fatalf("tool status=%d body=%s", rr.Code, rr.Body.String())
	}
	invoker.mu.Lock()
	calls := invoker.calls
	invoker.mu.Unlock()
	if calls != 0 {
		t.Fatalf("invoker calls=%d want 0", calls)
	}
	events, err := store.ListEvents(context.Background(), "c1", 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].Type != runtimesdk.EventRunStarted {
		t.Fatalf("unexpected events=%#v", events)
	}
}
