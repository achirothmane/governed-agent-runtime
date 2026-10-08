package temporalagent

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/achirothmane/governed-agent-runtime/agentloop"
	"github.com/achirothmane/governed-agent-runtime/mcptransport"
	runtimesdk "github.com/achirothmane/governed-agent-runtime/sdk"
	"github.com/achirothmane/governed-agent-runtime/temporaltools"
)

type memoryExecutionStore struct {
	mu      sync.Mutex
	records map[runtimesdk.RunID]ExecutionRef
}

func newMemoryExecutionStore() *memoryExecutionStore {
	return &memoryExecutionStore{records: map[runtimesdk.RunID]ExecutionRef{}}
}

func (s *memoryExecutionStore) PrepareExecution(_ context.Context, ref ExecutionRef) (ExecutionRef, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if existing, ok := s.records[ref.RunID]; ok {
		if existing != ref {
			return ExecutionRef{}, false, ErrExecutionConflict
		}
		return existing, false, nil
	}
	s.records[ref.RunID] = ref
	return ref, true, nil
}

func (s *memoryExecutionStore) GetExecution(_ context.Context, runID runtimesdk.RunID) (ExecutionRef, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ref, ok := s.records[runID]
	if !ok {
		return ExecutionRef{}, ErrExecutionNotFound
	}
	return ref, nil
}

type memoryStepStore struct {
	mu      sync.Mutex
	records map[string]DecisionRecord
}

func newMemoryStepStore() *memoryStepStore {
	return &memoryStepStore{records: map[string]DecisionRecord{}}
}

func stepKey(runID runtimesdk.RunID, step int) string {
	return string(runID) + "/" + string(rune(step))
}

func (s *memoryStepStore) PutDecision(_ context.Context, record DecisionRecord) (DecisionRecord, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := stepKey(record.RunID, record.Step)
	if existing, ok := s.records[key]; ok {
		if existing.DecisionDigest != record.DecisionDigest {
			return DecisionRecord{}, false, ErrDecisionConflict
		}
		return existing, false, nil
	}
	s.records[key] = record
	return record, true, nil
}

func (s *memoryStepStore) GetDecision(_ context.Context, runID runtimesdk.RunID, step int) (DecisionRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	record, ok := s.records[stepKey(runID, step)]
	if !ok {
		return DecisionRecord{}, ErrDecisionNotFound
	}
	return record, nil
}

type memoryInvocationStore struct {
	mu      sync.Mutex
	records map[string]temporaltools.Invocation
}

func newMemoryInvocationStore() *memoryInvocationStore {
	return &memoryInvocationStore{records: map[string]temporaltools.Invocation{}}
}

func (s *memoryInvocationStore) Prepare(_ context.Context, ref temporaltools.InvocationRef, args map[string]any) (temporaltools.Invocation, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if existing, ok := s.records[ref.InvocationID]; ok {
		if !temporaltools.SameBinding(existing.Ref, ref) {
			return temporaltools.Invocation{}, false, temporaltools.ErrInvocationConflict
		}
		return existing, false, nil
	}
	record := temporaltools.Invocation{Ref: ref, Arguments: args, State: temporaltools.InvocationPrepared}
	s.records[ref.InvocationID] = record
	return record, true, nil
}

func (s *memoryInvocationStore) Get(_ context.Context, id string) (temporaltools.Invocation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	record, ok := s.records[id]
	if !ok {
		return temporaltools.Invocation{}, temporaltools.ErrInvocationNotFound
	}
	return record, nil
}

func (s *memoryInvocationStore) Complete(_ context.Context, id string, result mcptransport.Result, digest string) (temporaltools.Invocation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	record, ok := s.records[id]
	if !ok {
		return temporaltools.Invocation{}, temporaltools.ErrInvocationNotFound
	}
	copyResult := result
	record.Result = &copyResult
	record.ResultDigest = digest
	record.State = temporaltools.InvocationComplete
	record.LastError = ""
	s.records[id] = record
	return record, nil
}

func (s *memoryInvocationStore) Fail(_ context.Context, id, message string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	record, ok := s.records[id]
	if !ok {
		return temporaltools.ErrInvocationNotFound
	}
	if record.State != temporaltools.InvocationComplete {
		record.State = temporaltools.InvocationFailed
		record.LastError = message
		s.records[id] = record
	}
	return nil
}

type staticResolver struct {
	run     runtimesdk.RunRequest
	mission string
}

func (r *staticResolver) Resolve(context.Context, runtimesdk.RunID) (runtimesdk.RunRequest, string, error) {
	return r.run, r.mission, nil
}

type countingReasoner struct {
	mu       sync.Mutex
	calls    int
	decision agentloop.Decision
}

func (r *countingReasoner) Decide(_ context.Context, _ agentloop.Turn) (agentloop.Decision, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls++
	return r.decision, nil
}

func durableRun() runtimesdk.RunRequest {
	return runtimesdk.RunRequest{
		ID: "run-1",
		Conversation: runtimesdk.ConversationRef{
			ID:          "conv-1",
			AgentID:     "data-profiler",
			WorkspaceID: "ws-1",
		},
		Workspace: runtimesdk.WorkspaceSpec{ID: "ws-1", Kind: runtimesdk.WorkspaceRemote},
		Input:     "inspect rows",
		Tools: []runtimesdk.ToolDescriptor{{
			Name:           "data.profile",
			Protocol:       runtimesdk.ToolProtocolMCP,
			Endpoint:       "http://data-engine.test/mcp",
			ReadOnly:       true,
			SnapshotDigest: strings.Repeat("a", 64),
		}},
	}
}

func TestReasonActivityCommitsToolDecisionAndReusesIt(t *testing.T) {
	ctx := context.Background()
	run := durableRun()
	ref, err := executionRef(run, "Inspect evidence.", 8)
	if err != nil {
		t.Fatal(err)
	}
	executions := newMemoryExecutionStore()
	if _, _, err := executions.PrepareExecution(ctx, ref); err != nil {
		t.Fatal(err)
	}
	steps := newMemoryStepStore()
	invocations := newMemoryInvocationStore()
	reasoner := &countingReasoner{decision: agentloop.Decision{
		Kind: agentloop.DecisionTool,
		Tool: "data.profile",
		Arguments: map[string]any{
			"rows": []any{map[string]any{"id": "a", "secret": "outside-history"}},
		},
	}}
	activity := ReasonActivity{
		Resolver:    &staticResolver{run: run, mission: "Inspect evidence."},
		Reasoner:    reasoner,
		Executions:  executions,
		Steps:       steps,
		Invocations: invocations,
	}

	first, err := activity.Execute(ctx, ReasoningRequest{Execution: ref, Step: 1})
	if err != nil {
		t.Fatal(err)
	}
	second, err := activity.Execute(ctx, ReasoningRequest{Execution: ref, Step: 1})
	if err != nil {
		t.Fatal(err)
	}
	reasoner.mu.Lock()
	calls := reasoner.calls
	reasoner.mu.Unlock()
	if calls != 1 {
		t.Fatalf("reasoner calls=%d want 1", calls)
	}
	if first.DecisionDigest == "" || first.DecisionDigest != second.DecisionDigest ||
		first.Invocation == nil || second.Invocation == nil ||
		first.Invocation.InvocationID != second.Invocation.InvocationID {
		t.Fatalf("first=%#v second=%#v", first, second)
	}
	payload, err := json.Marshal(first)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(payload), "outside-history") {
		t.Fatalf("compact reasoning result leaked tool arguments: %s", payload)
	}
	stored, err := invocations.Get(ctx, first.Invocation.InvocationID)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(stored.Arguments)
	if !strings.Contains(string(raw), "outside-history") {
		t.Fatalf("invocation ledger did not retain raw arguments: %s", raw)
	}
}

func TestReasonActivityRejectsMissionDriftBeforeReusingCommittedDecision(t *testing.T) {
	ctx := context.Background()
	run := durableRun()
	ref, err := executionRef(run, "original mission", 8)
	if err != nil {
		t.Fatal(err)
	}
	executions := newMemoryExecutionStore()
	_, _, _ = executions.PrepareExecution(ctx, ref)
	steps := newMemoryStepStore()
	decision := agentloop.Decision{Kind: agentloop.DecisionFinish, Message: "done"}
	record, err := newDecisionRecord(run.ID, 1, decision, "")
	if err != nil {
		t.Fatal(err)
	}
	_, _, _ = steps.PutDecision(ctx, record)
	activity := ReasonActivity{
		Resolver:    &staticResolver{run: run, mission: "changed mission"},
		Reasoner:    &countingReasoner{decision: decision},
		Executions:  executions,
		Steps:       steps,
		Invocations: newMemoryInvocationStore(),
	}
	if _, err := activity.Execute(ctx, ReasoningRequest{Execution: ref, Step: 1}); err == nil {
		t.Fatal("mission drift must fail before decision reuse")
	}
}

type completingExecutor struct {
	store  *memoryInvocationStore
	result mcptransport.Result
	calls  int
}

func (e *completingExecutor) Execute(
	ctx context.Context,
	runID runtimesdk.RunID,
	conversationID runtimesdk.ConversationID,
	invocationID string,
	tool runtimesdk.ToolDescriptor,
	args map[string]any,
) (mcptransport.Result, error) {
	e.calls++
	record, err := e.store.Get(ctx, invocationID)
	if err != nil {
		return mcptransport.Result{}, err
	}
	if record.Ref.RunID != runID || record.Ref.ConversationID != conversationID || record.Ref.Tool.Name != tool.Name {
		return mcptransport.Result{}, temporaltools.ErrInvocationConflict
	}
	digest, err := temporaltools.ResultDigest(e.result)
	if err != nil {
		return mcptransport.Result{}, err
	}
	if _, err := e.store.Complete(ctx, invocationID, e.result, digest); err != nil {
		return mcptransport.Result{}, err
	}
	return e.result, nil
}

func TestToolDispatchReturnsCompactObservationReference(t *testing.T) {
	ctx := context.Background()
	run := durableRun()
	decision := agentloop.Decision{
		Kind:      agentloop.DecisionTool,
		Tool:      "data.profile",
		Arguments: map[string]any{"rows": []any{map[string]any{"secret": "raw-observation"}}},
	}
	ref, args, err := toolRef(run, 1, decision)
	if err != nil {
		t.Fatal(err)
	}
	store := newMemoryInvocationStore()
	if _, _, err := store.Prepare(ctx, ref, args); err != nil {
		t.Fatal(err)
	}
	executor := &completingExecutor{
		store: store,
		result: mcptransport.Result{
			Tool:              "data.profile",
			SnapshotDigest:    strings.Repeat("a", 64),
			StructuredContent: json.RawMessage(`{"secret":"result-secret","decision":"KNOWN"}`),
		},
	}
	observation, err := (ToolDispatchActivity{Invocations: store, Executor: executor}).Execute(ctx, ref, 1)
	if err != nil {
		t.Fatal(err)
	}
	if observation.InvocationID != ref.InvocationID || observation.ResultDigest == "" || executor.calls != 1 {
		t.Fatalf("observation=%#v calls=%d", observation, executor.calls)
	}
	payload, _ := json.Marshal(observation)
	if strings.Contains(string(payload), "result-secret") || strings.Contains(string(payload), "raw-observation") {
		t.Fatalf("observation reference leaked raw payload: %s", payload)
	}
}

func TestDecisionRecordRejectsIncompleteToolBinding(t *testing.T) {
	record := DecisionRecord{
		RunID:          "run-1",
		Step:           1,
		Kind:           agentloop.DecisionTool,
		Tool:           "data.profile",
		DecisionDigest: strings.Repeat("0", 64),
	}
	if err := record.Validate(); err == nil {
		t.Fatal("tool decision record without invocation id must be rejected")
	}
}
