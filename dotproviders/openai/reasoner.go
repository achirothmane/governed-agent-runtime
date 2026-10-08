package openai

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/achirothmane/governed-agent-runtime/dotdecision"
	"github.com/achirothmane/governed-agent-runtime/portfoliocontext"
)

const DefaultEndpoint = "https://api.openai.com/v1/responses"

var (
	ErrInvalidConfig   = errors.New("invalid OpenAI Dot reasoner configuration")
	ErrProvider        = errors.New("OpenAI Dot reasoner provider error")
	ErrInvalidOutput   = errors.New("invalid OpenAI Dot decision output")
	ErrProviderRefusal = errors.New("OpenAI Dot reasoner refusal")
)

type Config struct {
	Endpoint   string
	Model      string
	APIKey     string
	HTTPClient *http.Client
}

type Reasoner struct {
	endpoint string
	model    string
	apiKey   string
	client   *http.Client
}

func New(config Config) (*Reasoner, error) {
	endpoint := strings.TrimSpace(config.Endpoint)
	if endpoint == "" {
		endpoint = DefaultEndpoint
	}
	if strings.TrimSpace(config.Model) == "" {
		return nil, fmt.Errorf("%w: model is required", ErrInvalidConfig)
	}
	if strings.TrimSpace(config.APIKey) == "" {
		return nil, fmt.Errorf("%w: API key is required", ErrInvalidConfig)
	}
	client := config.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 60 * time.Second}
	}
	return &Reasoner{
		endpoint: endpoint,
		model:    strings.TrimSpace(config.Model),
		apiKey:   strings.TrimSpace(config.APIKey),
		client:   client,
	}, nil
}

type boundedReasoningView struct {
	SnapshotDigest     string                          `json:"snapshot_digest"`
	StructuralReview   string                          `json:"structural_review"`
	NowProjects        []string                        `json:"now_projects"`
	NowProjectState    []portfoliocontext.ProjectState `json:"now_project_state"`
	RunnableItems      []portfoliocontext.RunnableItem `json:"runnable_items"`
	VerifiedContracts  []portfoliocontext.ContractEdge `json:"verified_contracts"`
	HumanFinalOn       []string                        `json:"human_final_on"`
	ExecutionPrinciple string                          `json:"execution_principle"`
	WIP                portfoliocontext.WIP            `json:"wip"`
}

func boundView(view portfoliocontext.ReasoningView) boundedReasoningView {
	return boundedReasoningView{
		SnapshotDigest:     view.SnapshotDigest,
		StructuralReview:   view.StructuralReview,
		NowProjects:        append([]string(nil), view.NowProjects...),
		NowProjectState:    append([]portfoliocontext.ProjectState(nil), view.NowProjectState...),
		RunnableItems:      append([]portfoliocontext.RunnableItem(nil), view.RunnableItems...),
		VerifiedContracts:  append([]portfoliocontext.ContractEdge(nil), view.VerifiedContracts...),
		HumanFinalOn:       append([]string(nil), view.HumanFinalOn...),
		ExecutionPrinciple: view.ExecutionPrinciple,
		WIP:                view.WIP,
	}
}

type responsesRequest struct {
	Model string         `json:"model"`
	Input []inputMessage `json:"input"`
	Text  textConfig     `json:"text"`
}

type inputMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type textConfig struct {
	Format formatConfig `json:"format"`
}

type formatConfig struct {
	Type   string         `json:"type"`
	Name   string         `json:"name"`
	Strict bool           `json:"strict"`
	Schema map[string]any `json:"schema"`
}

func decisionSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"kind": map[string]any{
				"type": "string",
				"enum": []string{
					string(dotdecision.ProposeNextGate),
					string(dotdecision.AskHuman),
					string(dotdecision.Refuse),
				},
			},
			"snapshot_digest": map[string]any{
				"type":    "string",
				"pattern": "^[0-9a-f]{64}$",
			},
			"work_item_id": map[string]any{"type": "string"},
			"requested_authority": map[string]any{
				"type": "string",
				"enum": []string{"OBSERVE", "PREPARE", "EXECUTE_REVERSIBLE", "COMMIT_EXTERNAL"},
			},
			"requested_action": map[string]any{"type": "string"},
			"rationale":        map[string]any{"type": "string"},
		},
		"required": []string{
			"kind",
			"snapshot_digest",
			"work_item_id",
			"requested_authority",
			"requested_action",
			"rationale",
		},
		"additionalProperties": false,
	}
}

func (r *Reasoner) Decide(ctx context.Context, view portfoliocontext.ReasoningView) (dotdecision.Decision, error) {
	if r == nil || r.client == nil || strings.TrimSpace(r.endpoint) == "" ||
		strings.TrimSpace(r.model) == "" || strings.TrimSpace(r.apiKey) == "" {
		return dotdecision.Decision{}, fmt.Errorf("%w: reasoner is not configured", ErrInvalidConfig)
	}
	if len(view.SnapshotDigest) != 64 || len(view.RunnableItems) == 0 {
		return dotdecision.Decision{}, fmt.Errorf("%w: incomplete bounded reasoning view", ErrInvalidConfig)
	}

	viewJSON, err := json.Marshal(boundView(view))
	if err != nil {
		return dotdecision.Decision{}, fmt.Errorf("encode bounded reasoning view: %w", err)
	}

	requestBody := responsesRequest{
		Model: r.model,
		Input: []inputMessage{
			{
				Role:    "system",
				Content: "You are the Portfolio Dot decision reasoner. Produce exactly one typed next-gate decision from the supplied sealed Portfolio ReasoningView. Do not claim execution occurred. Keep rationale concise and evidence-based. Human-final actions must use ASK_HUMAN. Return only the structured decision.",
			},
			{
				Role:    "user",
				Content: string(viewJSON),
			},
		},
		Text: textConfig{
			Format: formatConfig{
				Type:   "json_schema",
				Name:   "portfolio_dot_decision",
				Strict: true,
				Schema: decisionSchema(),
			},
		},
	}
	raw, err := json.Marshal(requestBody)
	if err != nil {
		return dotdecision.Decision{}, fmt.Errorf("encode Responses request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, r.endpoint, bytes.NewReader(raw))
	if err != nil {
		return dotdecision.Decision{}, fmt.Errorf("build Responses request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+r.apiKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := r.client.Do(req)
	if err != nil {
		return dotdecision.Decision{}, fmt.Errorf("%w: transport: %v", ErrProvider, err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil {
		return dotdecision.Decision{}, fmt.Errorf("%w: read response: %v", ErrProvider, err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return dotdecision.Decision{}, fmt.Errorf("%w: HTTP %d: %s", ErrProvider, resp.StatusCode, compact(body))
	}

	var envelope struct {
		Status string `json:"status"`
		Error  *struct {
			Message string `json:"message"`
		} `json:"error"`
		Output []struct {
			Type    string `json:"type"`
			Content []struct {
				Type    string `json:"type"`
				Text    string `json:"text"`
				Refusal string `json:"refusal"`
			} `json:"content"`
		} `json:"output"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		return dotdecision.Decision{}, fmt.Errorf("%w: decode response envelope: %v", ErrProvider, err)
	}
	if envelope.Error != nil && strings.TrimSpace(envelope.Error.Message) != "" {
		return dotdecision.Decision{}, fmt.Errorf("%w: %s", ErrProvider, envelope.Error.Message)
	}
	if envelope.Status != "completed" {
		return dotdecision.Decision{}, fmt.Errorf("%w: response status %q", ErrProvider, envelope.Status)
	}

	var outputText string
	for _, item := range envelope.Output {
		if item.Type != "message" {
			continue
		}
		for _, content := range item.Content {
			if content.Type == "refusal" && strings.TrimSpace(content.Refusal) != "" {
				return dotdecision.Decision{}, fmt.Errorf("%w: %s", ErrProviderRefusal, content.Refusal)
			}
			if content.Type == "output_text" && strings.TrimSpace(content.Text) != "" {
				if outputText != "" {
					return dotdecision.Decision{}, fmt.Errorf("%w: multiple output_text payloads", ErrInvalidOutput)
				}
				outputText = content.Text
			}
		}
	}
	if outputText == "" {
		return dotdecision.Decision{}, fmt.Errorf("%w: no output_text", ErrInvalidOutput)
	}

	var decision dotdecision.Decision
	decoder := json.NewDecoder(strings.NewReader(outputText))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&decision); err != nil {
		return dotdecision.Decision{}, fmt.Errorf("%w: decode decision: %v", ErrInvalidOutput, err)
	}
	if decoder.More() {
		return dotdecision.Decision{}, fmt.Errorf("%w: trailing decision JSON", ErrInvalidOutput)
	}
	if strings.TrimSpace(decision.RequestedAction) == "" {
		decision.RequestedAction = ""
	}
	return decision, nil
}

func compact(raw []byte) string {
	value := strings.TrimSpace(string(raw))
	if len(value) > 512 {
		return value[:512] + "..."
	}
	return value
}
