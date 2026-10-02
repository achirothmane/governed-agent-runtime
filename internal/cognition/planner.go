package cognition

import (
	"context"
	"errors"
	"fmt"
)

type Planner struct {
	Provider ModelProvider
}

func (p Planner) Generate(ctx context.Context, request PlanningRequest) (Plan, error) {
	if p.Provider == nil {
		return Plan{}, errors.New("model provider is required")
	}
	if err := request.Validate(); err != nil {
		return Plan{}, fmt.Errorf("validate planning request: %w", err)
	}

	snapshot := request.Clone()
	plan, err := p.Provider.GeneratePlan(ctx, snapshot)
	if err != nil {
		return Plan{}, fmt.Errorf("generate plan: %w", err)
	}
	if err := plan.ValidateAgainst(request); err != nil {
		return Plan{}, err
	}
	return plan.Clone(), nil
}
