package temporalagent

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

	"github.com/achirothmane/governed-agent-runtime/agentloop"
	runtimesdk "github.com/achirothmane/governed-agent-runtime/sdk"
	"github.com/achirothmane/governed-agent-runtime/temporaltools"
)

const (
	WorkflowName             = "ai-native.agent.v1"
	ReasonActivityName       = "ai-native.agent.reason.v1"
	ToolDispatchActivityName = "ai-native.agent.tool-dispatch.v1"
)

type WorkflowInput struct {
	Execution ExecutionRef `json:"execution"`
}

type WorkflowResult struct {
	RunID        runtimesdk.RunID   `json:"run_id"`
	Kind         agentloop.OutcomeKind `json:"kind"`
	Message      string             `json:"message,omitempty"`
	Steps        int                `json:"steps"`
	Observations []ObservationRef   `json:"observations,omitempty"`
}

func Workflow(ctx workflow.Context, input WorkflowInput) (WorkflowResult, error) {
	if err := input.Execution.Validate(); err != nil {
		return WorkflowResult{}, temporal.NewNonRetryableApplicationError(
			"invalid durable agent execution",
			"INVALID_AGENT_EXECUTION",
			err,
		)
	}

	observations := make([]ObservationRef, 0, input.Execution.MaxSteps)
	for step := 1; step <= input.Execution.MaxSteps; step++ {
		reasonCtx := workflow.WithActivityOptions(ctx, workflow.ActivityOptions{
			ActivityID:             fmt.Sprintf("reason-%02d", step),
			StartToCloseTimeout:    5 * time.Minute,
			ScheduleToCloseTimeout: 15 * time.Minute,
			WaitForCancellation:    true,
			RetryPolicy: &temporal.RetryPolicy{
				InitialInterval:    time.Second,
				BackoffCoefficient: 2,
				MaximumInterval:    30 * time.Second,
				MaximumAttempts:    3,
			},
		})
		var decision ReasoningResult
		if err := workflow.ExecuteActivity(reasonCtx, ReasonActivityName, ReasoningRequest{
			Execution:    input.Execution,
			Step:         step,
			Observations: append([]ObservationRef(nil), observations...),
		}).Get(reasonCtx, &decision); err != nil {
			return WorkflowResult{}, err
		}
		if decision.Step != step || len(decision.DecisionDigest) != 64 {
			return WorkflowResult{}, temporal.NewNonRetryableApplicationError(
				"invalid durable reasoning result",
				"INVALID_REASONING_RESULT",
				errors.New("reasoning result binding is incomplete"),
			)
		}

		switch decision.Kind {
		case agentloop.DecisionTool:
			if decision.Invocation == nil {
				return WorkflowResult{}, temporal.NewNonRetryableApplicationError(
					"tool decision has no invocation reference",
					"INVALID_REASONING_RESULT",
					errors.New("missing tool invocation"),
				)
			}
			dispatchCtx := workflow.WithActivityOptions(ctx, workflow.ActivityOptions{
				ActivityID:             fmt.Sprintf("tool-dispatch-%02d", step),
				StartToCloseTimeout:    12 * time.Minute,
				ScheduleToCloseTimeout: 20 * time.Minute,
				WaitForCancellation:    true,
				RetryPolicy: &temporal.RetryPolicy{
					InitialInterval:    time.Second,
					BackoffCoefficient: 2,
					MaximumInterval:    30 * time.Second,
					MaximumAttempts:    3,
				},
			})
			var observation ObservationRef
			if err := workflow.ExecuteActivity(
				dispatchCtx,
				ToolDispatchActivityName,
				*decision.Invocation,
				step,
			).Get(dispatchCtx, &observation); err != nil {
				return WorkflowResult{}, err
			}
			if err := observation.Validate(); err != nil {
				return WorkflowResult{}, temporal.NewNonRetryableApplicationError(
					"invalid tool observation reference",
					"INVALID_OBSERVATION_REFERENCE",
					err,
				)
			}
			observations = append(observations, observation)

		case agentloop.DecisionFinish:
			return WorkflowResult{
				RunID:        input.Execution.RunID,
				Kind:         agentloop.OutcomeFinished,
				Message:      decision.Message,
				Steps:        step,
				Observations: observations,
			}, nil

		case agentloop.DecisionAsk:
			return WorkflowResult{
				RunID:        input.Execution.RunID,
				Kind:         agentloop.OutcomeWaitingInput,
				Message:      decision.Message,
				Steps:        step,
				Observations: observations,
			}, nil

		case agentloop.DecisionFail:
			return WorkflowResult{
				RunID:        input.Execution.RunID,
				Kind:         agentloop.OutcomeFailed,
				Message:      decision.Message,
				Steps:        step,
				Observations: observations,
			}, nil

		default:
			return WorkflowResult{}, temporal.NewNonRetryableApplicationError(
				"unsupported reasoning decision",
				"INVALID_REASONING_RESULT",
				fmt.Errorf("unsupported decision kind %q", decision.Kind),
			)
		}
	}

	return WorkflowResult{
		RunID:        input.Execution.RunID,
		Kind:         agentloop.OutcomeMaxSteps,
		Message:      "agent reached the configured maximum number of reasoning steps",
		Steps:        input.Execution.MaxSteps,
		Observations: observations,
	}, nil
}

func Register(w worker.Worker, reason ReasonActivity, dispatch ToolDispatchActivity) {
	w.RegisterWorkflowWithOptions(Workflow, workflow.RegisterOptions{Name: WorkflowName})
	w.RegisterActivityWithOptions(reason.Execute, activity.RegisterOptions{Name: ReasonActivityName})
	w.RegisterActivityWithOptions(dispatch.Execute, activity.RegisterOptions{Name: ToolDispatchActivityName})
}

type Client interface {
	ExecuteWorkflow(context.Context, client.StartWorkflowOptions, any, ...any) (client.WorkflowRun, error)
	GetWorkflow(context.Context, string, string) client.WorkflowRun
}

type Executor struct {
	Client      Client
	TaskQueue   string
	Executions  ExecutionStore
	Invocations temporaltools.InvocationStore
	MaxSteps    int
}

func (e Executor) Run(ctx context.Context, run runtimesdk.RunRequest, mission string) (agentloop.Outcome, error) {
	if e.Client == nil {
		return agentloop.Outcome{}, errors.New("temporal agent client is required")
	}
	if e.Executions == nil || e.Invocations == nil {
		return agentloop.Outcome{}, errors.New("durable agent stores are required")
	}
	if strings.TrimSpace(e.TaskQueue) == "" {
		return agentloop.Outcome{}, errors.New("temporal agent task queue is required")
	}
	maxSteps := e.MaxSteps
	if maxSteps == 0 {
		maxSteps = 8
	}
	ref, err := executionRef(run, mission, maxSteps)
	if err != nil {
		return agentloop.Outcome{}, err
	}
	stored, _, err := e.Executions.PrepareExecution(ctx, ref)
	if err != nil {
		return agentloop.Outcome{}, fmt.Errorf("prepare durable agent execution: %w", err)
	}
	if !sameExecution(stored, ref) {
		return agentloop.Outcome{}, ErrExecutionConflict
	}

	workflowID := "ai-native-agent/" + string(ref.RunID)
	workflowRun, err := e.Client.ExecuteWorkflow(ctx, client.StartWorkflowOptions{
		ID:                                       workflowID,
		TaskQueue:                                e.TaskQueue,
		WorkflowExecutionErrorWhenAlreadyStarted: true,
		WorkflowIDReusePolicy:                    enumspb.WORKFLOW_ID_REUSE_POLICY_REJECT_DUPLICATE,
		Memo: map[string]any{
			"ai_native_run_id":          string(ref.RunID),
			"ai_native_conversation_id": string(ref.ConversationID),
			"ai_native_run_fingerprint": ref.RunFingerprint,
			"ai_native_mission_digest":  ref.MissionDigest,
			"ai_native_max_steps":       ref.MaxSteps,
		},
	}, WorkflowName, WorkflowInput{Execution: ref})
	if err != nil {
		var alreadyStarted *serviceerror.WorkflowExecutionAlreadyStarted
		if !errors.As(err, &alreadyStarted) {
			return agentloop.Outcome{}, fmt.Errorf("start durable agent workflow: %w", err)
		}
		workflowRun = e.Client.GetWorkflow(ctx, workflowID, "")
	}
	if workflowRun == nil {
		return agentloop.Outcome{}, errors.New("temporal returned no agent workflow")
	}

	var output WorkflowResult
	if err := workflowRun.Get(ctx, &output); err != nil {
		return agentloop.Outcome{}, fmt.Errorf("durable agent workflow: %w", err)
	}
	if output.RunID != ref.RunID {
		return agentloop.Outcome{}, ErrExecutionConflict
	}

	observations := make([]agentloop.Observation, 0, len(output.Observations))
	for _, observation := range output.Observations {
		if err := observation.Validate(); err != nil {
			return agentloop.Outcome{}, err
		}
		invocation, err := e.Invocations.Get(ctx, observation.InvocationID)
		if err != nil {
			return agentloop.Outcome{}, fmt.Errorf("load durable agent observation: %w", err)
		}
		if invocation.State != temporaltools.InvocationComplete ||
			invocation.Result == nil ||
			invocation.ResultDigest != observation.ResultDigest {
			return agentloop.Outcome{}, temporaltools.ErrInvocationConflict
		}
		digest, err := temporaltools.ResultDigest(*invocation.Result)
		if err != nil {
			return agentloop.Outcome{}, err
		}
		if digest != observation.ResultDigest {
			return agentloop.Outcome{}, temporaltools.ErrInvocationConflict
		}
		observations = append(observations, agentloop.Observation{
			Step:         observation.Step,
			InvocationID: observation.InvocationID,
			Tool:         invocation.Ref.Tool.Name,
			Result:       *invocation.Result,
		})
	}
	return agentloop.Outcome{
		Kind:         output.Kind,
		Message:      output.Message,
		Steps:        output.Steps,
		Observations: observations,
	}, nil
}
