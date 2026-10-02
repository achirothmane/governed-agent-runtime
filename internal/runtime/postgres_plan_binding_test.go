package runtime

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/achirothmane/governed-agent-runtime/internal/agent"
)

func TestPostgresPlanBindingSurvivesRestartAndBlocksReplacement(t *testing.T) {
	ctx := context.Background()
	store := newPostgresTestStore(t)
	t0 := time.Date(2026, 10, 2, 20, 20, 0, 0, time.UTC)

	if _, err := store.Enqueue(ctx, postgresTestEvent("pg-plan-binding", t0)); err != nil {
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

	binding, err := NewPlanBinding(cognitionPlan("pg-plan-binding", "plan-pg-1"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.BindPlan(ctx, claim.Lease, binding, t0.Add(3*time.Second)); err != nil {
		t.Fatal(err)
	}

	reopened, err := NewPostgresStore(store.db)
	if err != nil {
		t.Fatal(err)
	}
	persisted, err := reopened.Get(ctx, "pg-plan-binding")
	if err != nil {
		t.Fatal(err)
	}
	if persisted.Plan == nil || persisted.Plan.PlanID != "plan-pg-1" || persisted.PlanBoundAt == nil {
		t.Fatalf("persisted plan binding = %+v", persisted)
	}

	conflict, err := NewPlanBinding(cognitionPlan("pg-plan-binding", "plan-pg-2"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reopened.BindPlan(ctx, claim.Lease, conflict, t0.Add(4*time.Second)); !errors.Is(err, ErrPlanConflict) {
		t.Fatalf("replacement error = %v, want ErrPlanConflict", err)
	}

	if _, err := reopened.Transition(ctx, claim.Lease, agent.StateWaitingForAdmission, t0.Add(5*time.Second)); err != nil {
		t.Fatalf("admission with persisted plan error = %v", err)
	}
}
