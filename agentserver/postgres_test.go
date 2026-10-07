package agentserver

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"sync"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"

	runtimesdk "github.com/achirothmane/governed-agent-runtime/sdk"
)

func openAgentServerPostgres(t *testing.T) (*sql.DB, *PostgresStore) {
	t.Helper()
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL is not set")
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := db.PingContext(context.Background()); err != nil {
		t.Fatal(err)
	}
	store, err := NewPostgresStore(db)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(context.Background(), `
		TRUNCATE agent_server_events, agent_server_runs, agent_server_conversations CASCADE`); err != nil {
		t.Fatal(err)
	}
	return db, store
}

func seedPostgresConversationRun(t *testing.T, store *PostgresStore) (Conversation, RunRecord) {
	t.Helper()
	ctx := context.Background()
	at := time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)
	conversation := Conversation{
		Ref: runtimesdk.ConversationRef{
			ID:          "conv-1",
			AgentID:     "agent-1",
			WorkspaceID: "workspace-1",
		},
		CreatedAt: at,
	}
	storedConversation, created, err := store.PutConversation(ctx, conversation)
	if err != nil {
		t.Fatal(err)
	}
	if !created || storedConversation.Ref != conversation.Ref {
		t.Fatalf("unexpected conversation result: created=%v value=%#v", created, storedConversation)
	}
	handle := runtimesdk.RunHandle{
		ID:          "run-1",
		Backend:     "temporal",
		ExternalID:  "temporal-execution-1",
		Fingerprint: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		State:       runtimesdk.RunQueued,
	}
	record, err := NewRunRecord(conversation, "profile the source", handle, at.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	storedRun, runCreated, err := store.PutRun(ctx, record)
	if err != nil {
		t.Fatal(err)
	}
	if !runCreated || !sameRunBinding(storedRun, record) {
		t.Fatalf("unexpected run result: created=%v value=%#v", runCreated, storedRun)
	}
	return conversation, record
}

func TestPostgresStoreSurvivesStoreRecreationAndReplaysEvents(t *testing.T) {
	db, store := openAgentServerPostgres(t)
	conversation, record := seedPostgresConversationRun(t, store)
	ctx := context.Background()

	for i, typ := range []runtimesdk.EventType{runtimesdk.EventRunStarted, "run.signaled"} {
		event, err := store.AppendEvent(ctx, runtimesdk.EventEnvelope{
			Type:           typ,
			RunID:          record.ID,
			ConversationID: conversation.Ref.ID,
			OccurredAt:     time.Date(2026, 10, 7, 12, 1+i, 0, 0, time.UTC),
		})
		if err != nil {
			t.Fatal(err)
		}
		if event.Sequence != uint64(i+1) {
			t.Fatalf("sequence=%d want=%d", event.Sequence, i+1)
		}
	}

	restarted, err := NewPostgresStore(db)
	if err != nil {
		t.Fatal(err)
	}
	gotConversation, err := restarted.GetConversation(ctx, conversation.Ref.ID)
	if err != nil {
		t.Fatal(err)
	}
	if gotConversation.Ref != conversation.Ref {
		t.Fatalf("conversation after restart=%#v", gotConversation)
	}
	gotRun, err := restarted.GetRun(ctx, record.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !sameRunBinding(gotRun, record) {
		t.Fatalf("run after restart=%#v", gotRun)
	}
	events, err := restarted.ListEvents(ctx, conversation.Ref.ID, 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 || events[0].Sequence != 1 || events[1].Sequence != 2 {
		t.Fatalf("events after restart=%#v", events)
	}
	resumed, err := restarted.ListEvents(ctx, conversation.Ref.ID, 1, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(resumed) != 1 || resumed[0].Sequence != 2 {
		t.Fatalf("resumed events=%#v", resumed)
	}
}

func TestPostgresStoreAllocatesGapFreePerConversationSequencesConcurrently(t *testing.T) {
	_, store := openAgentServerPostgres(t)
	conversation, record := seedPostgresConversationRun(t, store)
	ctx := context.Background()

	const count = 32
	errs := make(chan error, count)
	var wg sync.WaitGroup
	for i := 0; i < count; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, err := store.AppendEvent(ctx, runtimesdk.EventEnvelope{
				Type:           runtimesdk.EventType("test.concurrent"),
				RunID:          record.ID,
				ConversationID: conversation.Ref.ID,
				OccurredAt:     time.Date(2026, 10, 7, 12, 10, i, 0, time.UTC),
			})
			errs <- err
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}

	events, err := store.ListEvents(ctx, conversation.Ref.ID, 0, count+1)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != count {
		t.Fatalf("event count=%d want=%d", len(events), count)
	}
	for i, event := range events {
		want := uint64(i + 1)
		if event.Sequence != want {
			t.Fatalf("events[%d].sequence=%d want=%d", i, event.Sequence, want)
		}
	}
}

func TestPostgresStoreRejectsRunRebinding(t *testing.T) {
	_, store := openAgentServerPostgres(t)
	_, record := seedPostgresConversationRun(t, store)

	changed := record
	changed.Input = "different input"
	changed.InputDigest = InputDigest(changed.Input)
	changed.Handle.Fingerprint = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	_, _, err := store.PutRun(context.Background(), changed)
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("error=%v want conflict", err)
	}
}

func TestPostgresStoreWatchObservesCrossStoreWritesByPolling(t *testing.T) {
	db, storeA := openAgentServerPostgres(t)
	conversation, record := seedPostgresConversationRun(t, storeA)
	storeB, err := NewPostgresStore(db)
	if err != nil {
		t.Fatal(err)
	}
	storeA.pollInterval = 10 * time.Millisecond

	ctx, cancelCtx := context.WithCancel(context.Background())
	defer cancelCtx()
	wake, cancelWatch, err := storeA.Watch(ctx, conversation.Ref.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer cancelWatch()

	if _, err := storeB.AppendEvent(context.Background(), runtimesdk.EventEnvelope{
		Type:           runtimesdk.EventRunStarted,
		RunID:          record.ID,
		ConversationID: conversation.Ref.ID,
		OccurredAt:     time.Date(2026, 10, 7, 12, 30, 0, 0, time.UTC),
	}); err != nil {
		t.Fatal(err)
	}

	select {
	case <-wake:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("watch did not wake after cross-store write")
	}
	events, err := storeA.ListEvents(context.Background(), conversation.Ref.ID, 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].Sequence != 1 {
		t.Fatalf("events=%#v", events)
	}
}

func TestPostgresStoreRejectsEventForWrongConversation(t *testing.T) {
	_, store := openAgentServerPostgres(t)
	_, record := seedPostgresConversationRun(t, store)
	other := Conversation{
		Ref:       runtimesdk.ConversationRef{ID: "conv-2", AgentID: "agent-1", WorkspaceID: "workspace-1"},
		CreatedAt: time.Now().UTC(),
	}
	if _, _, err := store.PutConversation(context.Background(), other); err != nil {
		t.Fatal(err)
	}
	_, err := store.AppendEvent(context.Background(), runtimesdk.EventEnvelope{
		Type:           runtimesdk.EventRunStarted,
		RunID:          record.ID,
		ConversationID: other.Ref.ID,
		OccurredAt:     time.Now().UTC(),
	})
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("error=%v want conflict", err)
	}
}
