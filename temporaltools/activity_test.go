package temporaltools

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"

	"github.com/achirothmane/governed-agent-runtime/mcptransport"
	runtimesdk "github.com/achirothmane/governed-agent-runtime/sdk"
)

type memoryInvocationStore struct {
	mu      sync.Mutex
	records map[string]Invocation
}

func newMemoryInvocationStore() *memoryInvocationStore {
	return &memoryInvocationStore{records: map[string]Invocation{}}
}

func (s *memoryInvocationStore) Prepare(_ context.Context, ref InvocationRef, args map[string]any) (Invocation, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if existing, ok := s.records[ref.InvocationID]; ok {
		if !SameBinding(existing.Ref, ref) {
			return Invocation{}, false, ErrInvocationConflict
		}
		return cloneInvocation(existing), false, nil
	}
	record := Invocation{
		Ref:       ref,
		Arguments: cloneMap(args),
		State:     InvocationPrepared,
	}
	s.records[ref.InvocationID] = record
	return cloneInvocation(record), true, nil
}

func (s *memoryInvocationStore) Get(_ context.Context, id string) (Invocation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	record, ok := s.records[id]
	if !ok {
		return Invocation{}, ErrInvocationNotFound
	}
	return cloneInvocation(record), nil
}

func (s *memoryInvocationStore) Complete(_ context.Context, id string, result mcptransport.Result, digest string) (Invocation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	record, ok := s.records[id]
	if !ok {
		return Invocation{}, ErrInvocationNotFound
	}
	if record.State == InvocationComplete {
		if record.ResultDigest != digest {
			return Invocation{}, ErrInvocationConflict
		}
		return cloneInvocation(record), nil
	}
	copyResult := result
	record.Result = &copyResult
	record.ResultDigest = digest
	record.LastError = ""
	record.State = InvocationComplete
	s.records[id] = record
	return cloneInvocation(record), nil
}

func (s *memoryInvocationStore) Fail(_ context.Context, id, message string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	record, ok := s.records[id]
	if !ok {
		return ErrInvocationNotFound
	}
	if record.State != InvocationComplete {
		record.State = InvocationFailed
		record.LastError = message
		s.records[id] = record
	}
	return nil
}

func cloneInvocation(in Invocation) Invocation {
	out := in
	out.Arguments = cloneMap(in.Arguments)
	if in.Result != nil {
		result := *in.Result
		result.Content = append([]json.RawMessage(nil), in.Result.Content...)
		result.StructuredContent = append(json.RawMessage(nil), in.Result.StructuredContent...)
		out.Result = &result
	}
	return out
}

func cloneMap(in map[string]any) map[string]any {
	if in == nil {
		return nil
	}
	payload, _ := json.Marshal(in)
	var out map[string]any
	_ = json.Unmarshal(payload, &out)
	return out
}

type countingInvoker struct {
	mu     sync.Mutex
	calls  int
	result mcptransport.Result
	err    error
}

func (i *countingInvoker) Invoke(_ context.Context, _ runtimesdk.ToolDescriptor, _ map[string]any) (mcptransport.Result, error) {
	i.mu.Lock()
	defer i.mu.Unlock()
	i.calls++
	if i.err != nil {
		return mcptransport.Result{}, i.err
	}
	return i.result, nil
}

func testRef(t *testing.T, args map[string]any) InvocationRef {
	t.Helper()
	digest, err := ArgumentsDigest(args)
	if err != nil {
		t.Fatal(err)
	}
	return InvocationRef{
		InvocationID:   "inv-1",
		RunID:          "run-1",
		ConversationID: "conv-1",
		Tool: runtimesdk.ToolDescriptor{
			Name:           "data.profile",
			Protocol:       runtimesdk.ToolProtocolMCP,
			Endpoint:       "http://data-engine.test/mcp",
			ReadOnly:       true,
			SnapshotDigest: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		},
		ArgumentsDigest: digest,
	}
}

func TestActivityReusesStoredCompletedResultWithoutReinvoking(t *testing.T) {
	ctx := context.Background()
	args := map[string]any{"rows": []any{map[string]any{"id": "a", "price": 20.0}}}
	ref := testRef(t, args)
	store := newMemoryInvocationStore()
	if _, _, err := store.Prepare(ctx, ref, args); err != nil {
		t.Fatal(err)
	}
	invoker := &countingInvoker{result: mcptransport.Result{
		Tool:           ref.Tool.Name,
		SnapshotDigest: ref.Tool.SnapshotDigest,
		StructuredContent: json.RawMessage(
			`{"stage":"PRE_SEMANTIC_PROFILE","decision":"KNOWN"}`,
		),
	}}
	activity := Activity{Store: store, Invoker: invoker}

	first, err := activity.Execute(ctx, ref)
	if err != nil {
		t.Fatal(err)
	}
	second, err := activity.Execute(ctx, ref)
	if err != nil {
		t.Fatal(err)
	}
	if first.Reused {
		t.Fatal("first activity execution unexpectedly reused a result")
	}
	if !second.Reused {
		t.Fatal("second activity execution did not reuse the completed result")
	}
	invoker.mu.Lock()
	calls := invoker.calls
	invoker.mu.Unlock()
	if calls != 1 {
		t.Fatalf("invoker calls=%d want 1", calls)
	}
	if first.ResultDigest == "" || first.ResultDigest != second.ResultDigest {
		t.Fatalf("result digests first=%q second=%q", first.ResultDigest, second.ResultDigest)
	}
}

func TestActivityRejectsArgumentsDigestMismatchBeforeInvocation(t *testing.T) {
	ctx := context.Background()
	args := map[string]any{"rows": []any{map[string]any{"id": "a"}}}
	ref := testRef(t, args)
	store := newMemoryInvocationStore()
	if _, _, err := store.Prepare(ctx, ref, args); err != nil {
		t.Fatal(err)
	}
	store.mu.Lock()
	record := store.records[ref.InvocationID]
	record.Arguments = map[string]any{"rows": []any{map[string]any{"id": "tampered"}}}
	store.records[ref.InvocationID] = record
	store.mu.Unlock()

	invoker := &countingInvoker{}
	_, err := (Activity{Store: store, Invoker: invoker}).Execute(ctx, ref)
	if !errors.Is(err, ErrInvocationConflict) {
		t.Fatalf("error=%v want ErrInvocationConflict", err)
	}
	invoker.mu.Lock()
	calls := invoker.calls
	invoker.mu.Unlock()
	if calls != 0 {
		t.Fatalf("invoker calls=%d want 0", calls)
	}
}

func TestActivityRecordsFailureAndAllowsTemporalRetry(t *testing.T) {
	ctx := context.Background()
	args := map[string]any{"rows": []any{map[string]any{"id": "a"}}}
	ref := testRef(t, args)
	store := newMemoryInvocationStore()
	if _, _, err := store.Prepare(ctx, ref, args); err != nil {
		t.Fatal(err)
	}
	invoker := &countingInvoker{err: errors.New("temporary transport failure")}
	activity := Activity{Store: store, Invoker: invoker}
	if _, err := activity.Execute(ctx, ref); err == nil {
		t.Fatal("expected invocation failure")
	}
	record, err := store.Get(ctx, ref.InvocationID)
	if err != nil {
		t.Fatal(err)
	}
	if record.State != InvocationFailed || record.LastError == "" {
		t.Fatalf("failed record=%#v", record)
	}

	invoker.mu.Lock()
	invoker.err = nil
	invoker.result = mcptransport.Result{Tool: ref.Tool.Name, SnapshotDigest: ref.Tool.SnapshotDigest}
	invoker.mu.Unlock()
	if _, err := activity.Execute(ctx, ref); err != nil {
		t.Fatal(err)
	}
	record, err = store.Get(ctx, ref.InvocationID)
	if err != nil {
		t.Fatal(err)
	}
	if record.State != InvocationComplete {
		t.Fatalf("state=%s want COMPLETE", record.State)
	}
}

func TestResultDigestIgnoresEquivalentStructuredJSONFormatting(t *testing.T) {
	first := mcptransport.Result{
		Tool:           "data.profile",
		SnapshotDigest: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		StructuredContent: json.RawMessage(
			"{\"stage\":\"PRE_SEMANTIC_PROFILE\",\"decision\":\"KNOWN\",\"meta\":{\"b\":2,\"a\":1}}",
		),
	}
	second := mcptransport.Result{
		Tool:           "data.profile",
		SnapshotDigest: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		StructuredContent: json.RawMessage(
			"{ \"meta\": { \"a\": 1, \"b\": 2 }, \"decision\": \"KNOWN\", \"stage\": \"PRE_SEMANTIC_PROFILE\" }",
		),
	}
	a, err := ResultDigest(first)
	if err != nil {
		t.Fatal(err)
	}
	b, err := ResultDigest(second)
	if err != nil {
		t.Fatal(err)
	}
	if a != b {
		t.Fatalf("semantic-equivalent result digests differ: %s != %s", a, b)
	}
}
