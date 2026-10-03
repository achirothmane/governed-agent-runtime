package runtime

import (
	"context"
	"crypto/sha256"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/achirothmane/governed-agent-runtime/internal/agent"
)

func testPlanBinding(eventID string) PlanBinding {
	document := []byte(`{"id":"plan-1","agent_id":"release-engineer","event_id":"` + eventID + `"}`)
	sum := sha256.Sum256(document)
	return PlanBinding{
		PlanID:   "plan-1",
		AgentID:  agent.AgentID("release-engineer"),
		EventID:  eventID,
		Digest:   append([]byte(nil), sum[:]...),
		Document: document,
	}
}

func TestLifecycleTransitionRequiresCurrentLease(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "runtime.json")
	t0 := time.Date(2026, 10, 2, 19, 30, 0, 0, time.UTC)
	store := newTestStore(t, path)

	if _, err := store.Enqueue(ctx, testEvent("evt-lifecycle", t0)); err != nil {
		t.Fatal(err)
	}
	claim, err := store.Claim(ctx, "worker-a", t0, 10*time.Second)
	if err != nil {
		t.Fatal(err)
	}

	waking, err := store.Transition(ctx, claim.Lease, agent.StateWaking, t0.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if waking.LifecycleState != agent.StateWaking || waking.LifecycleVersion != 2 {
		t.Fatalf("waking record = %+v", waking)
	}

	wrongOwner := claim.Lease
	wrongOwner.WorkerID = "worker-b"
	if _, err := store.Transition(ctx, wrongOwner, agent.StatePlanning, t0.Add(2*time.Second)); !errors.Is(err, ErrLeaseLost) {
		t.Fatalf("wrong owner transition error = %v, want ErrLeaseLost", err)
	}

	if _, err := store.Transition(ctx, claim.Lease, agent.StatePlanning, t0.Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Transition(ctx, claim.Lease, agent.StateWaitingForAdmission, t0.Add(3*time.Second)); !errors.Is(err, ErrPlanRequired) {
		t.Fatalf("unbound plan admission error = %v, want ErrPlanRequired", err)
	}
	if _, err := store.Transition(ctx, claim.Lease, agent.StateExecuting, t0.Add(3*time.Second)); !errors.Is(err, ErrInvalidLifecycleTransition) {
		t.Fatalf("skipped admission transition error = %v, want ErrInvalidLifecycleTransition", err)
	}
}

func TestTakeoverFromExecutingBecomesUnknownAndFencesOldWorker(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "runtime.json")
	t0 := time.Date(2026, 10, 2, 19, 30, 0, 0, time.UTC)
	storeA := newTestStore(t, path)

	if _, err := storeA.Enqueue(ctx, testEvent("evt-effect", t0)); err != nil {
		t.Fatal(err)
	}
	claimA, err := storeA.Claim(ctx, "worker-a", t0, 10*time.Second)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := storeA.Transition(ctx, claimA.Lease, agent.StateWaking, t0.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := storeA.Transition(ctx, claimA.Lease, agent.StatePlanning, t0.Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := storeA.BindPlan(ctx, claimA.Lease, testPlanBinding("evt-effect"), t0.Add(3*time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := storeA.Transition(ctx, claimA.Lease, agent.StateWaitingForAdmission, t0.Add(4*time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := beginTestExecution(t, storeA, claimA.Lease, t0.Add(5*time.Second)); err != nil {
		t.Fatal(err)
	}

	if _, err := storeA.Complete(ctx, claimA.Lease, t0.Add(6*time.Second)); !errors.Is(err, ErrLifecycleIncomplete) {
		t.Fatalf("executing work completed without verification: %v", err)
	}

	storeB := newTestStore(t, path)
	claimB, err := storeB.Claim(ctx, "worker-b", t0.Add(10*time.Second), 10*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if claimB.Lease.Epoch != 2 {
		t.Fatalf("takeover epoch = %d, want 2", claimB.Lease.Epoch)
	}
	if claimB.Record.LifecycleState != agent.StateUnknown {
		t.Fatalf("takeover lifecycle state = %s, want UNKNOWN", claimB.Record.LifecycleState)
	}
	if claimB.Record.LifecycleVersion != 6 {
		t.Fatalf("takeover lifecycle version = %d, want 6", claimB.Record.LifecycleVersion)
	}
	if claimB.Record.Plan == nil || claimB.Record.Plan.PlanID != "plan-1" {
		t.Fatalf("takeover lost plan binding: %+v", claimB.Record.Plan)
	}

	if _, err := storeA.Transition(ctx, claimA.Lease, agent.StateVerifying, t0.Add(11*time.Second)); !errors.Is(err, ErrLeaseLost) {
		t.Fatalf("stale worker transition error = %v, want ErrLeaseLost", err)
	}

	if _, err := storeB.Transition(ctx, claimB.Lease, agent.StateVerifying, t0.Add(11*time.Second)); err != nil {
		t.Fatalf("new owner UNKNOWN -> VERIFYING: %v", err)
	}
	if _, err := storeB.Transition(ctx, claimB.Lease, agent.StateSleeping, t0.Add(12*time.Second)); err != nil {
		t.Fatalf("new owner VERIFYING -> SLEEPING: %v", err)
	}
	done, err := storeB.Complete(ctx, claimB.Lease, t0.Add(13*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if done.State != WorkDone || done.LifecycleState != agent.StateSleeping {
		t.Fatalf("done record = %+v", done)
	}
}

func TestExpiredLeaseCannotAdvanceLifecycleWithoutTakeover(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "runtime.json")
	t0 := time.Date(2026, 10, 2, 19, 30, 0, 0, time.UTC)
	store := newTestStore(t, path)

	if _, err := store.Enqueue(ctx, testEvent("evt-expired", t0)); err != nil {
		t.Fatal(err)
	}
	claim, err := store.Claim(ctx, "worker-a", t0, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := store.Transition(ctx, claim.Lease, agent.StateWaking, t0.Add(5*time.Second)); !errors.Is(err, ErrLeaseLost) {
		t.Fatalf("expired lease transition error = %v, want ErrLeaseLost", err)
	}
}

