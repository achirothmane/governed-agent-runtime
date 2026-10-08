package openairesponses

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/google/jsonschema-go/jsonschema"
	openai "github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
	"github.com/openai/openai-go/v3/responses"
	"github.com/openai/openai-go/v3/shared"

	"github.com/achirothmane/governed-agent-runtime/agentloop"
	runtimesdk "github.com/achirothmane/governed-agent-runtime/sdk"
)

// NativeReasoner turns provider-native function calls into proposed, inert decisions.
type NativeReasoner struct {
	client NativeClient
	model string
	maxOutputTokens int
	maxInputBytes int
}

var _ agentloop.Reasoner = (*NativeReasoner)(nil)

type NativeCall struct {
	Name string
	Arguments string
	Incomplete bool
}

type NativeOutput struct {
	Calls []NativeCall
	OtherOutputs int
}

type NativeClient interface {
	CreateNative(context.Context, responses.ResponseNewParams) (NativeOutput, error)
}

type nativeSDKClient struct {
	client openai.Client
}

func (c nativeSDKClient) CreateNative(ctx context.Context, params responses.ResponseNewParams) (NativeOutput, error) {
	response, err := c.client.Responses.New(ctx, params)
	if err != nil { return NativeOutput{}, err }
	if response == nil { return NativeOutput{}, ErrNoDecision }
	out := NativeOutput{}
	for _, item := range response.Output {
		switch item.Type {
		case "function_call":
			call := item.AsFunctionCall()
			out.Calls = append(out.Calls, NativeCall{
				Name: call.Name, Arguments: call.Arguments, Incomplete: call.Status == "incomplete",
			})
		case "reasoning":
			// Do not persist reasoning.
		default:
			out.OtherOutputs++
		}
	}
	return out, nil
}

func NewNative(config Config) (*NativeReasoner, error) {
	if strings.TrimSpace(config.APIKey) == "" { return nil, errors.New("OpenAI API key is required") }
	if err := validateConfig(config); err != nil { return nil, err }
	client := openai.NewClient(option.WithAPIKey(config.APIKey), option.WithMaxRetries(0))
	return NewNativeWithClient(config, nativeSDKClient{client: client})
}

func NewNativeWithClient(config Config, client NativeClient) (*NativeReasoner, error) {
	if client == nil { return nil, errors.New("native Responses client is required") }
	if err := validateConfig(config); err != nil { return nil, err }
	outputLimit := config.MaxOutputTokens
	if outputLimit == 0 { outputLimit = defaultMaxOutputTokens }
	inputLimit := config.MaxInputBytes
	if inputLimit == 0 { inputLimit = defaultMaxInputBytes }
	return &NativeReasoner{
		client: client, model: strings.TrimSpace(config.Model),
		maxOutputTokens: outputLimit, maxInputBytes: inputLimit,
	}, nil
}

type nativeBoundTool struct {
	Name runtimesdk.ToolName
	Schema *jsonschema.Resolved
}

var terminalSchema = map[string]any{
	"type": "object",
	"additionalProperties": false,
	"properties": map[string]any{"message": map[string]any{"type": "string"}},
	"required": []string{"message"},
}

func nativeFunction(name, description string, schema map[string]any, strict bool) responses.ToolUnionParam {
	return responses.ToolUnionParam{OfFunction: &responses.FunctionToolParam{
		Name: name, Description: openai.String(description),
		Parameters: schema, Strict: openai.Bool(strict),
	}}
}

// Capability aliases avoid provider restrictions on punctuation in function names.
func buildNativeTools(turn agentloop.Turn) ([]responses.ToolUnionParam, map[string]nativeBoundTool, error) {
	tools := make([]responses.ToolUnionParam, 0, len(turn.Run.Tools)+3)
	bound := make(map[string]nativeBoundTool)
	for _, tool := range turn.Run.Tools {
		if !tool.ReadOnly { continue }
		if len(tool.InputSchema) == 0 {
			return nil, nil, fmt.Errorf("%w: missing schema for %q", agentloop.ErrInvalidReasoningContext, tool.Name)
		}
		var raw map[string]any
		if err := json.Unmarshal(tool.InputSchema, &raw); err != nil {
			return nil, nil, fmt.Errorf("%w: invalid schema for %q: %v", agentloop.ErrInvalidReasoningContext, tool.Name, err)
		}
		if raw["type"] != "object" {
			return nil, nil, fmt.Errorf("%w: nonobject schema for %q", agentloop.ErrInvalidReasoningContext, tool.Name)
		}
		var schema jsonschema.Schema
		if err := json.Unmarshal(tool.InputSchema, &schema); err != nil {
			return nil, nil, fmt.Errorf("%w: parse schema for %q: %v", agentloop.ErrInvalidReasoningContext, tool.Name, err)
		}
		resolved, err := schema.Resolve(nil)
		if err != nil {
			return nil, nil, fmt.Errorf("%w: resolve schema for %q: %v", agentloop.ErrInvalidReasoningContext, tool.Name, err)
		}
		alias := fmt.Sprintf("cap_%d", len(bound))
		bound[alias] = nativeBoundTool{Name: tool.Name, Schema: resolved}
		tools = append(tools, nativeFunction(alias, "Read-only "+string(tool.Name)+": "+tool.Description, raw, false))
	}
	tools = append(tools,
		nativeFunction("runtime_finish", "Finish after adequate evidence. Provide final answer in message.", terminalSchema, true),
		nativeFunction("runtime_ask", "Ask for essential missing information only.", terminalSchema, true),
		nativeFunction("runtime_fail", "Refuse or report inability to proceed correctly.", terminalSchema, true),
	)
	return tools, bound, nil
}

const nativeInstructions = "You are a decision component, not a tool executor. Choose exactly one available function. To inspect data, call the matching cap_N tool using JSON arguments. Only after receiving evidence, call runtime_finish with a concise message. Use runtime_ask only when essential user input is missing, and runtime_fail if work cannot proceed. Never claim a function ran yourself. Never invent an endpoint or authority. Do not put JSON inside a string. Do not reveal credentials, internal identifiers or hidden reasoning."

func (r *NativeReasoner) Decide(ctx context.Context, turn agentloop.Turn) (agentloop.Decision, error) {
	if r == nil || r.client == nil { return agentloop.Decision{}, errors.New("native reasoner not configured") }
	if err := turn.Run.Validate(); err != nil {
		return agentloop.Decision{}, fmt.Errorf("%w: bound run: %v", agentloop.ErrInvalidReasoningContext, err)
	}
	if strings.TrimSpace(turn.AgentMission) == "" || turn.Step < 1 {
		return agentloop.Decision{}, fmt.Errorf("%w: mission and step required", agentloop.ErrInvalidReasoningContext)
	}
	tools, bound, err := buildNativeTools(turn)
	if err != nil { return agentloop.Decision{}, err }
	input, _, err := buildModelInput(turn)
	if err != nil { return agentloop.Decision{}, err }
	if len(input) > r.maxInputBytes {
		return agentloop.Decision{}, fmt.Errorf("%w: %w: got=%d max=%d", agentloop.ErrInvalidReasoningContext, ErrInputTooLarge, len(input), r.maxInputBytes)
	}
	out, err := r.client.CreateNative(ctx, responses.ResponseNewParams{
		Model: shared.ResponsesModel(r.model),
		Input: responses.ResponseNewParamsInputUnion{OfString: openai.String(string(input))},
		Instructions: openai.String(nativeInstructions),
		MaxOutputTokens: openai.Int(int64(r.maxOutputTokens)),
		Store: openai.Bool(false),
		Tools: tools,
	})
	if err != nil { return agentloop.Decision{}, fmt.Errorf("OpenAI native function calls: %w", err) }
	decision, err := parseNativeDecision(out, bound)
	if err != nil { return agentloop.Decision{}, fmt.Errorf("%w: %v", agentloop.ErrInvalidDecision, err) }
	return decision, nil
}

func parseNativeDecision(out NativeOutput, allowed map[string]nativeBoundTool) (agentloop.Decision, error) {
	if len(out.Calls) != 1 || out.OtherOutputs != 0 {
		return agentloop.Decision{}, fmt.Errorf("expected exactly one function call, got calls=%d other=%d", len(out.Calls), out.OtherOutputs)
	}
	call := out.Calls[0]
	if call.Incomplete { return agentloop.Decision{}, errors.New("incomplete native function call") }
	args, err := decodeArguments(call.Arguments)
	if err != nil { return agentloop.Decision{}, err }
	if boundTool, ok := allowed[call.Name]; ok {
		var instance map[string]any
		if err := json.Unmarshal([]byte(call.Arguments), &instance); err != nil {
			return agentloop.Decision{}, fmt.Errorf("decode arguments for schema: %w", err)
		}
		if err := boundTool.Schema.Validate(instance); err != nil {
			return agentloop.Decision{}, fmt.Errorf("tool arguments violate bound schema: %w", err)
		}
		decision := agentloop.Decision{Kind: agentloop.DecisionTool, Tool: boundTool.Name, Arguments: args}
		if err := decision.Validate(); err != nil { return agentloop.Decision{}, err }
		return decision, nil
	}
	var kind agentloop.DecisionKind
	switch call.Name {
	case "runtime_finish": kind = agentloop.DecisionFinish
	case "runtime_ask": kind = agentloop.DecisionAsk
	case "runtime_fail": kind = agentloop.DecisionFail
	default: return agentloop.Decision{}, fmt.Errorf("unknown or unbound native function %q", call.Name)
	}
	if len(args) != 1 { return agentloop.Decision{}, errors.New("terminal function must contain message only") }
	message, ok := args["message"].(string)
	if !ok { return agentloop.Decision{}, errors.New("terminal function message must be a string") }
	decision := agentloop.Decision{Kind: kind, Message: strings.TrimSpace(message)}
	if err := decision.Validate(); err != nil { return agentloop.Decision{}, err }
	return decision, nil
}
