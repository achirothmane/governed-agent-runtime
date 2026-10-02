package agent

import (
	"errors"
	"fmt"
	"strings"
)

type AgentID string

type ToolRef string

type State string

const (
	StateCreated             State = "CREATED"
	StateSleeping            State = "SLEEPING"
	StateWaking              State = "WAKING"
	StatePlanning            State = "PLANNING"
	StateWaitingForAdmission State = "WAITING_FOR_ADMISSION"
	StateExecuting           State = "EXECUTING"
	StateVerifying           State = "VERIFYING"
	StateBlocked             State = "BLOCKED"
	StateRevoked             State = "REVOKED"
	StateUnknown             State = "UNKNOWN"
)

var validStates = map[State]struct{}{
	StateCreated:             {},
	StateSleeping:            {},
	StateWaking:              {},
	StatePlanning:            {},
	StateWaitingForAdmission: {},
	StateExecuting:           {},
	StateVerifying:           {},
	StateBlocked:             {},
	StateRevoked:             {},
	StateUnknown:             {},
}

type Agent struct {
	ID           AgentID
	Mission      string
	State        State
	AllowedTools []ToolRef
	PolicyRef    string
	BudgetRef    string
}

func New(id AgentID, mission, policyRef, budgetRef string, tools []ToolRef) (Agent, error) {
	a := Agent{
		ID:           id,
		Mission:      mission,
		State:        StateCreated,
		AllowedTools: append([]ToolRef(nil), tools...),
		PolicyRef:    policyRef,
		BudgetRef:    budgetRef,
	}

	if err := a.Validate(); err != nil {
		return Agent{}, err
	}

	return a, nil
}

func (a Agent) Validate() error {
	if strings.TrimSpace(string(a.ID)) == "" {
		return errors.New("agent id is required")
	}
	if strings.TrimSpace(a.Mission) == "" {
		return errors.New("agent mission is required")
	}
	if _, ok := validStates[a.State]; !ok {
		return fmt.Errorf("invalid agent state %q", a.State)
	}
	if strings.TrimSpace(a.PolicyRef) == "" {
		return errors.New("policy reference is required")
	}

	seen := make(map[ToolRef]struct{}, len(a.AllowedTools))
	for _, tool := range a.AllowedTools {
		if strings.TrimSpace(string(tool)) == "" {
			return errors.New("allowed tool reference cannot be empty")
		}
		if _, exists := seen[tool]; exists {
			return fmt.Errorf("duplicate allowed tool reference %q", tool)
		}
		seen[tool] = struct{}{}
	}

	return nil
}
