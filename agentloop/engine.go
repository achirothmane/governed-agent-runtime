package agentloop

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	runtimesdk "github.com/achirothmane/governed-agent-runtime/sdk"
)

type Engine struct {
	Reasoner Reasoner
	Tools    ToolExecutor
	MaxSteps int
}

func (e Engine) Run(ctx context.Context, run runtimesdk.RunRequest, mission string) (Outcome, error) {
	if e.Reasoner == nil {
		return Outcome{}, errors.New("agent reasoner is required")
	}
	if e.Tools == nil {
		return Outcome{}, errors.New("durable tool executor is required")
	}
	if err := run.Validate(); err != nil {
		return Outcome{}, fmt.Errorf("run: %w", err)
	}
	if strings.TrimSpace(mission) == "" {
		return Outcome{}, errors.New("agent mission is required")
	}

	maxSteps := e.MaxSteps
	if maxSteps == 0 {
		maxSteps = 8
	}
	if maxSteps < 1 || maxSteps > 64 {
		return Outcome{}, errors.New("agent max steps must be between 1 and 64")
	}

	observations := make([]Observation, 0, maxSteps)
	for step := 1; step <= maxSteps; step++ {
		if err := ctx.Err(); err != nil {
			return Outcome{}, err
		}
		decision, err := e.Reasoner.Decide(ctx, Turn{
			Run:          run,
			AgentMission: mission,
			Step:         step,
			Observations: append([]Observation(nil), observations...),
		})
		if err != nil {
			return Outcome{}, fmt.Errorf("agent reasoning step %d: %w", step, err)
		}
		if err := decision.Validate(); err != nil {
			return Outcome{}, fmt.Errorf("agent reasoning step %d: %w", step, err)
		}

		switch decision.Kind {
		case DecisionFinish:
			return Outcome{
				Kind:         OutcomeFinished,
				Message:      decision.Message,
				Steps:        step,
				Observations: observations,
			}, nil
		case DecisionAsk:
			return Outcome{
				Kind:         OutcomeWaitingInput,
				Message:      decision.Message,
				Steps:        step,
				Observations: observations,
			}, nil
		case DecisionFail:
			return Outcome{
				Kind:         OutcomeFailed,
				Message:      decision.Message,
				Steps:        step,
				Observations: observations,
			}, nil
		case DecisionTool:
			tool, ok := findTool(run.Tools, decision.Tool)
			if !ok {
				return Outcome{}, fmt.Errorf("%w: %s", ErrUnboundTool, decision.Tool)
			}
			if !tool.ReadOnly {
				return Outcome{}, fmt.Errorf("%w: %s", ErrMutatingTool, decision.Tool)
			}
			invocationID, err := InvocationID(run.ID, step, decision)
			if err != nil {
				return Outcome{}, fmt.Errorf("agent tool step %d: %w", step, err)
			}
			result, err := e.Tools.Execute(
				ctx,
				run.ID,
				run.Conversation.ID,
				invocationID,
				tool,
				decision.Arguments,
			)
			if err != nil {
				return Outcome{}, fmt.Errorf("agent tool step %d: %w", step, err)
			}
			observations = append(observations, Observation{
				Step:         step,
				InvocationID: invocationID,
				Tool:         tool.Name,
				Result:       result,
			})
		default:
			panic("validated decision kind became unsupported")
		}
	}

	return Outcome{
		Kind:         OutcomeMaxSteps,
		Message:      "agent reached the configured maximum number of reasoning steps",
		Steps:        maxSteps,
		Observations: observations,
	}, nil
}

func findTool(tools []runtimesdk.ToolDescriptor, name runtimesdk.ToolName) (runtimesdk.ToolDescriptor, bool) {
	for _, tool := range tools {
		if tool.Name == name {
			return tool, true
		}
	}
	return runtimesdk.ToolDescriptor{}, false
}

func InvocationID(runID runtimesdk.RunID, step int, decision Decision) (string, error) {
	payload, err := json.Marshal(struct {
		Run       runtimesdk.RunID    `json:"run"`
		Step      int                 `json:"step"`
		Tool      runtimesdk.ToolName `json:"tool"`
		Arguments map[string]any      `json:"arguments,omitempty"`
	}{
		Run:       runID,
		Step:      step,
		Tool:      decision.Tool,
		Arguments: decision.Arguments,
	})
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(payload)
	return fmt.Sprintf("a6-%02d-%s", step, hex.EncodeToString(sum[:8])), nil
}
