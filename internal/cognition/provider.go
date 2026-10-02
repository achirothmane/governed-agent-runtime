package cognition

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/achirothmane/governed-agent-runtime/internal/agent"
)

type ModelProvider interface {
	GeneratePlan(context.Context, PlanningRequest) (Plan, error)
}

type PlanningRequest struct {
	AgentID      agent.AgentID   `json:"agent_id"`
	Mission      string          `json:"mission"`
	EventID      string          `json:"event_id"`
	EventKind    string          `json:"event_kind"`
	EventPayload json.RawMessage `json:"event_payload,omitempty"`
	AllowedTools []agent.ToolRef `json:"allowed_tools,omitempty"`
	PolicyRef    string          `json:"policy_ref"`
	BudgetRef    string          `json:"budget_ref,omitempty"`
}

func NewPlanningRequest(a agent.Agent, eventID, eventKind string, eventPayload json.RawMessage) (PlanningRequest, error) {
	if err := a.Validate(); err != nil {
		return PlanningRequest{}, fmt.Errorf("validate agent: %w", err)
	}

	request := PlanningRequest{
		AgentID:      a.ID,
		Mission:      a.Mission,
		EventID:      eventID,
		EventKind:    eventKind,
		EventPayload: cloneRawMessage(eventPayload),
		AllowedTools: append([]agent.ToolRef(nil), a.AllowedTools...),
		PolicyRef:    a.PolicyRef,
		BudgetRef:    a.BudgetRef,
	}
	if err := request.Validate(); err != nil {
		return PlanningRequest{}, err
	}
	return request, nil
}

func (r PlanningRequest) Validate() error {
	if strings.TrimSpace(string(r.AgentID)) == "" {
		return errors.New("planning request agent id is required")
	}
	if strings.TrimSpace(r.Mission) == "" {
		return errors.New("planning request mission is required")
	}
	if strings.TrimSpace(r.EventID) == "" {
		return errors.New("planning request event id is required")
	}
	if strings.TrimSpace(r.EventKind) == "" {
		return errors.New("planning request event kind is required")
	}
	if strings.TrimSpace(r.PolicyRef) == "" {
		return errors.New("planning request policy ref is required")
	}
	if len(r.EventPayload) > 0 && !json.Valid(r.EventPayload) {
		return errors.New("planning request event payload must be valid JSON")
	}

	seen := make(map[agent.ToolRef]struct{}, len(r.AllowedTools))
	for _, tool := range r.AllowedTools {
		if strings.TrimSpace(string(tool)) == "" {
			return errors.New("planning request allowed tool cannot be empty")
		}
		if _, ok := seen[tool]; ok {
			return fmt.Errorf("planning request duplicate allowed tool %q", tool)
		}
		seen[tool] = struct{}{}
	}
	return nil
}

func (r PlanningRequest) Clone() PlanningRequest {
	out := r
	out.EventPayload = cloneRawMessage(r.EventPayload)
	out.AllowedTools = append([]agent.ToolRef(nil), r.AllowedTools...)
	return out
}

func cloneRawMessage(in json.RawMessage) json.RawMessage {
	if in == nil {
		return nil
	}
	return append(json.RawMessage(nil), in...)
}
