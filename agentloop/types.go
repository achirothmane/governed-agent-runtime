package agentloop

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/achirothmane/governed-agent-runtime/mcptransport"
	runtimesdk "github.com/achirothmane/governed-agent-runtime/sdk"
)

type DecisionKind string

const (
	DecisionTool   DecisionKind = "TOOL"
	DecisionFinish DecisionKind = "FINISH"
	DecisionAsk    DecisionKind = "ASK"
	DecisionFail   DecisionKind = "FAIL"
)

type Decision struct {
	Kind      DecisionKind          `json:"kind"`
	Tool      runtimesdk.ToolName   `json:"tool,omitempty"`
	Arguments map[string]any        `json:"arguments,omitempty"`
	Message   string                `json:"message,omitempty"`
}

type Observation struct {
	Step         int                 `json:"step"`
	InvocationID string              `json:"invocation_id"`
	Tool         runtimesdk.ToolName `json:"tool"`
	Result       mcptransport.Result `json:"result"`
}

type Turn struct {
	Run          runtimesdk.RunRequest      `json:"run"`
	AgentMission string                     `json:"agent_mission"`
	Step         int                        `json:"step"`
	Observations []Observation              `json:"observations,omitempty"`
}

type Reasoner interface {
	Decide(context.Context, Turn) (Decision, error)
}

type ToolExecutor interface {
	Execute(
		context.Context,
		runtimesdk.RunID,
		runtimesdk.ConversationID,
		string,
		runtimesdk.ToolDescriptor,
		map[string]any,
	) (mcptransport.Result, error)
}

type OutcomeKind string

const (
	OutcomeFinished     OutcomeKind = "FINISHED"
	OutcomeWaitingInput OutcomeKind = "WAITING_INPUT"
	OutcomeFailed       OutcomeKind = "FAILED"
	OutcomeMaxSteps     OutcomeKind = "MAX_STEPS"
)

type Outcome struct {
	Kind         OutcomeKind   `json:"kind"`
	Message      string        `json:"message,omitempty"`
	Steps        int           `json:"steps"`
	Observations []Observation `json:"observations,omitempty"`
}

var (
	ErrUnboundTool       = errors.New("agent selected a tool not bound to the run")
	ErrMutatingTool      = errors.New("A6 supports durable read-only tools only")
	ErrInvalidDecision   = errors.New("invalid agent decision")
)

func (d Decision) Validate() error {
	switch d.Kind {
	case DecisionTool:
		if strings.TrimSpace(string(d.Tool)) == "" {
			return fmt.Errorf("%w: TOOL requires tool", ErrInvalidDecision)
		}
		if strings.TrimSpace(d.Message) != "" {
			return fmt.Errorf("%w: TOOL must not include message", ErrInvalidDecision)
		}
	case DecisionFinish, DecisionAsk, DecisionFail:
		if strings.TrimSpace(d.Message) == "" {
			return fmt.Errorf("%w: %s requires message", ErrInvalidDecision, d.Kind)
		}
		if strings.TrimSpace(string(d.Tool)) != "" || d.Arguments != nil {
			return fmt.Errorf("%w: %s must not include tool arguments", ErrInvalidDecision, d.Kind)
		}
	default:
		return fmt.Errorf("%w: unsupported kind %q", ErrInvalidDecision, d.Kind)
	}
	if d.Arguments != nil {
		if _, err := json.Marshal(d.Arguments); err != nil {
			return fmt.Errorf("%w: arguments are not JSON-representable: %v", ErrInvalidDecision, err)
		}
	}
	return nil
}
