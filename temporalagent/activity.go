package temporalagent

import (
	"context"
	"errors"
	"fmt"

	"go.temporal.io/sdk/temporal"

	"github.com/achirothmane/governed-agent-runtime/agentloop"
	runtimesdk "github.com/achirothmane/governed-agent-runtime/sdk"
	"github.com/achirothmane/governed-agent-runtime/temporaltools"
)

type ReasoningRequest struct {
	Execution    ExecutionRef     `json:"execution"`
	Step         int              `json:"step"`
	Observations []ObservationRef `json:"observations,omitempty"`
}

type ReasoningResult struct {
	Step           int                          `json:"step"`
	Kind           agentloop.DecisionKind       `json:"kind"`
	Message        string                       `json:"message,omitempty"`
	DecisionDigest string                       `json:"decision_digest"`
	Invocation     *temporaltools.InvocationRef `json:"invocation,omitempty"`
}

type ReasonActivity struct {
	Resolver    RunResolver
	Reasoner    agentloop.Reasoner
	Executions  ExecutionStore
	Steps       StepStore
	Invocations temporaltools.InvocationStore
}

func (a ReasonActivity) Execute(ctx context.Context, req ReasoningRequest) (ReasoningResult, error) {
	if a.Resolver == nil || a.Reasoner == nil || a.Executions == nil || a.Steps == nil || a.Invocations == nil {
		return ReasoningResult{}, errors.New("durable reasoning activity is not fully configured")
	}
	if err := req.Execution.Validate(); err != nil {
		return ReasoningResult{}, nonRetryable("INVALID_AGENT_EXECUTION", err)
	}
	if req.Step < 1 || req.Step > req.Execution.MaxSteps {
		return ReasoningResult{}, nonRetryable("INVALID_REASONING_STEP", errors.New("reasoning step is outside execution bounds"))
	}

	storedExecution, err := a.Executions.GetExecution(ctx, req.Execution.RunID)
	if err != nil {
		return ReasoningResult{}, fmt.Errorf("load agent execution: %w", err)
	}
	if !sameExecution(storedExecution, req.Execution) {
		return ReasoningResult{}, nonRetryable("AGENT_EXECUTION_CONFLICT", ErrExecutionConflict)
	}

	run, mission, err := a.Resolver.Resolve(ctx, req.Execution.RunID)
	if err != nil {
		return ReasoningResult{}, fmt.Errorf("resolve bound run: %w", err)
	}
	fingerprint, err := run.Fingerprint()
	if err != nil {
		return ReasoningResult{}, nonRetryable("INVALID_BOUND_RUN", err)
	}
	if run.ID != req.Execution.RunID ||
		run.Conversation.ID != req.Execution.ConversationID ||
		fingerprint != req.Execution.RunFingerprint ||
		MissionDigest(mission) != req.Execution.MissionDigest {
		return ReasoningResult{}, nonRetryable("AGENT_BINDING_CHANGED", ErrExecutionConflict)
	}

	record, err := a.Steps.GetDecision(ctx, req.Execution.RunID, req.Step)
	switch {
	case err == nil:
		if err := record.Validate(); err != nil {
			return ReasoningResult{}, nonRetryable("INVALID_STORED_DECISION", err)
		}
		return a.materializeDecision(ctx, run, record)
	case !errors.Is(err, ErrDecisionNotFound):
		return ReasoningResult{}, fmt.Errorf("load reasoning decision: %w", err)
	}

	observations, err := a.loadObservations(ctx, req.Observations, req.Step)
	if err != nil {
		return ReasoningResult{}, err
	}
	decision, err := a.Reasoner.Decide(ctx, agentloop.Turn{
		Run:          run,
		AgentMission: mission,
		Step:         req.Step,
		Observations: observations,
	})
	if err != nil {
		return ReasoningResult{}, fmt.Errorf("reasoning step %d: %w", req.Step, err)
	}
	if err := decision.Validate(); err != nil {
		return ReasoningResult{}, nonRetryable("INVALID_REASONING_DECISION", err)
	}
	invocationID := ""
	if decision.Kind == agentloop.DecisionTool {
		ref, args, err := toolRef(run, req.Step, decision)
		if err != nil {
			return ReasoningResult{}, nonRetryable("INVALID_TOOL_DECISION", err)
		}
		storedInvocation, _, err := a.Invocations.Prepare(ctx, ref, args)
		if err != nil {
			return ReasoningResult{}, fmt.Errorf("prepare durable tool invocation: %w", err)
		}
		if !temporaltools.SameBinding(storedInvocation.Ref, ref) {
			return ReasoningResult{}, nonRetryable("TOOL_INVOCATION_CONFLICT", temporaltools.ErrInvocationConflict)
		}
		invocationID = ref.InvocationID
	}

	record, err = newDecisionRecord(run.ID, req.Step, decision, invocationID)
	if err != nil {
		return ReasoningResult{}, nonRetryable("INVALID_REASONING_DECISION", err)
	}
	stored, _, err := a.Steps.PutDecision(ctx, record)
	if err != nil {
		if errors.Is(err, ErrDecisionConflict) {
			return ReasoningResult{}, nonRetryable("REASONING_DECISION_CONFLICT", err)
		}
		return ReasoningResult{}, fmt.Errorf("persist reasoning decision: %w", err)
	}
	return a.materializeDecision(ctx, run, stored)
}

func (a ReasonActivity) materializeDecision(
	ctx context.Context,
	run runtimesdk.RunRequest,
	record DecisionRecord,
) (ReasoningResult, error) {
	if err := record.Validate(); err != nil {
		return ReasoningResult{}, nonRetryable("INVALID_STORED_DECISION", err)
	}
	result := ReasoningResult{
		Step:           record.Step,
		Kind:           record.Kind,
		Message:        record.Message,
		DecisionDigest: record.DecisionDigest,
	}
	if record.Kind != agentloop.DecisionTool {
		decision := agentloop.Decision{Kind: record.Kind, Message: record.Message}
		digest, err := DecisionDigest(decision)
		if err != nil || digest != record.DecisionDigest {
			return ReasoningResult{}, nonRetryable("REASONING_DECISION_CONFLICT", ErrDecisionConflict)
		}
		return result, nil
	}

	invocation, err := a.Invocations.Get(ctx, record.InvocationID)
	if err != nil {
		return ReasoningResult{}, fmt.Errorf("load committed tool invocation: %w", err)
	}
	if invocation.Ref.RunID != run.ID ||
		invocation.Ref.ConversationID != run.Conversation.ID ||
		invocation.Ref.Tool.Name != record.Tool {
		return ReasoningResult{}, nonRetryable("TOOL_INVOCATION_CONFLICT", temporaltools.ErrInvocationConflict)
	}
	decision := agentloop.Decision{
		Kind:      agentloop.DecisionTool,
		Tool:      record.Tool,
		Arguments: invocation.Arguments,
	}
	digest, err := DecisionDigest(decision)
	if err != nil || digest != record.DecisionDigest {
		return ReasoningResult{}, nonRetryable("REASONING_DECISION_CONFLICT", ErrDecisionConflict)
	}
	result.Invocation = &invocation.Ref
	return result, nil
}

func (a ReasonActivity) loadObservations(ctx context.Context, refs []ObservationRef, currentStep int) ([]agentloop.Observation, error) {
	observations := make([]agentloop.Observation, 0, len(refs))
	for _, ref := range refs {
		if err := ref.Validate(); err != nil {
			return nil, nonRetryable("INVALID_OBSERVATION_REFERENCE", err)
		}
		if ref.Step >= currentStep {
			return nil, nonRetryable("INVALID_OBSERVATION_REFERENCE", errors.New("observation step must precede current reasoning step"))
		}
		invocation, err := a.Invocations.Get(ctx, ref.InvocationID)
		if err != nil {
			return nil, fmt.Errorf("load observation invocation %s: %w", ref.InvocationID, err)
		}
		if invocation.State != temporaltools.InvocationComplete || invocation.Result == nil {
			return nil, fmt.Errorf("observation invocation %s is not complete", ref.InvocationID)
		}
		if invocation.ResultDigest != ref.ResultDigest {
			return nil, nonRetryable("OBSERVATION_BINDING_CONFLICT", temporaltools.ErrInvocationConflict)
		}
		digest, err := temporaltools.ResultDigest(*invocation.Result)
		if err != nil {
			return nil, nonRetryable("INVALID_STORED_OBSERVATION", err)
		}
		if digest != ref.ResultDigest {
			return nil, nonRetryable("OBSERVATION_BINDING_CONFLICT", temporaltools.ErrInvocationConflict)
		}
		observations = append(observations, agentloop.Observation{
			Step:         ref.Step,
			InvocationID: ref.InvocationID,
			Tool:         invocation.Ref.Tool.Name,
			Result:       *invocation.Result,
		})
	}
	return observations, nil
}

type ToolDispatchActivity struct {
	Invocations temporaltools.InvocationStore
	Executor    agentloop.ToolExecutor
}

func (a ToolDispatchActivity) Execute(ctx context.Context, ref temporaltools.InvocationRef, step int) (ObservationRef, error) {
	if a.Invocations == nil || a.Executor == nil {
		return ObservationRef{}, errors.New("durable tool dispatch activity is not fully configured")
	}
	if err := ref.Validate(); err != nil {
		return ObservationRef{}, nonRetryable("INVALID_TOOL_INVOCATION", err)
	}
	if step < 1 {
		return ObservationRef{}, nonRetryable("INVALID_TOOL_STEP", errors.New("tool step must be positive"))
	}
	stored, err := a.Invocations.Get(ctx, ref.InvocationID)
	if err != nil {
		return ObservationRef{}, fmt.Errorf("load prepared tool invocation: %w", err)
	}
	if !temporaltools.SameBinding(stored.Ref, ref) {
		return ObservationRef{}, nonRetryable("TOOL_INVOCATION_CONFLICT", temporaltools.ErrInvocationConflict)
	}
	if _, err := a.Executor.Execute(
		ctx,
		ref.RunID,
		ref.ConversationID,
		ref.InvocationID,
		ref.Tool,
		stored.Arguments,
	); err != nil {
		return ObservationRef{}, err
	}
	stored, err = a.Invocations.Get(ctx, ref.InvocationID)
	if err != nil {
		return ObservationRef{}, fmt.Errorf("reload completed tool invocation: %w", err)
	}
	if stored.State != temporaltools.InvocationComplete || stored.Result == nil || stored.ResultDigest == "" {
		return ObservationRef{}, errors.New("durable tool dispatch completed without stored result")
	}
	return ObservationRef{
		Step:         step,
		InvocationID: ref.InvocationID,
		ResultDigest: stored.ResultDigest,
	}, nil
}

func nonRetryable(kind string, err error) error {
	return temporal.NewNonRetryableApplicationError(err.Error(), kind, err)
}
