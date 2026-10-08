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
)

// ObservedTerminalReasoner is an explicit, single-read-only-observation policy.
// It uses native tool calling for the first step, then a structured *terminal*
// response after the durable read-only observation. It is not a replacement for
// the general multistep NativeReasoner, which remains the default.
//
// Its terminal response cannot request another tool, and plain text is never
// accepted in place of a typed decision. It does not execute tools.
type ObservedTerminalReasoner struct {
	first *NativeReasoner
	terminal ResponseClient
	model string
	maxOutputTokens int
	maxInputBytes int
}

var _ agentloop.Reasoner = (*ObservedTerminalReasoner)(nil)

func NewObservedTerminal(config Config) (*ObservedTerminalReasoner, error) {
	if strings.TrimSpace(config.APIKey) == "" {
		return nil, errors.New("OpenAI API key is required")
	}
	if err := validateConfig(config); err != nil {
		return nil, err
	}
	client := openai.NewClient(option.WithAPIKey(config.APIKey), option.WithMaxRetries(0))
	return NewObservedTerminalWithClients(config,
		nativeSDKClient{client: client}, sdkResponseClient{client: client})
}

func NewObservedTerminalWithClients(config Config, first NativeClient, terminal ResponseClient) (*ObservedTerminalReasoner, error) {
	if terminal == nil {
		return nil, errors.New("structured terminal response client is required")
	}
	native, err := NewNativeWithClient(config, first)
	if err != nil {
		return nil, err
	}
	return &ObservedTerminalReasoner{
		first: native,
		terminal: terminal,
		model: native.model,
		maxOutputTokens: native.maxOutputTokens,
		maxInputBytes: native.maxInputBytes,
	}, nil
}

var observedTerminalSchema = map[string]any{
	"type": "object",
	"additionalProperties": false,
	"required": []string{"kind", "message"},
	"properties": map[string]any{
		"kind": map[string]any{
			"type": "string",
			"enum": []string{"FINISH", "ASK", "FAIL"},
		},
		"message": map[string]any{"type": "string"},
	},
}

const observedTerminalInstructions = "A read-only tool observation has already been supplied from durable execution. Return one structured decision ONLY. FINISH when the evidence supports a concise factual answer, ASK only when essential user input is missing, or FAIL when evidence is insufficient. For conflicting values, report the conflict rather than resolving or modifying it. Never claim any external effect not evidenced by the observation. Do not invent tools or reveal credentials, run IDs, endpoints, snapshot digests, or hidden reasoning."

type observedTerminalEnvelope struct {
	Kind agentloop.DecisionKind `json:"kind"`
	Message string `json:"message"`
}

func (r *ObservedTerminalReasoner) Decide(ctx context.Context, turn agentloop.Turn) (agentloop.Decision, error) {
	if r == nil || r.first == nil || r.terminal == nil {
		return agentloop.Decision{}, errors.New("observed terminal reasoner is not configured")
	}
	if len(turn.Observations) == 0 {
		// First step remains a native function proposal behind exact schema
		// validation and the runtime's durable invocation boundary.
		return r.first.Decide(ctx, turn)
	}
	if err := turn.Run.Validate(); err != nil {
		return agentloop.Decision{}, fmt.Errorf("%w: bound run: %v", agentloop.ErrInvalidReasoningContext, err)
	}
	if strings.TrimSpace(turn.AgentMission) == "" || turn.Step < 2 {
		return agentloop.Decision{}, fmt.Errorf("%w: mission or terminal step invalid", agentloop.ErrInvalidReasoningContext)
	}
	if len(turn.Observations) != 1 {
		return agentloop.Decision{}, fmt.Errorf("%w: single-observation policy does not admit %d observations", agentloop.ErrInvalidReasoningContext, len(turn.Observations))
	}
	observation := turn.Observations[0]
	if observation.Step != turn.Step-1 || observation.Tool != observation.Result.Tool {
		return agentloop.Decision{}, fmt.Errorf("%w: observation step/tool binding mismatch", agentloop.ErrInvalidReasoningContext)
	}
	var admitted bool
	for _, tool := range turn.Run.Tools {
		if tool.ReadOnly && tool.Name == observation.Tool {
			admitted = true
			if observation.Result.SnapshotDigest != "" && tool.SnapshotDigest != observation.Result.SnapshotDigest {
				return agentloop.Decision{}, fmt.Errorf("%w: observation snapshot binding mismatch", agentloop.ErrInvalidReasoningContext)
			}
		}
	}
	if !admitted {
		return agentloop.Decision{}, fmt.Errorf("%w: observation came from unbound or mutating tool", agentloop.ErrInvalidReasoningContext)
	}
	// There must be an actual normalized observation to justify synthesis.
	hasEvidence := len(observation.Result.StructuredContent) > 0 || len(observation.Result.Content) > 0
	if len(observation.Result.StructuredContent) > 0 && !json.Valid(observation.Result.StructuredContent) {
		return agentloop.Decision{}, fmt.Errorf("%w: invalid normalized observation evidence", agentloop.ErrInvalidReasoningContext)
	}
	input, _, err := buildModelInput(turn)
	if err != nil {
		return agentloop.Decision{}, err
	}
	if len(input) > r.maxInputBytes {
		return agentloop.Decision{}, fmt.Errorf("%w: %w: got=%d max=%d",
			agentloop.ErrInvalidReasoningContext, ErrInputTooLarge, len(input), r.maxInputBytes)
	}
	raw, err := r.terminal.Create(ctx, responses.ResponseNewParams{
		Model: shared.ResponsesModel(r.model),
		Input: responses.ResponseNewParamsInputUnion{
			OfString: openai.String(string(input)),
		},
		Instructions: openai.String(observedTerminalInstructions),
		MaxOutputTokens: openai.Int(int64(r.maxOutputTokens)),
		Store: openai.Bool(false),
		Text: responses.ResponseTextConfigParam{
			Format: responses.ResponseFormatTextConfigUnionParam{
				OfJSONSchema: &responses.ResponseFormatTextJSONSchemaConfigParam{
					Name: "a7_observed_terminal_decision",
					Schema: observedTerminalSchema,
					Strict: openai.Bool(true),
				},
			},
		},
		// No provider tools here: an explicit one-observation policy only.
	})
	if err != nil {
		return agentloop.Decision{}, fmt.Errorf("structured observed terminal response: %w", err)
	}
	decision, err := parseObservedTerminal(raw)
	if err != nil {
		return agentloop.Decision{}, fmt.Errorf("%w: %v", agentloop.ErrInvalidDecision, err)
	}
	if decision.Kind == agentloop.DecisionFinish &&
		(!hasEvidence || observation.Result.IsError || observation.Result.NeedsInput) {
		return agentloop.Decision{}, fmt.Errorf("%w: FINISH is not supported by a successful tool observation", agentloop.ErrInvalidDecision)
	}
	return decision, nil
}

func parseObservedTerminal(raw string) (agentloop.Decision, error) {
	if strings.TrimSpace(raw) == "" {
		return agentloop.Decision{}, ErrNoDecision
	}
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.DisallowUnknownFields()
	var envelope observedTerminalEnvelope
	if err := decoder.Decode(&envelope); err != nil {
		return agentloop.Decision{}, fmt.Errorf("decode structured terminal decision: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return agentloop.Decision{}, errors.New("multiple structured terminal JSON values")
		}
		return agentloop.Decision{}, fmt.Errorf("trailing structured terminal data: %w", err)
	}
	if envelope.Kind != agentloop.DecisionFinish &&
		envelope.Kind != agentloop.DecisionAsk && envelope.Kind != agentloop.DecisionFail {
		return agentloop.Decision{}, fmt.Errorf("unsupported structured terminal kind %q", envelope.Kind)
	}
	decision := agentloop.Decision{Kind: envelope.Kind, Message: strings.TrimSpace(envelope.Message)}
	if err := decision.Validate(); err != nil {
		return agentloop.Decision{}, err
	}
	return decision, nil
}

