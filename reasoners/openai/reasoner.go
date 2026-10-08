package openaireasoner

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	openai "github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
	"github.com/openai/openai-go/v3/responses"

	"github.com/achirothmane/governed-agent-runtime/agentloop"
	runtimesdk "github.com/achirothmane/governed-agent-runtime/sdk"
)

const (
	finishFunction = "runtime_finish"
	askFunction    = "runtime_ask"
	failFunction   = "runtime_fail"
)

type Config struct {
	Model           string
	MaxContextBytes int
}

type Reasoner struct {
	client          openai.Client
	model           string
	maxContextBytes int
}

func New(config Config, opts ...option.RequestOption) (*Reasoner, error) {
	if strings.TrimSpace(config.Model) == "" {
		return nil, errors.New("openai model is required")
	}
	maxContextBytes := config.MaxContextBytes
	if maxContextBytes == 0 {
		maxContextBytes = 256 << 10
	}
	if maxContextBytes < 4<<10 {
		return nil, errors.New("openai max context bytes must be at least 4096")
	}
	return &Reasoner{
		client:          openai.NewClient(opts...),
		model:           strings.TrimSpace(config.Model),
		maxContextBytes: maxContextBytes,
	}, nil
}

func (r *Reasoner) Decide(ctx context.Context, turn agentloop.Turn) (agentloop.Decision, error) {
	if r == nil {
		return agentloop.Decision{}, errors.New("openai reasoner is nil")
	}
	if err := turn.Run.Validate(); err != nil {
		return agentloop.Decision{}, fmt.Errorf("openai turn run: %w", err)
	}
	if strings.TrimSpace(turn.AgentMission) == "" {
		return agentloop.Decision{}, errors.New("openai turn agent mission is required")
	}
	if turn.Step < 1 {
		return agentloop.Decision{}, errors.New("openai turn step must be positive")
	}

	prompt, err := buildPrompt(turn)
	if err != nil {
		return agentloop.Decision{}, err
	}
	if len(prompt) > r.maxContextBytes {
		return agentloop.Decision{}, fmt.Errorf("openai reasoner context exceeds %d bytes", r.maxContextBytes)
	}
	tools, bindings, err := buildTools(turn.Run.Tools)
	if err != nil {
		return agentloop.Decision{}, err
	}

	response, err := r.client.Responses.New(ctx, responses.ResponseNewParams{
		Model: r.model,
		Input: responses.ResponseNewParamsInputUnion{
			OfString: openai.String(prompt),
		},
		Tools:             tools,
		ParallelToolCalls: openai.Bool(false),
		Store:             openai.Bool(false),
	})
	if err != nil {
		return agentloop.Decision{}, fmt.Errorf("openai responses: %w", err)
	}

	var calls []responses.ResponseFunctionToolCall
	for _, item := range response.Output {
		if item.Type == "function_call" {
			calls = append(calls, item.AsFunctionCall())
		}
	}
	if len(calls) != 1 {
		return agentloop.Decision{}, fmt.Errorf("openai reasoner expected exactly one function call, got %d", len(calls))
	}
	decision, err := decisionFromCall(calls[0], bindings)
	if err != nil {
		return agentloop.Decision{}, err
	}
	if err := decision.Validate(); err != nil {
		return agentloop.Decision{}, fmt.Errorf("openai decision: %w", err)
	}
	return decision, nil
}

type modelObservation struct {
	Step              int               `json:"step"`
	InvocationID      string            `json:"invocation_id"`
	Tool              runtimesdk.ToolName `json:"tool"`
	IsError           bool              `json:"is_error"`
	NeedsInput        bool              `json:"needs_input"`
	Content           []json.RawMessage `json:"content,omitempty"`
	StructuredContent json.RawMessage   `json:"structured_content,omitempty"`
	InputRequests     json.RawMessage   `json:"input_requests,omitempty"`
	RequestState      string            `json:"request_state,omitempty"`
}

func buildPrompt(turn agentloop.Turn) (string, error) {
	observations := make([]modelObservation, 0, len(turn.Observations))
	for _, observation := range turn.Observations {
		observations = append(observations, modelObservation{
			Step:              observation.Step,
			InvocationID:      observation.InvocationID,
			Tool:              observation.Tool,
			IsError:           observation.Result.IsError,
			NeedsInput:        observation.Result.NeedsInput,
			Content:           observation.Result.Content,
			StructuredContent: observation.Result.StructuredContent,
			InputRequests:     observation.Result.InputRequests,
			RequestState:      observation.Result.RequestState,
		})
	}
	observationJSON, err := json.Marshal(observations)
	if err != nil {
		return "", fmt.Errorf("marshal model observations: %w", err)
	}

	return fmt.Sprintf(
		"You are the decision component of an AI-native runtime.\n"+
			"Call exactly one supplied function. Do not return prose outside a function call.\n"+
			"Do not provide chain-of-thought, private reasoning, hidden analysis, or a rationale field.\n"+
			"Use a runtime tool only when its result is needed. Use runtime_finish when the task is complete. "+
			"Use runtime_ask only when user input is required. Use runtime_fail only when the task cannot proceed.\n\n"+
			"Agent mission:\n%s\n\n"+
			"User input:\n%s\n\n"+
			"Reasoning step: %d\n\n"+
			"Prior normalized observations (JSON):\n%s",
		turn.AgentMission,
		turn.Run.Input,
		turn.Step,
		string(observationJSON),
	), nil
}

func buildTools(descriptors []runtimesdk.ToolDescriptor) ([]responses.ToolUnionParam, map[string]runtimesdk.ToolName, error) {
	ordered := append([]runtimesdk.ToolDescriptor(nil), descriptors...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].Name < ordered[j].Name })

	tools := make([]responses.ToolUnionParam, 0, len(ordered)+3)
	bindings := make(map[string]runtimesdk.ToolName, len(ordered))
	for i, tool := range ordered {
		if !tool.ReadOnly {
			continue
		}
		if len(tool.InputSchema) == 0 {
			return nil, nil, fmt.Errorf("openai tool %q has no bound input schema", tool.Name)
		}
		var schema map[string]any
		decoder := json.NewDecoder(strings.NewReader(string(tool.InputSchema)))
		decoder.UseNumber()
		if err := decoder.Decode(&schema); err != nil {
			return nil, nil, fmt.Errorf("decode input schema for %q: %w", tool.Name, err)
		}
		name := fmt.Sprintf("runtime_tool_%03d", i)
		description := strings.TrimSpace(tool.Description)
		if description == "" {
			description = "Invoke the bound runtime tool " + string(tool.Name) + "."
		} else {
			description = string(tool.Name) + ": " + description
		}
		tools = append(tools, responses.ToolUnionParam{
			OfFunction: &responses.FunctionToolParam{
				Name:        name,
				Description: openai.String(description),
				Parameters:  schema,
			},
		})
		bindings[name] = tool.Name
	}

	tools = append(tools,
		controlTool(finishFunction, "Finish the task and return the final user-visible answer."),
		controlTool(askFunction, "Ask the user for information that is required before the task can continue."),
		controlTool(failFunction, "Stop because the task cannot proceed; provide a concise user-visible reason."),
	)
	return tools, bindings, nil
}

func controlTool(name, description string) responses.ToolUnionParam {
	return responses.ToolUnionParam{
		OfFunction: &responses.FunctionToolParam{
			Name:        name,
			Description: openai.String(description),
			Parameters: map[string]any{
				"type":                 "object",
				"additionalProperties": false,
				"properties": map[string]any{
					"message": map[string]any{"type": "string"},
				},
				"required": []string{"message"},
			},
			Strict: openai.Bool(true),
		},
	}
}

func decisionFromCall(call responses.ResponseFunctionToolCall, bindings map[string]runtimesdk.ToolName) (agentloop.Decision, error) {
	switch call.Name {
	case finishFunction, askFunction, failFunction:
		var payload struct {
			Message string `json:"message"`
		}
		if err := json.Unmarshal([]byte(call.Arguments), &payload); err != nil {
			return agentloop.Decision{}, fmt.Errorf("decode %s arguments: %w", call.Name, err)
		}
		kind := agentloop.DecisionFinish
		if call.Name == askFunction {
			kind = agentloop.DecisionAsk
		} else if call.Name == failFunction {
			kind = agentloop.DecisionFail
		}
		return agentloop.Decision{Kind: kind, Message: payload.Message}, nil
	default:
		tool, ok := bindings[call.Name]
		if !ok {
			return agentloop.Decision{}, fmt.Errorf("openai called unknown function %q", call.Name)
		}
		var arguments map[string]any
		decoder := json.NewDecoder(strings.NewReader(call.Arguments))
		decoder.UseNumber()
		if err := decoder.Decode(&arguments); err != nil {
			return agentloop.Decision{}, fmt.Errorf("decode tool arguments for %q: %w", tool, err)
		}
		if arguments == nil {
			arguments = map[string]any{}
		}
		return agentloop.Decision{
			Kind:      agentloop.DecisionTool,
			Tool:      tool,
			Arguments: arguments,
		}, nil
	}
}
