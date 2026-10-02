package agent

import "testing"

func mustAgent(t *testing.T) Agent {
	t.Helper()

	a, err := New(
		"release-engineer",
		"Keep the release pipeline healthy",
		"policy://release-engineer/v1",
		"budget://release-engineer/v1",
		[]ToolRef{"github.read_ci", "github.open_pr"},
	)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	return a
}

func TestHappyPathLifecycle(t *testing.T) {
	a := mustAgent(t)

	path := []State{
		StateSleeping,
		StateWaking,
		StatePlanning,
		StateWaitingForAdmission,
		StateExecuting,
		StateVerifying,
		StateSleeping,
	}

	var err error
	for _, next := range path {
		a, err = a.Transition(next)
		if err != nil {
			t.Fatalf("Transition(%s) error = %v", next, err)
		}
	}

	if a.State != StateSleeping {
		t.Fatalf("final state = %s, want %s", a.State, StateSleeping)
	}
}

func TestCannotSkipAdmission(t *testing.T) {
	a := mustAgent(t)

	var err error
	a, err = a.Transition(StateSleeping)
	if err != nil {
		t.Fatal(err)
	}
	a, err = a.Transition(StateWaking)
	if err != nil {
		t.Fatal(err)
	}
	a, err = a.Transition(StatePlanning)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := a.Transition(StateExecuting); err == nil {
		t.Fatal("expected direct PLANNING -> EXECUTING transition to be rejected")
	}
}

func TestUnknownRequiresVerificationBeforeSleeping(t *testing.T) {
	a := mustAgent(t)
	path := []State{
		StateSleeping,
		StateWaking,
		StatePlanning,
		StateWaitingForAdmission,
		StateExecuting,
		StateUnknown,
	}

	var err error
	for _, next := range path {
		a, err = a.Transition(next)
		if err != nil {
			t.Fatal(err)
		}
	}

	if _, err := a.Transition(StateSleeping); err == nil {
		t.Fatal("expected UNKNOWN -> SLEEPING transition to be rejected")
	}

	a, err = a.Transition(StateVerifying)
	if err != nil {
		t.Fatalf("UNKNOWN -> VERIFYING error = %v", err)
	}
	a, err = a.Transition(StateSleeping)
	if err != nil {
		t.Fatalf("VERIFYING -> SLEEPING error = %v", err)
	}
}

func TestBlockedAndRevokedAreFailClosed(t *testing.T) {
	tests := []struct {
		name string
		path []State
	}{
		{
			name: "blocked",
			path: []State{
				StateSleeping,
				StateWaking,
				StatePlanning,
				StateBlocked,
			},
		},
		{
			name: "revoked",
			path: []State{
				StateSleeping,
				StateWaking,
				StatePlanning,
				StateWaitingForAdmission,
				StateRevoked,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a := mustAgent(t)
			var err error
			for _, next := range tt.path {
				a, err = a.Transition(next)
				if err != nil {
					t.Fatal(err)
				}
			}

			if _, err := a.Transition(StateSleeping); err == nil {
				t.Fatalf("expected terminal state %s to reject automatic resume", a.State)
			}
		})
	}
}

func TestAgentValidation(t *testing.T) {
	tests := []struct {
		name      string
		id        AgentID
		mission   string
		policyRef string
		tools     []ToolRef
	}{
		{name: "missing id", mission: "m", policyRef: "p"},
		{name: "missing mission", id: "a", policyRef: "p"},
		{name: "missing policy", id: "a", mission: "m"},
		{name: "empty tool", id: "a", mission: "m", policyRef: "p", tools: []ToolRef{""}},
		{name: "duplicate tool", id: "a", mission: "m", policyRef: "p", tools: []ToolRef{"x", "x"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := New(tt.id, tt.mission, tt.policyRef, "", tt.tools); err == nil {
				t.Fatal("expected validation error")
			}
		})
	}
}
