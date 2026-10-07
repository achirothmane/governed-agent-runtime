package temporalbackend

import (
	"context"
	"errors"
	"testing"

	commonpb "go.temporal.io/api/common/v1"
	enumspb "go.temporal.io/api/enums/v1"
	serviceerror "go.temporal.io/api/serviceerror"
	workflowpb "go.temporal.io/api/workflow/v1"
	workflowservice "go.temporal.io/api/workflowservice/v1"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/converter"

	runtimesdk "github.com/achirothmane/governed-agent-runtime/sdk"
)

type fakeWorkflowRun struct {
	id    string
	runID string
}

func (f fakeWorkflowRun) GetID() string                  { return f.id }
func (f fakeWorkflowRun) GetRunID() string               { return f.runID }
func (f fakeWorkflowRun) GetFirstExecutionRunID() string { return f.runID }
func (f fakeWorkflowRun) Get(context.Context, any) error { return nil }
func (f fakeWorkflowRun) GetWithOptions(context.Context, any, client.WorkflowRunGetOptions) error {
	return nil
}

type fakeClient struct {
	executeOptions   client.StartWorkflowOptions
	executeWorkflow  any
	executeArgs      []any
	executeRun       client.WorkflowRun
	executeErr       error
	describe         *workflowservice.DescribeWorkflowExecutionResponse
	describeErr      error
	signalWorkflowID string
	signalRunID      string
	signalName       string
	signalArg        any
	cancelWorkflowID string
	cancelRunID      string
}

func (f *fakeClient) ExecuteWorkflow(_ context.Context, options client.StartWorkflowOptions, workflow any, args ...any) (client.WorkflowRun, error) {
	f.executeOptions = options
	f.executeWorkflow = workflow
	f.executeArgs = append([]any(nil), args...)
	return f.executeRun, f.executeErr
}

func (f *fakeClient) DescribeWorkflowExecution(context.Context, string, string) (*workflowservice.DescribeWorkflowExecutionResponse, error) {
	return f.describe, f.describeErr
}

func (f *fakeClient) SignalWorkflow(_ context.Context, workflowID, runID, signalName string, arg any) error {
	f.signalWorkflowID = workflowID
	f.signalRunID = runID
	f.signalName = signalName
	f.signalArg = arg
	return nil
}

func (f *fakeClient) CancelWorkflow(_ context.Context, workflowID, runID string) error {
	f.cancelWorkflowID = workflowID
	f.cancelRunID = runID
	return nil
}

func validRun(t *testing.T) runtimesdk.RunRequest {
	t.Helper()
	catalog, err := runtimesdk.NewToolCatalog([]runtimesdk.ToolDescriptor{
		{Name: "github", Protocol: runtimesdk.ToolProtocolNative},
	})
	if err != nil {
		t.Fatal(err)
	}
	tools, err := catalog.Resolve([]runtimesdk.ToolName{"github"})
	if err != nil {
		t.Fatal(err)
	}
	return runtimesdk.RunRequest{
		ID:           "run-42",
		Conversation: runtimesdk.ConversationRef{ID: "conv-7", AgentID: "release-engineer", WorkspaceID: "repo-1"},
		Workspace:    runtimesdk.WorkspaceSpec{ID: "repo-1", Kind: runtimesdk.WorkspaceRemote},
		Input:        "diagnose and repair",
		Tools:        tools,
		PolicyRef:    "effects/default",
	}
}

func memoFor(t *testing.T, runID, fingerprint string) *commonpb.Memo {
	t.Helper()
	dc := converter.GetDefaultDataConverter()
	fp, err := dc.ToPayload(fingerprint)
	if err != nil {
		t.Fatal(err)
	}
	run, err := dc.ToPayload(runID)
	if err != nil {
		t.Fatal(err)
	}
	return &commonpb.Memo{Fields: map[string]*commonpb.Payload{
		memoFingerprintKey: fp,
		memoRunIDKey:       run,
	}}
}

func description(t *testing.T, runID, fingerprint, externalID string, status enumspb.WorkflowExecutionStatus) *workflowservice.DescribeWorkflowExecutionResponse {
	t.Helper()
	return &workflowservice.DescribeWorkflowExecutionResponse{
		WorkflowExecutionInfo: &workflowpb.WorkflowExecutionInfo{
			Execution: &commonpb.WorkflowExecution{WorkflowId: runID, RunId: externalID},
			Status:    status,
			Memo:      memoFor(t, runID, fingerprint),
		},
	}
}

func TestStartBindsRuntimeRunIntoTemporal(t *testing.T) {
	run := validRun(t)
	fingerprint, err := run.Fingerprint()
	if err != nil {
		t.Fatal(err)
	}
	fc := &fakeClient{executeRun: fakeWorkflowRun{id: string(run.ID), runID: "temporal-run-1"}}
	backend := Backend{Client: fc, TaskQueue: "ai-native", Workflow: "AIWorker"}

	handle, err := backend.Start(context.Background(), run)
	if err != nil {
		t.Fatal(err)
	}
	if handle.ID != run.ID || handle.ExternalID != "temporal-run-1" || handle.Fingerprint != fingerprint || handle.State != runtimesdk.RunQueued {
		t.Fatalf("unexpected handle: %#v", handle)
	}
	if fc.executeOptions.ID != string(run.ID) || fc.executeOptions.TaskQueue != "ai-native" {
		t.Fatalf("unexpected start options: %#v", fc.executeOptions)
	}
	if !fc.executeOptions.WorkflowExecutionErrorWhenAlreadyStarted {
		t.Fatal("already-started errors must be surfaced for reconciliation")
	}
	if fc.executeOptions.WorkflowIDReusePolicy != enumspb.WORKFLOW_ID_REUSE_POLICY_REJECT_DUPLICATE {
		t.Fatalf("reuse policy = %s", fc.executeOptions.WorkflowIDReusePolicy)
	}
	if got := fc.executeOptions.Memo[memoFingerprintKey]; got != fingerprint {
		t.Fatalf("memo fingerprint = %#v, want %q", got, fingerprint)
	}
	if got := fc.executeOptions.Memo[memoRunIDKey]; got != string(run.ID) {
		t.Fatalf("memo run id = %#v, want %q", got, run.ID)
	}
	if len(fc.executeArgs) != 1 {
		t.Fatalf("workflow args = %d, want 1", len(fc.executeArgs))
	}
	input, ok := fc.executeArgs[0].(WorkflowInput)
	if !ok || input.Fingerprint != fingerprint || input.Request.ID != run.ID {
		t.Fatalf("unexpected workflow input: %#v", fc.executeArgs[0])
	}
}

func TestAlreadyStartedReconcilesOnlyMatchingFingerprint(t *testing.T) {
	run := validRun(t)
	fingerprint, err := run.Fingerprint()
	if err != nil {
		t.Fatal(err)
	}
	fc := &fakeClient{
		executeErr: serviceerror.NewWorkflowExecutionAlreadyStarted("already started", "request-id", "temporal-run-1"),
		describe:   description(t, string(run.ID), fingerprint, "temporal-run-1", enumspb.WORKFLOW_EXECUTION_STATUS_RUNNING),
	}
	backend := Backend{Client: fc, TaskQueue: "ai-native", Workflow: "AIWorker"}

	handle, err := backend.Start(context.Background(), run)
	if err != nil {
		t.Fatal(err)
	}
	if handle.ExternalID != "temporal-run-1" || handle.Fingerprint != fingerprint || handle.State != runtimesdk.RunRunning {
		t.Fatalf("unexpected reconciled handle: %#v", handle)
	}
}

func TestAlreadyStartedRejectsFingerprintMismatch(t *testing.T) {
	run := validRun(t)
	fc := &fakeClient{
		executeErr: serviceerror.NewWorkflowExecutionAlreadyStarted("already started", "request-id", "temporal-run-1"),
		describe:   description(t, string(run.ID), "different-fingerprint", "temporal-run-1", enumspb.WORKFLOW_EXECUTION_STATUS_RUNNING),
	}
	backend := Backend{Client: fc, TaskQueue: "ai-native", Workflow: "AIWorker"}

	_, err := backend.Start(context.Background(), run)
	if !errors.Is(err, ErrBindingMismatch) {
		t.Fatalf("error = %v, want ErrBindingMismatch", err)
	}
}

func TestInspectFailsClosedWhenBindingMemoMissing(t *testing.T) {
	fc := &fakeClient{describe: &workflowservice.DescribeWorkflowExecutionResponse{
		WorkflowExecutionInfo: &workflowpb.WorkflowExecutionInfo{
			Execution: &commonpb.WorkflowExecution{WorkflowId: "run-42", RunId: "temporal-run-1"},
			Status:    enumspb.WORKFLOW_EXECUTION_STATUS_RUNNING,
		},
	}}
	backend := Backend{Client: fc, TaskQueue: "ai-native", Workflow: "AIWorker"}

	handle, err := backend.Inspect(context.Background(), "run-42")
	if !errors.Is(err, ErrUnprovenBinding) {
		t.Fatalf("error = %v, want ErrUnprovenBinding", err)
	}
	if handle.State != runtimesdk.RunUnknown {
		t.Fatalf("state = %s, want UNKNOWN", handle.State)
	}
}

func TestInspectMapsTemporalStatusAfterBindingProof(t *testing.T) {
	cases := []struct {
		status enumspb.WorkflowExecutionStatus
		want   runtimesdk.RunState
	}{
		{enumspb.WORKFLOW_EXECUTION_STATUS_RUNNING, runtimesdk.RunRunning},
		{enumspb.WORKFLOW_EXECUTION_STATUS_COMPLETED, runtimesdk.RunSucceeded},
		{enumspb.WORKFLOW_EXECUTION_STATUS_FAILED, runtimesdk.RunFailed},
		{enumspb.WORKFLOW_EXECUTION_STATUS_TIMED_OUT, runtimesdk.RunFailed},
		{enumspb.WORKFLOW_EXECUTION_STATUS_TERMINATED, runtimesdk.RunFailed},
		{enumspb.WORKFLOW_EXECUTION_STATUS_CANCELED, runtimesdk.RunCanceled},
		{enumspb.WORKFLOW_EXECUTION_STATUS_CONTINUED_AS_NEW, runtimesdk.RunUnknown},
	}
	for _, tc := range cases {
		t.Run(tc.status.String(), func(t *testing.T) {
			fc := &fakeClient{describe: description(t, "run-42", "fp", "temporal-run-1", tc.status)}
			backend := Backend{Client: fc, TaskQueue: "ai-native", Workflow: "AIWorker"}
			handle, err := backend.Inspect(context.Background(), "run-42")
			if err != nil {
				t.Fatal(err)
			}
			if handle.State != tc.want {
				t.Fatalf("state = %s, want %s", handle.State, tc.want)
			}
		})
	}
}

func TestSignalAndCancelTargetLatestBoundExecution(t *testing.T) {
	fc := &fakeClient{}
	backend := Backend{Client: fc, TaskQueue: "ai-native", Workflow: "AIWorker"}
	if err := backend.Signal(context.Background(), "run-42", "human.approved", []byte("yes")); err != nil {
		t.Fatal(err)
	}
	if fc.signalWorkflowID != "run-42" || fc.signalRunID != "" || fc.signalName != "human.approved" {
		t.Fatalf("unexpected signal target: workflow=%q run=%q signal=%q", fc.signalWorkflowID, fc.signalRunID, fc.signalName)
	}
	if err := backend.Cancel(context.Background(), "run-42", "operator requested"); err != nil {
		t.Fatal(err)
	}
	if fc.cancelWorkflowID != "run-42" || fc.cancelRunID != "" {
		t.Fatalf("unexpected cancel target: workflow=%q run=%q", fc.cancelWorkflowID, fc.cancelRunID)
	}
}
