package temporaltools

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	enumspb "go.temporal.io/api/enums/v1"
	serviceerror "go.temporal.io/api/serviceerror"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/worker"
	"go.temporal.io/sdk/workflow"

	"github.com/achirothmane/governed-agent-runtime/mcptransport"
	runtimesdk "github.com/achirothmane/governed-agent-runtime/sdk"
)

const (
	WorkflowName = "ai-native.tool.v1"
	ActivityName = "ai-native.tool.execute.v1"
)

type WorkflowInput struct {
	Invocation InvocationRef `json:"invocation"`
}

func Workflow(ctx workflow.Context, input WorkflowInput) (ActivityResult, error) {
	if err := input.Invocation.Validate(); err != nil {
		return ActivityResult{}, temporal.NewNonRetryableApplicationError(
			"invalid durable tool invocation",
			"INVALID_TOOL_INVOCATION",
			err,
		)
	}
	ctx = workflow.WithActivityOptions(ctx, workflow.ActivityOptions{
		ActivityID:             "tool-" + input.Invocation.InvocationID,
		StartToCloseTimeout:    2 * time.Minute,
		ScheduleToCloseTimeout: 10 * time.Minute,
		WaitForCancellation:    true,
		RetryPolicy: &temporal.RetryPolicy{
			InitialInterval:    time.Second,
			BackoffCoefficient: 2,
			MaximumInterval:    30 * time.Second,
			MaximumAttempts:    5,
		},
	})
	var result ActivityResult
	if err := workflow.ExecuteActivity(ctx, ActivityName, input.Invocation).Get(ctx, &result); err != nil {
		return ActivityResult{}, err
	}
	return result, nil
}

func Register(w worker.Worker, a Activity) {
	w.RegisterWorkflowWithOptions(Workflow, workflow.RegisterOptions{Name: WorkflowName})
	w.RegisterActivityWithOptions(a.Execute, activity.RegisterOptions{Name: ActivityName})
}

type Client interface {
	ExecuteWorkflow(context.Context, client.StartWorkflowOptions, any, ...any) (client.WorkflowRun, error)
	GetWorkflow(context.Context, string, string) client.WorkflowRun
}

type Executor struct {
	Client    Client
	TaskQueue string
	Store     InvocationStore
}

func (e Executor) Execute(
	ctx context.Context,
	runID runtimesdk.RunID,
	conversationID runtimesdk.ConversationID,
	invocationID string,
	tool runtimesdk.ToolDescriptor,
	arguments map[string]any,
) (mcptransport.Result, error) {
	if e.Client == nil {
		return mcptransport.Result{}, errors.New("temporal tool client is required")
	}
	if e.Store == nil {
		return mcptransport.Result{}, errors.New("temporal tool invocation store is required")
	}
	if strings.TrimSpace(e.TaskQueue) == "" {
		return mcptransport.Result{}, errors.New("temporal tool task queue is required")
	}
	digest, err := ArgumentsDigest(arguments)
	if err != nil {
		return mcptransport.Result{}, fmt.Errorf("digest tool arguments: %w", err)
	}
	ref := InvocationRef{
		InvocationID:    strings.TrimSpace(invocationID),
		RunID:           runID,
		ConversationID:  conversationID,
		Tool:            tool,
		ArgumentsDigest: digest,
	}
	if err := ref.Validate(); err != nil {
		return mcptransport.Result{}, err
	}
	stored, _, err := e.Store.Prepare(ctx, ref, arguments)
	if err != nil {
		return mcptransport.Result{}, fmt.Errorf("prepare durable tool invocation: %w", err)
	}
	if stored.State == InvocationComplete {
		if stored.Result == nil {
			return mcptransport.Result{}, errors.New("completed invocation is missing result")
		}
		return *stored.Result, nil
	}

	workflowID := workflowID(ref)
	run, err := e.Client.ExecuteWorkflow(ctx, client.StartWorkflowOptions{
		ID:                                       workflowID,
		TaskQueue:                                e.TaskQueue,
		WorkflowExecutionErrorWhenAlreadyStarted: true,
		WorkflowIDReusePolicy:                    enumspb.WORKFLOW_ID_REUSE_POLICY_REJECT_DUPLICATE,
		Memo: map[string]any{
			"ai_native_run_id":           string(ref.RunID),
			"ai_native_conversation_id":  string(ref.ConversationID),
			"ai_native_invocation_id":    ref.InvocationID,
			"ai_native_tool":             string(ref.Tool.Name),
			"ai_native_snapshot_digest":  ref.Tool.SnapshotDigest,
			"ai_native_arguments_digest": ref.ArgumentsDigest,
		},
	}, WorkflowName, WorkflowInput{Invocation: ref})
	if err != nil {
		var alreadyStarted *serviceerror.WorkflowExecutionAlreadyStarted
		if !errors.As(err, &alreadyStarted) {
			return mcptransport.Result{}, fmt.Errorf("start durable tool workflow: %w", err)
		}
		run = e.Client.GetWorkflow(ctx, workflowID, "")
	}
	if run == nil {
		return mcptransport.Result{}, errors.New("temporal returned no tool workflow")
	}

	var output ActivityResult
	if err := run.Get(ctx, &output); err != nil {
		return mcptransport.Result{}, fmt.Errorf("durable tool workflow: %w", err)
	}
	if output.InvocationID != ref.InvocationID {
		return mcptransport.Result{}, ErrInvocationConflict
	}
	stored, err = e.Store.Get(ctx, ref.InvocationID)
	if err != nil {
		return mcptransport.Result{}, fmt.Errorf("load completed durable tool invocation: %w", err)
	}
	if stored.State != InvocationComplete || stored.Result == nil || stored.ResultDigest == "" {
		return mcptransport.Result{}, errors.New("durable tool workflow completed without a stored result")
	}
	if stored.ResultDigest != output.ResultDigest {
		return mcptransport.Result{}, errors.New("durable tool result digest mismatch")
	}
	gotDigest, err := ResultDigest(*stored.Result)
	if err != nil {
		return mcptransport.Result{}, err
	}
	if gotDigest != output.ResultDigest {
		return mcptransport.Result{}, errors.New("stored durable tool result digest mismatch")
	}
	return *stored.Result, nil
}

func workflowID(ref InvocationRef) string {
	return "ai-native-tool/" + string(ref.RunID) + "/" + ref.InvocationID
}
