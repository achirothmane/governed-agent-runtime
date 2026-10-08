package runtime

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/achirothmane/governed-agent-runtime/internal/agent"
)

func makeResolution(record WorkRecord, lease LeaseToken, outcome EffectResolutionOutcome, observedAt time.Time) EffectResolution {
	evidence := sha256.Sum256([]byte(string(outcome) + ":" + lease.EventID))
	return EffectResolution{
		EffectID:       "effect-open-pr",
		Outcome:        outcome,
		WorkerID:       lease.WorkerID,
		LeaseEpoch:     lease.Epoch,
		PlanDigest:     hex.EncodeToString(record.Plan.Digest),
		EvidenceDigest: hex.EncodeToString(evidence[:]),
		ObservedAt:     observedAt.UTC(),
	}
}

func setupUnknownWork(t *testing.T, store Store, eventID string) (ClaimedWork, time.Time) {
	t.Helper()
	ctx := context.Background()
	t0 := time.Date(2026, 10, 8, 5, 30, 0, 0, time.UTC)

	if _, err := store.Enqueue(ctx, testEvent(eventID, t0)); err != nil {
		t.Fatal(err)
	}
	first, err := store.Claim(ctx, "worker-a", t0, 10*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Transition(ctx, first.Lease, agent.StateWaking, t0.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Transition(ctx, first.Lease, agent.StatePlanning, t0.Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := store.BindPlan(ctx, first.Lease, testPlanBinding(eventID), t0.Add(3*time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Transition(ctx, first.Lease, agent.StateWaitingForAdmission, t0.Add(4*time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := beginTestExecution(t, store, first.Lease, t0.Add(5*time.Second)); err != nil {
		t.Fatal(err)
	}

	takeoverAt := t0.Add(11 * time.Second)
	second, err := store.Claim(ctx, "worker-b", takeoverAt, 30*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if second.Record.LifecycleState != agent.StateUnknown {
		t.Fatalf("takeover state = %s, want UNKNOWN", second.Record.LifecycleState)
	}
	if second.Lease.Epoch != first.Lease.Epoch+1 {
		t.Fatalf("takeover epoch = %d, want %d", second.Lease.Epoch, first.Lease.Epoch+1)
	}
	return second, takeoverAt
}

func testResolutionStore(t *testing.T, newStore func(*testing.T) Store) {
	t.Helper()

	t.Run("applied once moves UNKNOWN to VERIFYING and persists proof", func(t *testing.T) {
		store := newStore(t)
		claim, at := setupUnknownWork(t, store, "evt-resolution-applied")
		proof := makeResolution(claim.Record, claim.Lease, EffectAppliedOnce, at)

		got, err := store.ResolveUnknown(context.Background(), claim.Lease, proof, at)
		if err != nil {
			t.Fatal(err)
		}
		if got.LifecycleState != agent.StateVerifying {
			t.Fatalf("state = %s, want VERIFYING", got.LifecycleState)
		}
		if got.Resolution == nil || got.Resolution.Outcome != EffectAppliedOnce {
			t.Fatalf("resolution = %+v", got.Resolution)
		}

		persisted, err := store.Get(context.Background(), claim.Lease.EventID)
		if err != nil {
			t.Fatal(err)
		}
		if persisted.Resolution == nil || persisted.Resolution.EvidenceDigest != proof.EvidenceDigest {
			t.Fatalf("persisted resolution = %+v", persisted.Resolution)
		}
	})

	t.Run("proved absence reopens admission for current lease epoch", func(t *testing.T) {
		store := newStore(t)
		claim, at := setupUnknownWork(t, store, "evt-resolution-absent")
		proof := makeResolution(claim.Record, claim.Lease, EffectAbsent, at)

		got, err := store.ResolveUnknown(context.Background(), claim.Lease, proof, at)
		if err != nil {
			t.Fatal(err)
		}
		if got.LifecycleState != agent.StateWaitingForAdmission {
			t.Fatalf("state = %s, want WAITING_FOR_ADMISSION", got.LifecycleState)
		}

		executing, err := beginTestExecution(t, store, claim.Lease, at.Add(time.Second))
		if err != nil {
			t.Fatalf("new-epoch admission rejected: %v", err)
		}
		if executing.LifecycleState != agent.StateExecuting {
			t.Fatalf("state = %s, want EXECUTING", executing.LifecycleState)
		}
	})

	t.Run("stale lease proof is rejected", func(t *testing.T) {
		store := newStore(t)
		claim, at := setupUnknownWork(t, store, "evt-resolution-stale")
		proof := makeResolution(claim.Record, claim.Lease, EffectAbsent, at)
		proof.LeaseEpoch--

		if _, err := store.ResolveUnknown(context.Background(), claim.Lease, proof, at); !errors.Is(err, ErrEffectResolution) {
			t.Fatalf("error = %v, want ErrEffectResolution", err)
		}
		persisted, err := store.Get(context.Background(), claim.Lease.EventID)
		if err != nil {
			t.Fatal(err)
		}
		if persisted.LifecycleState != agent.StateUnknown || persisted.Resolution != nil {
			t.Fatalf("rejected proof mutated work: %+v", persisted)
		}
	})

	t.Run("wrong plan proof is rejected", func(t *testing.T) {
		store := newStore(t)
		claim, at := setupUnknownWork(t, store, "evt-resolution-plan")
		proof := makeResolution(claim.Record, claim.Lease, EffectAbsent, at)
		proof.PlanDigest = hex.EncodeToString(make([]byte, sha256.Size))

		if _, err := store.ResolveUnknown(context.Background(), claim.Lease, proof, at); !errors.Is(err, ErrEffectResolution) {
			t.Fatalf("error = %v, want ErrEffectResolution", err)
		}
	})

	t.Run("future observation is rejected", func(t *testing.T) {
		store := newStore(t)
		claim, at := setupUnknownWork(t, store, "evt-resolution-future")
		proof := makeResolution(claim.Record, claim.Lease, EffectAppliedOnce, at.Add(time.Second))

		if _, err := store.ResolveUnknown(context.Background(), claim.Lease, proof, at); !errors.Is(err, ErrEffectResolution) {
			t.Fatalf("error = %v, want ErrEffectResolution", err)
		}
	})
}

func TestFileStoreEffectResolution(t *testing.T) {
	testResolutionStore(t, func(t *testing.T) Store {
		return newTestStore(t, filepath.Join(t.TempDir(), "runtime.json"))
	})
}

func TestPostgresStoreEffectResolution(t *testing.T) {
	testResolutionStore(t, func(t *testing.T) Store {
		return newPostgresTestStore(t)
	})
}
