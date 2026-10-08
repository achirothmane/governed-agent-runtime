package dotdurable

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
)

const (
	WorkflowName = "portfolio-dot.decision.v1"
	ActivityName = "portfolio-dot.decision.commit.v1"
)

type WorkflowInput struct {
	Request Request `json:"request"`
}

func Workflow(ctx workflow.Context, input WorkflowInput) (Record, error) {
	if err := input.Request.Validate(); err != nil {
		return Record{}, temporal.NewNonRetryableApplicationError(
			"invalid durable Dot decision request",
			"INVALID_DOT_DECISION_REQUEST",
			err,
		)
	}
	activityCtx := workflow.WithActivityOptions(ctx, workflow.ActivityOptions{
		ActivityID:             "decide-and-commit",
		StartToCloseTimeout:    5 * time.Minute,
		ScheduleToCloseTimeout: 15 * time.Minute,
		WaitForCancellation:    false,
		RetryPolicy: &temporal.RetryPolicy{
			InitialInterval:    time.Second,
			BackoffCoefficient: 2,
			MaximumInterval:    20 * time.Second,
			MaximumAttempts:    3,
		},
	})
	var record Record
	if err := workflow.ExecuteActivity(activityCtx, ActivityName, input.Request).Get(activityCtx, &record); err != nil {
		return Record{}, err
	}
	if err := record.Validate(); err != nil {
		return Record{}, temporal.NewNonRetryableApplicationError(
			"invalid committed Dot decision",
			"INVALID_DOT_DECISION_RECORD",
			err,
		)
	}
	if record.DecisionID != input.Request.DecisionID ||
		record.SnapshotDigest != input.Request.SnapshotDigest {
		return Record{}, temporal.NewNonRetryableApplicationError(
			"committed Dot decision binding mismatch",
			"DOT_DECISION_CONFLICT",
			ErrDecisionConflict,
		)
	}
	return record, nil
}

func Register(w worker.Worker, activityImpl DecideActivity) {
	w.RegisterWorkflowWithOptions(Workflow, workflow.RegisterOptions{Name: WorkflowName})
	w.RegisterActivityWithOptions(activityImpl.Execute, activity.RegisterOptions{Name: ActivityName})
}

type Client interface {
	ExecuteWorkflow(context.Context, client.StartWorkflowOptions, any, ...any) (client.WorkflowRun, error)
	GetWorkflow(context.Context, string, string) client.WorkflowRun
}

type Executor struct {
	Client    Client
	TaskQueue string
}

func (e Executor) Decide(ctx context.Context, req Request) (Record, error) {
	if e.Client == nil {
		return Record{}, errors.New("Temporal client is required")
	}
	if strings.TrimSpace(e.TaskQueue) == "" {
		return Record{}, errors.New("Dot decision task queue is required")
	}
	if err := req.Validate(); err != nil {
		return Record{}, err
	}
	workflowID := "portfolio-dot-decision/" + req.DecisionID
	run, err := e.Client.ExecuteWorkflow(ctx, client.StartWorkflowOptions{
		ID:                                       workflowID,
		TaskQueue:                                e.TaskQueue,
		WorkflowExecutionErrorWhenAlreadyStarted: true,
		WorkflowIDReusePolicy:                    enumspb.WORKFLOW_ID_REUSE_POLICY_REJECT_DUPLICATE,
		Memo: map[string]any{
			"portfolio_dot_decision_id":     req.DecisionID,
			"portfolio_dot_snapshot_digest": req.SnapshotDigest,
		},
	}, WorkflowName, WorkflowInput{Request: req})
	if err != nil {
		var alreadyStarted *serviceerror.WorkflowExecutionAlreadyStarted
		if !errors.As(err, &alreadyStarted) {
			return Record{}, fmt.Errorf("start durable Dot decision workflow: %w", err)
		}
		run = e.Client.GetWorkflow(ctx, workflowID, "")
	}
	if run == nil {
		return Record{}, errors.New("Temporal returned no Dot decision workflow")
	}
	var record Record
	if err := run.Get(ctx, &record); err != nil {
		return Record{}, fmt.Errorf("durable Dot decision workflow: %w", err)
	}
	if record.DecisionID != req.DecisionID || record.SnapshotDigest != req.SnapshotDigest {
		return Record{}, ErrDecisionConflict
	}
	return record, nil
}
