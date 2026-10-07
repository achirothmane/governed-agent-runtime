package agentserver

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"

	"github.com/achirothmane/governed-agent-runtime/mcptransport"
	runtimesdk "github.com/achirothmane/governed-agent-runtime/sdk"
)

type fakeDurableToolInvoker struct {
	mu             sync.Mutex
	calls          int
	runID          runtimesdk.RunID
	conversationID runtimesdk.ConversationID
	invocationID   string
	tool           runtimesdk.ToolDescriptor
	args           map[string]any
	result         mcptransport.Result
	err            error
}

func (f *fakeDurableToolInvoker) Execute(
	_ context.Context,
	runID runtimesdk.RunID,
	conversationID runtimesdk.ConversationID,
	invocationID string,
	tool runtimesdk.ToolDescriptor,
	args map[string]any,
) (mcptransport.Result, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	f.runID = runID
	f.conversationID = conversationID
	f.invocationID = invocationID
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

func TestDurableToolRouteRequiresInvocationIDAndPreservesEvidenceBoundary(t *testing.T) {
	service, store, _, _ := toolService(t, true)
	durable := &fakeDurableToolInvoker{
		result: mcptransport.Result{
			StructuredContent: json.RawMessage(`{"stage":"PRE_SEMANTIC_PROFILE","decision":"KNOWN"}`),
		},
	}
	service.durableInvoker = durable
	service.invoker = nil
	startToolRun(t, service)

	missing := request(
		t,
		service,
		"POST",
		"/v1/runs/r1/tools/data.profile",
		`{"arguments":{"rows":[{"id":"a"}]}}`,
		true,
	)
	if missing.Code != 400 || !strings.Contains(missing.Body.String(), "invocation_id_required") {
		t.Fatalf("missing invocation id status=%d body=%s", missing.Code, missing.Body.String())
	}

	rr := request(
		t,
		service,
		"POST",
		"/v1/runs/r1/tools/data.profile",
		`{"invocation_id":"profile-001","arguments":{"rows":[{"id":"a","secret":"raw-secret"}]}}`,
		true,
	)
	if rr.Code != 200 {
		t.Fatalf("durable tool status=%d body=%s", rr.Code, rr.Body.String())
	}

	durable.mu.Lock()
	calls := durable.calls
	runID := durable.runID
	conversationID := durable.conversationID
	invocationID := durable.invocationID
	durable.mu.Unlock()
	if calls != 1 || runID != "r1" || conversationID != "c1" || invocationID != "profile-001" {
		t.Fatalf("durable call count=%d run=%s conversation=%s invocation=%s", calls, runID, conversationID, invocationID)
	}

	events, err := store.ListEvents(context.Background(), "c1", 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 3 {
		t.Fatalf("events=%#v", events)
	}
	called := string(events[1].Payload)
	returned := string(events[2].Payload)
	if !strings.Contains(called, `"invocation_id":"profile-001"`) ||
		!strings.Contains(returned, `"invocation_id":"profile-001"`) {
		t.Fatalf("invocation identity missing: called=%s returned=%s", called, returned)
	}
	if strings.Contains(called, "raw-secret") || strings.Contains(returned, "raw-secret") {
		t.Fatalf("event evidence leaked raw payload: called=%s returned=%s", called, returned)
	}
}
