package runtime

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/achirothmane/governed-agent-runtime/internal/agent"
	"github.com/achirothmane/governed-agent-runtime/internal/cognition"
)

func cognitionPlan(eventID, planID string) cognition.Plan {
	return cognition.Plan{
		ID:          planID,
		AgentID:     agent.AgentID("release-engineer"),
		EventID:     eventID,
		ProviderRef: "fake://deterministic/v1",
		Summary:     "Inspect CI and prepare a repair.",
		Effects: []cognition.ProposedEffect{
			{
				ID:        "effect-1",
				Tool:      agent.ToolRef("github.read_ci"),
				Action:    "inspect_failure",
				Arguments: json.RawMessage(`{"run_id":42}`),
			},
		},
	}
}

func TestNewPlanBindingHashesExactPlanDocument(t *testing.T) {
	plan := cognitionPlan("evt-binding", "plan-1")
	binding, err := NewPlanBinding(plan)
	if err != nil {
		t.Fatal(err)
	}

	document, err := json.Marshal(plan)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(document)

	if !bytes.Equal(binding.Document, document) {
		t.Fatalf("bound document differs from marshaled plan: got=%s want=%s", binding.Document, document)
	}
	if !bytes.Equal(binding.Digest, sum[:]) {
		t.Fatalf("bound digest differs from plan digest: got=%x want=%x", binding.Digest, sum)
	}
	if err := binding.Validate(); err != nil {
		t.Fatalf("binding validation error = %v", err)
	}
}

func TestFileStoreRequiresAndFreezesPlanBeforeAdmission(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "runtime.json")
	t0 := time.Date(2026, 10, 2, 20, 20, 0, 0, time.UTC)
	store := newTestStore(t, path)

	if _, err := store.Enqueue(ctx, testEvent("evt-binding", t0)); err != nil {
		t.Fatal(err)
	}
	claim, err := store.Claim(ctx, "worker-a", t0, 30*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Transition(ctx, claim.Lease, agent.StateWaking, t0.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Transition(ctx, claim.Lease, agent.StatePlanning, t0.Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}

	if _, err := store.Transition(ctx, claim.Lease, agent.StateWaitingForAdmission, t0.Add(3*time.Second)); !errors.Is(err, ErrPlanRequired) {
		t.Fatalf("admission without plan error = %v, want ErrPlanRequired", err)
	}

	binding, err := NewPlanBinding(cognitionPlan("evt-binding", "plan-1"))
	if err != nil {
		t.Fatal(err)
	}
	bound, err := store.BindPlan(ctx, claim.Lease, binding, t0.Add(4*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if bound.Plan == nil || bound.Plan.PlanID != "plan-1" || bound.PlanBoundAt == nil {
		t.Fatalf("bound record = %+v", bound)
	}

	firstBoundAt := *bound.PlanBoundAt
	again, err := store.BindPlan(ctx, claim.Lease, binding, t0.Add(5*time.Second))
	if err != nil {
		t.Fatalf("idempotent bind error = %v", err)
	}
	if again.PlanBoundAt == nil || !again.PlanBoundAt.Equal(firstBoundAt) {
		t.Fatalf("idempotent bind changed bound time: first=%s again=%v", firstBoundAt, again.PlanBoundAt)
	}

	binding.Document[0] = '['
	persisted, err := store.Get(ctx, "evt-binding")
	if err != nil {
		t.Fatal(err)
	}
	if persisted.Plan == nil || !json.Valid(persisted.Plan.Document) {
		t.Fatalf("caller mutation corrupted durable plan: %+v", persisted.Plan)
	}

	conflict, err := NewPlanBinding(cognitionPlan("evt-binding", "plan-2"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.BindPlan(ctx, claim.Lease, conflict, t0.Add(6*time.Second)); !errors.Is(err, ErrPlanConflict) {
		t.Fatalf("different plan bind error = %v, want ErrPlanConflict", err)
	}

	if _, err := store.Transition(ctx, claim.Lease, agent.StateWaitingForAdmission, t0.Add(7*time.Second)); err != nil {
		t.Fatalf("admission after durable binding error = %v", err)
	}
}

func TestPlanBindingRejectsDigestMismatchAndCrossEvent(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "runtime.json")
	t0 := time.Date(2026, 10, 2, 20, 20, 0, 0, time.UTC)
	store := newTestStore(t, path)

	if _, err := store.Enqueue(ctx, testEvent("evt-identity", t0)); err != nil {
		t.Fatal(err)
	}
	claim, err := store.Claim(ctx, "worker-a", t0, 30*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Transition(ctx, claim.Lease, agent.StateWaking, t0.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Transition(ctx, claim.Lease, agent.StatePlanning, t0.Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}

	badDigest, err := NewPlanBinding(cognitionPlan("evt-identity", "plan-bad-digest"))
	if err != nil {
		t.Fatal(err)
	}
	badDigest.Digest[0] ^= 0xff
	if _, err := store.BindPlan(ctx, claim.Lease, badDigest, t0.Add(3*time.Second)); !errors.Is(err, ErrPlanBindingInvalid) {
		t.Fatalf("digest mismatch error = %v, want ErrPlanBindingInvalid", err)
	}

	wrongEvent, err := NewPlanBinding(cognitionPlan("other-event", "plan-wrong-event"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.BindPlan(ctx, claim.Lease, wrongEvent, t0.Add(4*time.Second)); !errors.Is(err, ErrPlanBindingInvalid) {
		t.Fatalf("cross-event binding error = %v, want ErrPlanBindingInvalid", err)
	}
}
