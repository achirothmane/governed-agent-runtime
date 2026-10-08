package temporaltools

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	serviceerror "go.temporal.io/api/serviceerror"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/testsuite"

	"github.com/achirothmane/governed-agent-runtime/mcptransport"
)

func TestWorkflowExecutesNamedActivityWithOpaqueReference(t *testing.T) {
	ctx := context.Background()
	args := map[string]any{
		"rows": []any{map[string]any{"id": "a", "secret": "raw-value"}},
	}
	ref := testRef(t, args)
	store := newMemoryInvocationStore()
	if _, _, err := store.Prepare(ctx, ref, args); err != nil {
		t.Fatal(err)
	}
	invoker := &countingInvoker{result: mcptransport.Result{
		Tool:              ref.Tool.Name,
		SnapshotDigest:    ref.Tool.SnapshotDigest,
		StructuredContent: json.RawMessage(`{"stage":"PRE_SEMANTIC_PROFILE","decision":"KNOWN"}`),
	}}

	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	env.RegisterActivityWithOptions(
		(Activity{Store: store, Invoker: invoker}).Execute,
		activity.RegisterOptions{Name: ActivityName},
	)
	env.ExecuteWorkflow(Workflow, WorkflowInput{Invocation: ref})
	if err := env.GetWorkflowError(); err != nil {
		t.Fatal(err)
	}
	var output ActivityResult
	if err := env.GetWorkflowResult(&output); err != nil {
		t.Fatal(err)
	}
	if output.InvocationID != ref.InvocationID || output.ResultDigest == "" {
		t.Fatalf("output=%#v", output)
	}
	invoker.mu.Lock()
	calls := invoker.calls
	invoker.mu.Unlock()
	if calls != 1 {
		t.Fatalf("invoker calls=%d want 1", calls)
	}
}

type fakeWorkflowRun struct {
	id     string
	runID  string
	output ActivityResult
	err    error
	onGet  func() error
}

func (r fakeWorkflowRun) GetID() string                  { return r.id }
func (r fakeWorkflowRun) GetRunID() string               { return r.runID }
func (r fakeWorkflowRun) GetFirstExecutionRunID() string { return r.runID }
func (r fakeWorkflowRun) Get(_ context.Context, valuePtr any) error {
	if r.err != nil {
		return r.err
	}
	if r.onGet != nil {
		if err := r.onGet(); err != nil {
			return err
		}
	}
	out, ok := valuePtr.(*ActivityResult)
	if !ok {
		return errors.New("unexpected workflow result target")
	}
	*out = r.output
	return nil
}
func (r fakeWorkflowRun) GetWithOptions(ctx context.Context, valuePtr any, _ client.WorkflowRunGetOptions) error {
	return r.Get(ctx, valuePtr)
}

type fakeTemporalClient struct {
	options       client.StartWorkflowOptions
	workflow      any
	args          []any
	run           client.WorkflowRun
	executeErr    error
	getWorkflowID string
	getRunID      string
}

func (c *fakeTemporalClient) ExecuteWorkflow(_ context.Context, options client.StartWorkflowOptions, workflow any, args ...any) (client.WorkflowRun, error) {
	c.options = options
	c.workflow = workflow
	c.args = append([]any(nil), args...)
	return c.run, c.executeErr
}

func (c *fakeTemporalClient) GetWorkflow(_ context.Context, workflowID, runID string) client.WorkflowRun {
	c.getWorkflowID = workflowID
	c.getRunID = runID
	return c.run
}

func TestExecutorPersistsRawArgumentsOutsideTemporalInput(t *testing.T) {
	ctx := context.Background()
	args := map[string]any{
		"rows": []any{map[string]any{"id": "a", "secret": "must-not-enter-history"}},
	}
	ref := testRef(t, args)
	result := mcptransport.Result{
		Tool:              ref.Tool.Name,
		SnapshotDigest:    ref.Tool.SnapshotDigest,
		StructuredContent: json.RawMessage(`{"stage":"PRE_SEMANTIC_PROFILE","decision":"KNOWN"}`),
	}
	resultDigest, err := ResultDigest(result)
	if err != nil {
		t.Fatal(err)
	}
	store := newMemoryInvocationStore()
	fc := &fakeTemporalClient{
		run: fakeWorkflowRun{
			id:    workflowID(ref),
			runID: "temporal-tool-run",
			output: ActivityResult{
				InvocationID: ref.InvocationID,
				ResultDigest: resultDigest,
			},
			onGet: func() error {
				_, err := store.Complete(ctx, ref.InvocationID, result, resultDigest)
				return err
			},
		},
	}
	executor := Executor{Client: fc, TaskQueue: "ai-native-tools", Store: store}
	got, err := executor.Execute(
		ctx,
		ref.RunID,
		ref.ConversationID,
		ref.InvocationID,
		ref.Tool,
		args,
	)
	if err != nil {
		t.Fatal(err)
	}
	if got.SnapshotDigest != ref.Tool.SnapshotDigest {
		t.Fatalf("result=%#v", got)
	}
	if fc.options.ID != workflowID(ref) || fc.options.TaskQueue != "ai-native-tools" {
		t.Fatalf("options=%#v", fc.options)
	}
	if len(fc.args) != 1 {
		t.Fatalf("workflow args=%d want 1", len(fc.args))
	}
	input, ok := fc.args[0].(WorkflowInput)
	if !ok {
		t.Fatalf("workflow input type=%T", fc.args[0])
	}
	payload, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	if string(payload) == "" || containsJSONSecret(payload, "must-not-enter-history") {
		t.Fatalf("workflow input leaked raw arguments: %s", payload)
	}
	stored, err := store.Get(ctx, ref.InvocationID)
	if err != nil {
		t.Fatal(err)
	}
	storedPayload, _ := json.Marshal(stored.Arguments)
	if !containsJSONSecret(storedPayload, "must-not-enter-history") {
		t.Fatalf("payload store did not retain raw arguments: %s", storedPayload)
	}
}

func TestExecutorReconcilesAlreadyStartedWorkflowByInvocationID(t *testing.T) {
	ctx := context.Background()
	args := map[string]any{"rows": []any{map[string]any{"id": "a"}}}
	ref := testRef(t, args)
	result := mcptransport.Result{Tool: ref.Tool.Name, SnapshotDigest: ref.Tool.SnapshotDigest}
	resultDigest, err := ResultDigest(result)
	if err != nil {
		t.Fatal(err)
	}
	store := newMemoryInvocationStore()
	fc := &fakeTemporalClient{
		executeErr: serviceerror.NewWorkflowExecutionAlreadyStarted("already started", "request-id", "run-id"),
		run: fakeWorkflowRun{
			id:    workflowID(ref),
			runID: "temporal-tool-run",
			output: ActivityResult{
				InvocationID: ref.InvocationID,
				ResultDigest: resultDigest,
			},
			onGet: func() error {
				_, err := store.Complete(ctx, ref.InvocationID, result, resultDigest)
				return err
			},
		},
	}
	executor := Executor{Client: fc, TaskQueue: "ai-native-tools", Store: store}
	if _, err := executor.Execute(ctx, ref.RunID, ref.ConversationID, ref.InvocationID, ref.Tool, args); err != nil {
		t.Fatal(err)
	}
	if fc.getWorkflowID != workflowID(ref) || fc.getRunID != "" {
		t.Fatalf("reconcile target workflow=%q run=%q", fc.getWorkflowID, fc.getRunID)
	}
}

func containsJSONSecret(payload []byte, secret string) bool {
	var value any
	if err := json.Unmarshal(payload, &value); err != nil {
		return false
	}
	return containsValue(value, secret)
}

func containsValue(value any, secret string) bool {
	switch v := value.(type) {
	case string:
		return v == secret
	case []any:
		for _, item := range v {
			if containsValue(item, secret) {
				return true
			}
		}
	case map[string]any:
		for _, item := range v {
			if containsValue(item, secret) {
				return true
			}
		}
	}
	return false
}
