package cognition

import (
	"context"
	"errors"
	"sync"
)

type DeterministicProvider struct {
	mu       sync.Mutex
	plan     Plan
	err      error
	requests []PlanningRequest
}

var _ ModelProvider = (*DeterministicProvider)(nil)

func NewDeterministicProvider(plan Plan) *DeterministicProvider {
	return &DeterministicProvider{plan: plan.Clone()}
}

func NewFailingDeterministicProvider(err error) *DeterministicProvider {
	if err == nil {
		err = errors.New("deterministic provider failure")
	}
	return &DeterministicProvider{err: err}
}

func (p *DeterministicProvider) GeneratePlan(_ context.Context, request PlanningRequest) (Plan, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.requests = append(p.requests, request.Clone())
	if p.err != nil {
		return Plan{}, p.err
	}
	return p.plan.Clone(), nil
}

func (p *DeterministicProvider) Requests() []PlanningRequest {
	p.mu.Lock()
	defer p.mu.Unlock()

	out := make([]PlanningRequest, len(p.requests))
	for i, request := range p.requests {
		out[i] = request.Clone()
	}
	return out
}
