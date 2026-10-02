package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/achirothmane/governed-agent-runtime/internal/agent"
)

func testEvent(id string, at time.Time) Event {
	return Event{
		ID:        id,
		AgentID:   agent.AgentID("release-engineer"),
		Kind:      "github.workflow_failed",
		Payload:   json.RawMessage(`{"run_id":42}`),
		CreatedAt: at,
	}
}

func newTestStore(t *testing.T, path string) *FileStore {
	t.Helper()
	store, err := NewFileStore(path)
	if err != nil {
		t.Fatalf("NewFileStore() error = %v", err)
	}
	return store
}

func TestTakeoverAfterRestartRejectsStaleOwner(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "runtime.json")
	t0 := time.Date(2026, 10, 2, 19, 0, 0, 0, time.UTC)

	storeA := newTestStore(t, path)
	if _, err := storeA.Enqueue(ctx, testEvent("evt-1", t0)); err != nil {
		t.Fatal(err)
	}
	claimA, err := storeA.Claim(ctx, "worker-a", t0, 10*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if claimA.Lease.Epoch != 1 {
		t.Fatalf("worker A epoch = %d, want 1", claimA.Lease.Epoch)
	}

	storeAfterRestart := newTestStore(t, path)
	if _, err := storeAfterRestart.Claim(ctx, "worker-b", t0.Add(5*time.Second), 10*time.Second); !errors.Is(err, ErrNoWork) {
		t.Fatalf("claim before expiry error = %v, want ErrNoWork", err)
	}

	claimB, err := storeAfterRestart.Claim(ctx, "worker-b", t0.Add(10*time.Second), 10*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if claimB.Lease.Epoch != 2 {
		t.Fatalf("worker B epoch = %d, want 2", claimB.Lease.Epoch)
	}

	if _, err := storeA.Complete(ctx, claimA.Lease, t0.Add(11*time.Second)); !errors.Is(err, ErrLeaseLost) {
		t.Fatalf("stale worker completion error = %v, want ErrLeaseLost", err)
	}

	completed, err := storeAfterRestart.Complete(ctx, claimB.Lease, t0.Add(11*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if completed.State != WorkDone {
		t.Fatalf("completed state = %s, want %s", completed.State, WorkDone)
	}

	storeAfterSecondRestart := newTestStore(t, path)
	persisted, err := storeAfterSecondRestart.Get(ctx, "evt-1")
	if err != nil {
		t.Fatal(err)
	}
	if persisted.State != WorkDone || persisted.LeaseOwner != "worker-b" || persisted.LeaseEpoch != 2 {
		t.Fatalf("persisted work = %+v", persisted)
	}
	if _, err := storeAfterSecondRestart.Claim(ctx, "worker-c", t0.Add(time.Minute), 10*time.Second); !errors.Is(err, ErrNoWork) {
		t.Fatalf("completed work became claimable: %v", err)
	}
}

func TestDuplicateDeliveryIsIdempotentButConflictFailsClosed(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "runtime.json")
	t0 := time.Date(2026, 10, 2, 19, 0, 0, 0, time.UTC)
	store := newTestStore(t, path)
	event := testEvent("evt-dup", t0)

	first, err := store.Enqueue(ctx, event)
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.Enqueue(ctx, event)
	if err != nil {
		t.Fatal(err)
	}
	if first.Sequence != second.Sequence {
		t.Fatalf("duplicate delivery created new sequence: %d != %d", first.Sequence, second.Sequence)
	}

	conflicting := event
	conflicting.Kind = "github.pull_request"
	if _, err := store.Enqueue(ctx, conflicting); !errors.Is(err, ErrEventConflict) {
		t.Fatalf("conflicting duplicate error = %v, want ErrEventConflict", err)
	}
}

func TestRenewPreventsPrematureTakeover(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "runtime.json")
	t0 := time.Date(2026, 10, 2, 19, 0, 0, 0, time.UTC)
	store := newTestStore(t, path)

	if _, err := store.Enqueue(ctx, testEvent("evt-renew", t0)); err != nil {
		t.Fatal(err)
	}
	claim, err := store.Claim(ctx, "worker-a", t0, 10*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	renewed, err := store.Renew(ctx, claim.Lease, t0.Add(8*time.Second), 10*time.Second)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := store.Claim(ctx, "worker-b", t0.Add(12*time.Second), 10*time.Second); !errors.Is(err, ErrNoWork) {
		t.Fatalf("claim during renewed lease error = %v, want ErrNoWork", err)
	}
	if _, err := store.Complete(ctx, renewed, t0.Add(13*time.Second)); err != nil {
		t.Fatalf("completion with renewed lease error = %v", err)
	}
}

func TestCorruptStateFailsClosed(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "runtime.json")
	if err := os.WriteFile(path, []byte("not-json"), 0o600); err != nil {
		t.Fatal(err)
	}
	store := newTestStore(t, path)

	_, err := store.Claim(ctx, "worker-a", time.Now(), time.Second)
	if err == nil || errors.Is(err, ErrNoWork) {
		t.Fatalf("corrupt durable state must fail closed, got %v", err)
	}
}

func TestLoopFailureLeavesWorkForLeaseExpiryAndTakeover(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "runtime.json")
	t0 := time.Date(2026, 10, 2, 19, 0, 0, 0, time.UTC)
	store := newTestStore(t, path)
	if _, err := store.Enqueue(ctx, testEvent("evt-loop", t0)); err != nil {
		t.Fatal(err)
	}

	now := t0
	loopA := Loop{Store: store, LeaseTTL: 10 * time.Second, Now: func() time.Time { return now }}
	boom := errors.New("worker stopped before durable completion")
	didWork, err := loopA.RunOnce(ctx, "worker-a", HandlerFunc(func(context.Context, ClaimedWork) error {
		return boom
	}))
	if !didWork || !errors.Is(err, boom) {
		t.Fatalf("first worker didWork=%v err=%v", didWork, err)
	}

	now = t0.Add(10 * time.Second)
	storeAfterRestart := newTestStore(t, path)
	loopB := Loop{Store: storeAfterRestart, LeaseTTL: 10 * time.Second, Now: func() time.Time { return now }}
	didWork, err = loopB.RunOnce(ctx, "worker-b", HandlerFunc(func(_ context.Context, work ClaimedWork) error {
		if work.Lease.Epoch != 2 {
			t.Fatalf("takeover epoch = %d, want 2", work.Lease.Epoch)
		}
		return nil
	}))
	if err != nil || !didWork {
		t.Fatalf("takeover didWork=%v err=%v", didWork, err)
	}

	record, err := storeAfterRestart.Get(ctx, "evt-loop")
	if err != nil {
		t.Fatal(err)
	}
	if record.State != WorkDone || record.LeaseOwner != "worker-b" || record.LeaseEpoch != 2 {
		t.Fatalf("takeover record = %+v", record)
	}
}
