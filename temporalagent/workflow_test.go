package temporalagent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/testsuite"

	"github.com/achirothmane/governed-agent-runtime/agentloop"
	"github.com/achirothmane/governed-agent-runtime/temporaltools"
)

func TestWorkflowCarriesOnlyCompactReasoningAndObservationReferences(t *testing.T) {
	run := durableRun()
	execution, err := executionRef(run, "Inspect evidence.", 4)
	if err != nil {
		t.Fatal(err)
	}
	decision := agentloop.Decision{
		Kind: agentloop.DecisionTool,
		Tool: "data.profile",
		Arguments: map[string]any{
			"rows": []any{map[string]any{"secret": "must-not-enter-agent-history"}},
		},
	}
	invocation, _, err := toolRef(run, 1, decision)
	if err != nil {
		t.Fatal(err)
	}
	decisionDigest, err := DecisionDigest(decision)
	if err != nil {
		t.Fatal(err)
	}
	observation := ObservationRef{
		Step:         1,
		InvocationID: invocation.InvocationID,
		ResultDigest: strings.Repeat("b", 64),
	}

	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	env.RegisterActivityWithOptions(
		func(_ context.Context, req ReasoningRequest) (ReasoningResult, error) {
			if req.Step == 1 {
				return ReasoningResult{
					Step:           1,
					Kind:           agentloop.DecisionTool,
					DecisionDigest: decisionDigest,
					Invocation:     &invocation,
				}, nil
			}
			if len(req.Observations) != 1 || req.Observations[0] != observation {
				t.Fatalf("second reasoning request observations=%#v", req.Observations)
			}
			finish := agentloop.Decision{Kind: agentloop.DecisionFinish, Message: "finished"}
			digest, _ := DecisionDigest(finish)
			return ReasoningResult{
				Step:           2,
				Kind:           agentloop.DecisionFinish,
				Message:        "finished",
				DecisionDigest: digest,
			}, nil
		},
		activity.RegisterOptions{Name: ReasonActivityName},
	)
	env.RegisterActivityWithOptions(
		func(_ context.Context, ref temporaltools.InvocationRef, step int) (ObservationRef, error) {
			if ref.InvocationID != invocation.InvocationID || step != 1 {
				t.Fatalf("dispatch ref=%#v step=%d", ref, step)
			}
			return observation, nil
		},
		activity.RegisterOptions{Name: ToolDispatchActivityName},
	)
	env.ExecuteWorkflow(Workflow, WorkflowInput{Execution: execution})
	if err := env.GetWorkflowError(); err != nil {
		t.Fatal(err)
	}
	var output WorkflowResult
	if err := env.GetWorkflowResult(&output); err != nil {
		t.Fatal(err)
	}
	if output.Kind != agentloop.OutcomeFinished || output.Steps != 2 || len(output.Observations) != 1 {
		t.Fatalf("output=%#v", output)
	}
	payload, err := json.Marshal(output)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(payload), "must-not-enter-agent-history") {
		t.Fatalf("workflow result leaked raw decision arguments: %s", payload)
	}
}

func TestWorkflowStopsAtDurableMaxSteps(t *testing.T) {
	run := durableRun()
	execution, err := executionRef(run, "Inspect evidence.", 2)
	if err != nil {
		t.Fatal(err)
	}
	decision := agentloop.Decision{Kind: agentloop.DecisionTool, Tool: "data.profile", Arguments: map[string]any{}}
	invocation1, _, _ := toolRef(run, 1, decision)
	invocation2, _, _ := toolRef(run, 2, decision)
	digest, _ := DecisionDigest(decision)

	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	env.RegisterActivityWithOptions(
		func(_ context.Context, req ReasoningRequest) (ReasoningResult, error) {
			invocation := invocation1
			if req.Step == 2 {
				invocation = invocation2
			}
			return ReasoningResult{
				Step: req.Step, Kind: agentloop.DecisionTool, DecisionDigest: digest, Invocation: &invocation,
			}, nil
		},
		activity.RegisterOptions{Name: ReasonActivityName},
	)
	env.RegisterActivityWithOptions(
		func(_ context.Context, ref temporaltools.InvocationRef, step int) (ObservationRef, error) {
			return ObservationRef{Step: step, InvocationID: ref.InvocationID, ResultDigest: strings.Repeat("c", 64)}, nil
		},
		activity.RegisterOptions{Name: ToolDispatchActivityName},
	)
	env.ExecuteWorkflow(Workflow, WorkflowInput{Execution: execution})
	if err := env.GetWorkflowError(); err != nil {
		t.Fatal(err)
	}
	var output WorkflowResult
	if err := env.GetWorkflowResult(&output); err != nil {
		t.Fatal(err)
	}
	if output.Kind != agentloop.OutcomeMaxSteps || output.Steps != 2 || len(output.Observations) != 2 {
		t.Fatalf("output=%#v", output)
	}
}
