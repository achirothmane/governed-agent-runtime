package cognition

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/achirothmane/governed-agent-runtime/internal/agent"
)

var (
	ErrProviderProtocol           = errors.New("model provider returned an invalid plan")
	ErrToolOutsideAgentCapability = errors.New("proposed tool is outside agent capability declaration")
)

type Plan struct {
	ID          string           `json:"id"`
	AgentID     agent.AgentID    `json:"agent_id"`
	EventID     string           `json:"event_id"`
	ProviderRef string           `json:"provider_ref"`
	Summary     string           `json:"summary,omitempty"`
	Effects     []ProposedEffect `json:"effects,omitempty"`
}

type ProposedEffect struct {
	ID           string          `json:"id"`
	Tool         agent.ToolRef   `json:"tool"`
	Action       string          `json:"action"`
	Arguments    json.RawMessage `json:"arguments,omitempty"`
	EvidenceRefs []string        `json:"evidence_refs,omitempty"`
	Rationale    string          `json:"rationale,omitempty"`
}

func (p Plan) ValidateAgainst(request PlanningRequest) error {
	if err := request.Validate(); err != nil {
		return err
	}
	if strings.TrimSpace(p.ID) == "" {
		return fmt.Errorf("%w: plan id is required", ErrProviderProtocol)
	}
	if p.AgentID != request.AgentID {
		return fmt.Errorf("%w: plan agent %q does not match request agent %q", ErrProviderProtocol, p.AgentID, request.AgentID)
	}
	if p.EventID != request.EventID {
		return fmt.Errorf("%w: plan event %q does not match request event %q", ErrProviderProtocol, p.EventID, request.EventID)
	}
	if strings.TrimSpace(p.ProviderRef) == "" {
		return fmt.Errorf("%w: provider ref is required", ErrProviderProtocol)
	}

	allowed := make(map[agent.ToolRef]struct{}, len(request.AllowedTools))
	for _, tool := range request.AllowedTools {
		allowed[tool] = struct{}{}
	}

	seenEffects := make(map[string]struct{}, len(p.Effects))
	for i, effect := range p.Effects {
		if strings.TrimSpace(effect.ID) == "" {
			return fmt.Errorf("%w: effect[%d] id is required", ErrProviderProtocol, i)
		}
		if _, ok := seenEffects[effect.ID]; ok {
			return fmt.Errorf("%w: duplicate effect id %q", ErrProviderProtocol, effect.ID)
		}
		seenEffects[effect.ID] = struct{}{}

		if strings.TrimSpace(string(effect.Tool)) == "" {
			return fmt.Errorf("%w: effect[%d] tool is required", ErrProviderProtocol, i)
		}
		if _, ok := allowed[effect.Tool]; !ok {
			return fmt.Errorf("%w: %s", ErrToolOutsideAgentCapability, effect.Tool)
		}
		if strings.TrimSpace(effect.Action) == "" {
			return fmt.Errorf("%w: effect[%d] action is required", ErrProviderProtocol, i)
		}
		if len(effect.Arguments) > 0 && !json.Valid(effect.Arguments) {
			return fmt.Errorf("%w: effect[%d] arguments must be valid JSON", ErrProviderProtocol, i)
		}

		seenEvidence := make(map[string]struct{}, len(effect.EvidenceRefs))
		for _, ref := range effect.EvidenceRefs {
			if strings.TrimSpace(ref) == "" {
				return fmt.Errorf("%w: effect[%d] evidence ref cannot be empty", ErrProviderProtocol, i)
			}
			if _, ok := seenEvidence[ref]; ok {
				return fmt.Errorf("%w: effect[%d] duplicate evidence ref %q", ErrProviderProtocol, i, ref)
			}
			seenEvidence[ref] = struct{}{}
		}
	}
	return nil
}

func (p Plan) Clone() Plan {
	out := p
	out.Effects = make([]ProposedEffect, len(p.Effects))
	for i, effect := range p.Effects {
		out.Effects[i] = effect
		out.Effects[i].Arguments = cloneRawMessage(effect.Arguments)
		out.Effects[i].EvidenceRefs = append([]string(nil), effect.EvidenceRefs...)
	}
	return out
}
