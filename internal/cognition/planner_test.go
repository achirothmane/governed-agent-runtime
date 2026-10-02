package cognition

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"github.com/achirothmane/governed-agent-runtime/internal/agent"
)

func testPlanningRequest(t *testing.T) PlanningRequest {
	t.Helper()

	a, err := agent.New(
		"release-engineer",
		"Keep the release pipeline healthy",
		"policy://release-engineer/v1",
		"budget://release-engineer/v1",
		[]agent.ToolRef{
			"github.read_ci",
			"github.create_branch",
			"github.open_pr",
		},
	)
	if err != nil {
		t.Fatal(err)
	}

	request, err := NewPlanningRequest(
		a,
		"evt-ci-failed",
		"github.workflow_failed",
		json.RawMessage(`{"run_id":42}`),
	)
	if err != nil {
		t.Fatal(err)
	}
	return request
}

func validPlan(request PlanningRequest) Plan {
	return Plan{
		ID:          "plan-1",
		AgentID:     request.AgentID,
		EventID:     request.EventID,
		ProviderRef: "fake://deterministic/v1",
		Summary:     "Inspect CI and prepare a repair pull request.",
		Effects: []ProposedEffect{
			{
				ID:           "effect-1",
				Tool:         "github.read_ci",
				Action:       "inspect_failure",
				Arguments:    json.RawMessage(`{"run_id":42}`),
				EvidenceRefs: []string{"event:evt-ci-failed"},
			},
			{
				ID:        "effect-2",
				Tool:      "github.open_pr",
				Action:    "propose_repair",
				Arguments: json.RawMessage(`{"base":"main"}`),
			},
		},
	}
}

func TestDeterministicProviderReturnsStablePlanAndRecordsSnapshot(t *testing.T) {
	request := testPlanningRequest(t)
	provider := NewDeterministicProvider(validPlan(request))
	planner := Planner{Provider: provider}

	first, err := planner.Generate(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	second, err := planner.Generate(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}

	if !reflect.DeepEqual(first, second) {
		t.Fatalf("deterministic provider returned different plans:\nfirst=%+v\nsecond=%+v", first, second)
	}

	request.EventPayload[0] = '['
	request.AllowedTools[0] = "mutated.tool"

	calls := provider.Requests()
	if len(calls) != 2 {
		t.Fatalf("provider calls = %d, want 2", len(calls))
	}
	if string(calls[0].EventPayload) != `{"run_id":42}` {
		t.Fatalf("provider request payload was mutated: %s", calls[0].EventPayload)
	}
	if calls[0].AllowedTools[0] != "github.read_ci" {
		t.Fatalf("provider request tool was mutated: %s", calls[0].AllowedTools[0])
	}
}

func TestPlannerRejectsCrossEventPlanReplay(t *testing.T) {
	request := testPlanningRequest(t)
	plan := validPlan(request)
	plan.EventID = "different-event"

	_, err := (Planner{Provider: NewDeterministicProvider(plan)}).Generate(context.Background(), request)
	if !errors.Is(err, ErrProviderProtocol) {
		t.Fatalf("cross-event plan error = %v, want ErrProviderProtocol", err)
	}
}

func TestPlannerRejectsCrossAgentPlanReplay(t *testing.T) {
	request := testPlanningRequest(t)
	plan := validPlan(request)
	plan.AgentID = "different-agent"

	_, err := (Planner{Provider: NewDeterministicProvider(plan)}).Generate(context.Background(), request)
	if !errors.Is(err, ErrProviderProtocol) {
		t.Fatalf("cross-agent plan error = %v, want ErrProviderProtocol", err)
	}
}

func TestPlannerRejectsToolOutsideDeclaredCapability(t *testing.T) {
	request := testPlanningRequest(t)
	plan := validPlan(request)
	plan.Effects = append(plan.Effects, ProposedEffect{
		ID:        "effect-3",
		Tool:      "github.merge_pr",
		Action:    "merge",
		Arguments: json.RawMessage(`{"number":99}`),
	})

	_, err := (Planner{Provider: NewDeterministicProvider(plan)}).Generate(context.Background(), request)
	if !errors.Is(err, ErrToolOutsideAgentCapability) {
		t.Fatalf("outside-capability plan error = %v, want ErrToolOutsideAgentCapability", err)
	}
}

func TestPlannerRejectsMalformedArguments(t *testing.T) {
	request := testPlanningRequest(t)
	plan := validPlan(request)
	plan.Effects[0].Arguments = json.RawMessage(`{"run_id":`)

	_, err := (Planner{Provider: NewDeterministicProvider(plan)}).Generate(context.Background(), request)
	if !errors.Is(err, ErrProviderProtocol) {
		t.Fatalf("malformed plan error = %v, want ErrProviderProtocol", err)
	}
}

func TestProviderFailureCannotProducePlan(t *testing.T) {
	request := testPlanningRequest(t)
	expected := errors.New("provider unavailable")

	_, err := (Planner{Provider: NewFailingDeterministicProvider(expected)}).Generate(context.Background(), request)
	if !errors.Is(err, expected) {
		t.Fatalf("provider failure error = %v, want wrapped provider error", err)
	}
}

func TestReturnedPlanIsDetachedFromProviderState(t *testing.T) {
	request := testPlanningRequest(t)
	provider := NewDeterministicProvider(validPlan(request))
	planner := Planner{Provider: provider}

	first, err := planner.Generate(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	first.Effects[0].Arguments[0] = '['
	first.Effects[0].EvidenceRefs[0] = "mutated"

	second, err := planner.Generate(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if string(second.Effects[0].Arguments) != `{"run_id":42}` {
		t.Fatalf("provider plan arguments were mutated: %s", second.Effects[0].Arguments)
	}
	if second.Effects[0].EvidenceRefs[0] != "event:evt-ci-failed" {
		t.Fatalf("provider plan evidence refs were mutated: %s", second.Effects[0].EvidenceRefs[0])
	}
}
