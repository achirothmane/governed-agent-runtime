package agent

import "fmt"

var allowedTransitions = map[State]map[State]struct{}{
	StateCreated: {
		StateSleeping: {},
	},
	StateSleeping: {
		StateWaking: {},
	},
	StateWaking: {
		StatePlanning: {},
	},
	StatePlanning: {
		StateWaitingForAdmission: {},
		StateBlocked:             {},
	},
	StateWaitingForAdmission: {
		StateExecuting: {},
		StateBlocked:   {},
		StateRevoked:   {},
	},
	StateExecuting: {
		StateVerifying: {},
		StateUnknown:   {},
		StateRevoked:   {},
	},
	StateVerifying: {
		StateSleeping: {},
		StateUnknown:  {},
	},
	StateUnknown: {
		StateVerifying: {},
	},
}

func CanTransition(from, to State) bool {
	next, ok := allowedTransitions[from]
	if !ok {
		return false
	}
	_, ok = next[to]
	return ok
}

func (a Agent) Transition(to State) (Agent, error) {
	if _, ok := validStates[to]; !ok {
		return Agent{}, fmt.Errorf("invalid target state %q", to)
	}
	if !CanTransition(a.State, to) {
		return Agent{}, fmt.Errorf("invalid agent state transition %s -> %s", a.State, to)
	}

	next := a
	next.State = to
	return next, nil
}
