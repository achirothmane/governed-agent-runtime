package runtime

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/achirothmane/governed-agent-runtime/internal/agent"
)

func postgresTestEvent(id string, at time.Time) Event {
	return Event{
		ID:        id,
		AgentID:   agent.AgentID("release-engineer"),
		Kind:      "github.workflow_failed",
		Payload:   json.RawMessage(`{"run_id":42}`),
		CreatedAt: at,
	}
}

func newPostgresTestStore(t *testing.T) *PostgresStore {
	t.Helper()

	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL is not set")
	}

	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatalf("sql.Open() error = %v", err)
	}
	t.Cleanup(func() {
		_ = db.Close()
	})

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := db.PingContext(ctx); err != nil {
		t.Fatalf("postgres ping error = %v", err)
	}

	store, err := NewPostgresStore(db)
	if err != nil {
		t.Fatalf("NewPostgresStore() error = %v", err)
	}
	if err := store.Migrate(ctx); err != nil {
		t.Fatalf("Migrate() error = %v", err)
	}
	if _, err := db.ExecContext(ctx, `TRUNCATE TABLE agent_runtime_work RESTART IDENTITY`); err != nil {
		t.Fatalf("truncate runtime store: %v", err)
	}

	return store
}

func TestPostgresStoreDuplicateDeliveryIsIdempotentAndConflictFailsClosed(t *testing.T) {
	ctx := context.Background()
	store := newPostgresTestStore(t)
	t0 := time.Date(2026, 10, 2, 19, 50, 0, 123456789, time.UTC)
	event := postgresTestEvent("pg-dup", t0)

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
	if !second.Event.CreatedAt.Equal(t0) {
		t.Fatalf("event timestamp lost precision: got %s want %s", second.Event.CreatedAt, t0)
	}

	conflicting := event
	conflicting.Kind = "github.pull_request"
	if _, err := store.Enqueue(ctx, conflicting); !errors.Is(err, ErrEventConflict) {
		t.Fatalf("conflicting duplicate error = %v, want ErrEventConflict", err)
	}
}

func TestPostgresStoreConcurrentClaimHasSingleOwner(t *testing.T) {
	ctx := context.Background()
	store := newPostgresTestStore(t)
	t0 := time.Date(2026, 10, 2, 19, 50, 0, 0, time.UTC)

	if _, err := store.Enqueue(ctx, postgresTestEvent("pg-claim", t0)); err != nil {
		t.Fatal(err)
	}

	const workers = 8
	start := make(chan struct{})
	results := make(chan error, workers)
	var wg sync.WaitGroup

	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			_, err := store.Claim(ctx, fmt.Sprintf("worker-%d", i), t0, 30*time.Second)
			results <- err
		}(i)
	}

	close(start)
	wg.Wait()
	close(results)

	var claimed, noWork int
	for err := range results {
		switch {
		case err == nil:
			claimed++
		case errors.Is(err, ErrNoWork):
			noWork++
		default:
			t.Fatalf("unexpected claim error: %v", err)
		}
	}

	if claimed != 1 || noWork != workers-1 {
		t.Fatalf("claim results: claimed=%d noWork=%d", claimed, noWork)
	}

	record, err := store.Get(ctx, "pg-claim")
	if err != nil {
		t.Fatal(err)
	}
	if record.State != WorkLeased || record.LeaseEpoch != 1 || record.LeaseOwner == "" {
		t.Fatalf("claimed record = %+v", record)
	}
}

func TestPostgresStoreTakeoverFromExecutingBecomesUnknownAndFencesOldOwner(t *testing.T) {
	ctx := context.Background()
	store := newPostgresTestStore(t)
	t0 := time.Date(2026, 10, 2, 19, 50, 0, 0, time.UTC)

	if _, err := store.Enqueue(ctx, postgresTestEvent("pg-takeover", t0)); err != nil {
		t.Fatal(err)
	}
	claimA, err := store.Claim(ctx, "worker-a", t0, 10*time.Second)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := store.Transition(ctx, claimA.Lease, agent.StateWaking, t0.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Transition(ctx, claimA.Lease, agent.StatePlanning, t0.Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := store.BindPlan(ctx, claimA.Lease, testPlanBinding("pg-takeover"), t0.Add(3*time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Transition(ctx, claimA.Lease, agent.StateWaitingForAdmission, t0.Add(4*time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := beginTestExecution(t, store, claimA.Lease, t0.Add(5*time.Second)); err != nil {
		t.Fatal(err)
	}

	claimB, err := store.Claim(ctx, "worker-b", t0.Add(10*time.Second), 10*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if claimB.Lease.Epoch != 2 {
		t.Fatalf("takeover epoch = %d, want 2", claimB.Lease.Epoch)
	}
	if claimB.Record.LifecycleState != agent.StateUnknown {
		t.Fatalf("takeover lifecycle state = %s, want UNKNOWN", claimB.Record.LifecycleState)
	}
	if claimB.Record.Plan == nil || claimB.Record.Plan.PlanID != "plan-1" {
		t.Fatalf("takeover lost plan binding: %+v", claimB.Record.Plan)
	}

	if _, err := store.Transition(ctx, claimA.Lease, agent.StateVerifying, t0.Add(11*time.Second)); !errors.Is(err, ErrLeaseLost) {
		t.Fatalf("stale owner transition error = %v, want ErrLeaseLost", err)
	}

	if _, err := store.Transition(ctx, claimB.Lease, agent.StateVerifying, t0.Add(11*time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Transition(ctx, claimB.Lease, agent.StateSleeping, t0.Add(12*time.Second)); err != nil {
		t.Fatal(err)
	}
	done, err := store.Complete(ctx, claimB.Lease, t0.Add(13*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if done.State != WorkDone || done.LeaseOwner != "worker-b" || done.LeaseEpoch != 2 {
		t.Fatalf("done record = %+v", done)
	}
}

func TestPostgresStoreRestartPreservesLeaseAndRenewal(t *testing.T) {
	ctx := context.Background()
	store := newPostgresTestStore(t)
	t0 := time.Date(2026, 10, 2, 19, 50, 0, 0, time.UTC)

	if _, err := store.Enqueue(ctx, postgresTestEvent("pg-restart", t0)); err != nil {
		t.Fatal(err)
	}
	claim, err := store.Claim(ctx, "worker-a", t0, 10*time.Second)
	if err != nil {
		t.Fatal(err)
	}

	reopened, err := NewPostgresStore(store.db)
	if err != nil {
		t.Fatal(err)
	}
	persisted, err := reopened.Get(ctx, "pg-restart")
	if err != nil {
		t.Fatal(err)
	}
	if persisted.LeaseOwner != "worker-a" || persisted.LeaseEpoch != 1 {
		t.Fatalf("persisted lease = %+v", persisted)
	}

	renewed, err := reopened.Renew(ctx, claim.Lease, t0.Add(8*time.Second), 10*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if !renewed.ExpiresAt.Equal(t0.Add(18 * time.Second)) {
		t.Fatalf("renewed expiry = %s", renewed.ExpiresAt)
	}
	if _, err := reopened.Claim(ctx, "worker-b", t0.Add(12*time.Second), 10*time.Second); !errors.Is(err, ErrNoWork) {
		t.Fatalf("premature takeover error = %v, want ErrNoWork", err)
	}
}
