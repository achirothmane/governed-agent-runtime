package openairesponses

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	openai "github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
	"github.com/openai/openai-go/v3/responses"
	"github.com/openai/openai-go/v3/shared"

	"github.com/achirothmane/governed-agent-runtime/agentloop"
	runtimesdk "github.com/achirothmane/governed-agent-runtime/sdk"
)

const (
	defaultMaxOutputTokens = 768
	defaultMaxInputBytes    = 256 << 10
)

var (
	ErrNoDecision    = errors.New("OpenAI response contained no structured decision")
	ErrInputTooLarge = errors.New("OpenAI reasoning input exceeds configured byte limit")
)

type Config struct {
	APIKey          string
	Model           string
	MaxOutputTokens int
	MaxInputBytes   int
}

type ResponseClient interface {
	Create(context.Context, responses.ResponseNewParams) (string, error)
}

type sdkResponseClient struct {
	client openai.Client
}

func (c sdkResponseClient) Create(ctx context.Context, params responses.ResponseNewParams) (string, error) {
	response, err := c.client.Responses.New(ctx, params)
	if err != nil {
		return "", err
	}
	if response == nil {
		return "", ErrNoDecision
	}
	return response.OutputText(), nil
}

type Reasoner struct {
	client          ResponseClient
	model           string
	maxOutputTokens int
	maxInputBytes   int
}

var _ agentloop.Reasoner = (*Reasoner)(nil)

func New(config Config) (*Reasoner, error) {
	if strings.TrimSpace(config.APIKey) == "" {
		return nil, errors.New("OpenAI API key is required")
	}
	if err := validateConfig(config); err != nil {
		return nil, err
	}
	client := openai.NewClient(
		option.WithAPIKey(config.APIKey),
		// Temporal owns retry semantics for reasoning activities.
		option.WithMaxRetries(0),
	)
	return newWithClient(config, sdkResponseClient{client: client})
}

func NewWithClient(config Config, client ResponseClient) (*Reasoner, error) {
	if client == nil {
		return nil, errors.New("OpenAI response client is required")
	}
	if err := validateConfig(config); err != nil {
		return nil, err
	}
	return newWithClient(config, client)
}

func newWithClient(config Config, client ResponseClient) (*Reasoner, error) {
	maxOutputTokens := config.MaxOutputTokens
	if maxOutputTokens == 0 {
		maxOutputTokens = defaultMaxOutputTokens
	}
	maxInputBytes := config.MaxInputBytes
	if maxInputBytes == 0 {
		maxInputBytes = defaultMaxInputBytes
	}
	return &Reasoner{
		client:          client,
		model:           strings.TrimSpace(config.Model),
		maxOutputTokens: maxOutputTokens,
		maxInputBytes:   maxInputBytes,
	}, nil
}

func validateConfig(config Config) error {
	if strings.TrimSpace(config.Model) == "" {
		return errors.New("OpenAI model is required")
	}
	if config.MaxOutputTokens < 0 {
		return errors.New("max output tokens cannot be negative")
	}
	if config.MaxOutputTokens > 0 && config.MaxOutputTokens < 128 {
		return errors.New("max output tokens must be at least 128")
	}
	if config.MaxInputBytes < 0 {
		return errors.New("max input bytes cannot be negative")
	}
	if config.MaxInputBytes > 0 && config.MaxInputBytes < 1024 {
		return errors.New("max input bytes must be at least 1024")
	}
	return nil
}

func (r *Reasoner) Decide(ctx context.Context, turn agentloop.Turn) (agentloop.Decision, error) {
	if r == nil || r.client == nil {
		return agentloop.Decision{}, errors.New("OpenAI reasoner is not configured")
	}
	if err := turn.Run.Validate(); err != nil {
		return agentloop.Decision{}, fmt.Errorf("%w: bound run: %v", agentloop.ErrInvalidReasoningContext, err)
	}
	if strings.TrimSpace(turn.AgentMission) == "" {
		return agentloop.Decision{}, fmt.Errorf("%w: agent mission is required", agentloop.ErrInvalidReasoningContext)
	}
	if turn.Step < 1 {
		return agentloop.Decision{}, fmt.Errorf("%w: reasoning step must be positive", agentloop.ErrInvalidReasoningContext)
	}

	input, allowed, err := buildModelInput(turn)
	if err != nil {
		return agentloop.Decision{}, err
	}
	if len(input) > r.maxInputBytes {
		return agentloop.Decision{}, fmt.Errorf(
			"%w: %w: got=%d max=%d",
			agentloop.ErrInvalidReasoningContext,
			ErrInputTooLarge,
			len(input),
			r.maxInputBytes,
		)
	}

	raw, err := r.client.Create(ctx, responses.ResponseNewParams{
		Model: shared.ResponsesModel(r.model),
		Input: responses.ResponseNewParamsInputUnion{
			OfString: openai.String(string(input)),
		},
		Instructions:    openai.String(instructions),
		MaxOutputTokens: openai.Int(int64(r.maxOutputTokens)),
		Store:           openai.Bool(false),
		Text: responses.ResponseTextConfigParam{
			Format: responses.ResponseFormatTextConfigUnionParam{
				OfJSONSchema: &responses.ResponseFormatTextJSONSchemaConfigParam{
					Name:        "ai_native_runtime_decision",
					Description: openai.String("One structured next-step decision for the bound AI-native runtime"),
					Schema:      decisionSchema(),
					Strict:      openai.Bool(true),
				},
			},
		},
	})
	if err != nil {
		return agentloop.Decision{}, fmt.Errorf("OpenAI Responses API: %w", err)
	}

	decision, err := parseDecision(raw, allowed)
	if err != nil {
		return agentloop.Decision{}, fmt.Errorf("%w: %v", agentloop.ErrInvalidDecision, err)
	}
	return decision, nil
}

const instructions = `You are the decision component of an AI-native runtime.

Return exactly one structured decision. Do not execute tools yourself and do not claim that a tool ran.

Decision semantics:
- TOOL: select exactly one tool from available_tools. Set tool to its exact name, arguments_json to a JSON object string matching that tool's input schema, and message to an empty string.
- FINISH: the task is complete from the evidence available. Set tool to an empty string, arguments_json to "{}", and message to the concise user-visible answer.
- ASK: essential user input is missing. Set tool to an empty string, arguments_json to "{}", and message to the concise question for the user.
- FAIL: the task cannot proceed safely or correctly under the available capabilities/evidence. Set tool to an empty string, arguments_json to "{}", and message to the concise user-visible reason.

Never invent a tool. Never infer tool authority from a description. Do not expose or request chain-of-thought, hidden reasoning, credentials, endpoints, snapshot digests, workflow IDs, run IDs, conversation IDs, or internal runtime identifiers. Use only the supplied mission, user input, current step, available tool contracts, and prior normalized observations.`

type modelInput struct {
	Mission        string             `json:"mission"`
	UserInput      string             `json:"user_input"`
	Step           int                `json:"step"`
	AvailableTools []modelTool        `json:"available_tools"`
	Observations   []modelObservation `json:"observations,omitempty"`
}

type modelTool struct {
	Name        runtimesdk.ToolName `json:"name"`
	Title       string              `json:"title,omitempty"`
	Description string              `json:"description,omitempty"`
	InputSchema json.RawMessage     `json:"input_schema"`
}

type modelObservation struct {
	Step              int                 `json:"step"`
	Tool              runtimesdk.ToolName `json:"tool"`
	IsError           bool                `json:"is_error"`
	NeedsInput        bool                `json:"needs_input"`
	Content           []json.RawMessage   `json:"content,omitempty"`
	StructuredContent json.RawMessage     `json:"structured_content,omitempty"`
	InputRequests     json.RawMessage     `json:"input_requests,omitempty"`
	RequestState      string              `json:"request_state,omitempty"`
}

func buildModelInput(turn agentloop.Turn) ([]byte, map[runtimesdk.ToolName]struct{}, error) {
	tools := make([]modelTool, 0, len(turn.Run.Tools))
	allowed := make(map[runtimesdk.ToolName]struct{}, len(turn.Run.Tools))
	for _, tool := range turn.Run.Tools {
		if !tool.ReadOnly {
			continue
		}
		if len(tool.InputSchema) == 0 || !json.Valid(tool.InputSchema) {
			return nil, nil, fmt.Errorf(
				"%w: bound tool %q has no valid model-visible input schema",
				agentloop.ErrInvalidReasoningContext,
				tool.Name,
			)
		}
		tools = append(tools, modelTool{
			Name:        tool.Name,
			Title:       tool.Title,
			Description: tool.Description,
			InputSchema: append(json.RawMessage(nil), tool.InputSchema...),
		})
		allowed[tool.Name] = struct{}{}
	}

	observations := make([]modelObservation, 0, len(turn.Observations))
	for _, observation := range turn.Observations {
		observations = append(observations, modelObservation{
			Step:              observation.Step,
			Tool:              observation.Tool,
			IsError:           observation.Result.IsError,
			NeedsInput:        observation.Result.NeedsInput,
			Content:           append([]json.RawMessage(nil), observation.Result.Content...),
			StructuredContent: append(json.RawMessage(nil), observation.Result.StructuredContent...),
			InputRequests:     append(json.RawMessage(nil), observation.Result.InputRequests...),
			RequestState:      observation.Result.RequestState,
		})
	}
	payload, err := json.Marshal(modelInput{
		Mission:        turn.AgentMission,
		UserInput:      turn.Run.Input,
		Step:           turn.Step,
		AvailableTools: tools,
		Observations:   observations,
	})
	if err != nil {
		return nil, nil, fmt.Errorf("%w: marshal OpenAI reasoning input: %v", agentloop.ErrInvalidReasoningContext, err)
	}
	return payload, allowed, nil
}

type decisionEnvelope struct {
	Kind          agentloop.DecisionKind `json:"kind"`
	Tool          string                 `json:"tool"`
	ArgumentsJSON string                 `json:"arguments_json"`
	Message       string                 `json:"message"`
}

func decisionSchema() map[string]any {
	return map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"required":             []string{"kind", "tool", "arguments_json", "message"},
		"properties": map[string]any{
			"kind": map[string]any{
				"type": "string",
				"enum": []string{
					string(agentloop.DecisionTool),
					string(agentloop.DecisionFinish),
					string(agentloop.DecisionAsk),
					string(agentloop.DecisionFail),
				},
			},
			"tool": map[string]any{"type": "string"},
			"arguments_json": map[string]any{
				"type":        "string",
				"description": "A JSON object encoded as a string. Use {} for non-TOOL decisions.",
			},
			"message": map[string]any{"type": "string"},
		},
	}
}

func parseDecision(raw string, allowed map[runtimesdk.ToolName]struct{}) (agentloop.Decision, error) {
	if strings.TrimSpace(raw) == "" {
		return agentloop.Decision{}, ErrNoDecision
	}
	var envelope decisionEnvelope
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&envelope); err != nil {
		return agentloop.Decision{}, fmt.Errorf("decode structured decision: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return agentloop.Decision{}, errors.New("structured decision contains multiple JSON values")
		}
		return agentloop.Decision{}, fmt.Errorf("decode trailing structured decision data: %w", err)
	}
	arguments, err := decodeArguments(envelope.ArgumentsJSON)
	if err != nil {
		return agentloop.Decision{}, err
	}
	switch envelope.Kind {
	case agentloop.DecisionTool:
		tool := runtimesdk.ToolName(strings.TrimSpace(envelope.Tool))
		if _, ok := allowed[tool]; !ok {
			return agentloop.Decision{}, fmt.Errorf("%w: %s", agentloop.ErrUnboundTool, tool)
		}
		if strings.TrimSpace(envelope.Message) != "" {
			return agentloop.Decision{}, errors.New("TOOL decision must have an empty message")
		}
		decision := agentloop.Decision{Kind: envelope.Kind, Tool: tool, Arguments: arguments}
		if err := decision.Validate(); err != nil {
			return agentloop.Decision{}, err
		}
		return decision, nil
	case agentloop.DecisionFinish, agentloop.DecisionAsk, agentloop.DecisionFail:
		if strings.TrimSpace(envelope.Tool) != "" {
			return agentloop.Decision{}, errors.New("terminal decision must have an empty tool")
		}
		if len(arguments) != 0 {
			return agentloop.Decision{}, errors.New("terminal decision arguments_json must encode an empty object")
		}
		decision := agentloop.Decision{Kind: envelope.Kind, Message: strings.TrimSpace(envelope.Message)}
		if err := decision.Validate(); err != nil {
			return agentloop.Decision{}, err
		}
		return decision, nil
	default:
		return agentloop.Decision{}, fmt.Errorf("unsupported decision kind %q", envelope.Kind)
	}
}

func decodeArguments(raw string) (map[string]any, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, errors.New("arguments_json is required")
	}
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.UseNumber()
	var arguments map[string]any
	if err := decoder.Decode(&arguments); err != nil {
		return nil, fmt.Errorf("decode arguments_json: %w", err)
	}
	if arguments == nil {
		return nil, errors.New("arguments_json must encode a JSON object")
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return nil, errors.New("arguments_json contains multiple JSON values")
		}
		return nil, fmt.Errorf("decode trailing arguments_json data: %w", err)
	}
	return arguments, nil
}
