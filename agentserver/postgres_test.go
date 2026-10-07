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

func newPostgresAgentServerStore(t *testing.T) (*PostgresStore, *sql.DB) {
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

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		t.Fatal(err)
	}
	store, err := NewPostgresStore(db)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, "TRUNCATE TABLE agent_server_events, agent_server_event_cursors, agent_server_runs, agent_server_conversations RESTART IDENTITY CASCADE"); err != nil {
		t.Fatal(err)
	}
	return store, db
}

func pgConversation(id string, at time.Time) Conversation {
	return Conversation{
		Ref: runtimesdk.ConversationRef{
			ID:          runtimesdk.ConversationID(id),
			AgentID:     "agent",
			WorkspaceID: "workspace",
		},
		CreatedAt: at,
	}
}

func pgRun(t *testing.T, conversation Conversation, id, input string, at time.Time) RunRecord {
	t.Helper()
	handle := runtimesdk.RunHandle{
		ID:          runtimesdk.RunID(id),
		Backend:     "temporal",
		ExternalID:  "ai-native-run/" + id,
		Fingerprint: "fingerprint-" + id,
		State:       runtimesdk.RunQueued,
	}
	record, err := NewRunRecord(conversation, input, handle, at)
	if err != nil {
		t.Fatal(err)
	}
	return record
}

func TestPostgresAgentServerStoreSurvivesReconstruction(t *testing.T) {
	store, db := newPostgresAgentServerStore(t)
	ctx := context.Background()
	at := time.Date(2026, 10, 7, 14, 0, 0, 123456789, time.UTC)
	conversation := pgConversation("restart-conversation", at)

	if _, created, err := store.PutConversation(ctx, conversation); err != nil || !created {
		t.Fatalf("PutConversation created=%v err=%v", created, err)
	}
	record := pgRun(t, conversation, "restart-run", "profile dataset", at.Add(time.Second))
	if _, created, err := store.PutRun(ctx, record); err != nil || !created {
		t.Fatalf("PutRun created=%v err=%v", created, err)
	}
	first, err := store.AppendEvent(ctx, runtimesdk.EventEnvelope{
		Type:           runtimesdk.EventRunStarted,
		RunID:          record.ID,
		ConversationID: conversation.Ref.ID,
		OccurredAt:     at.Add(2 * time.Second),
	})
	if err != nil {
		t.Fatal(err)
	}

	restarted, err := NewPostgresStore(db)
	if err != nil {
		t.Fatal(err)
	}
	gotConversation, err := restarted.GetConversation(ctx, conversation.Ref.ID)
	if err != nil {
		t.Fatal(err)
	}
	if gotConversation.Ref != conversation.Ref || !gotConversation.CreatedAt.Equal(conversation.CreatedAt) {
		t.Fatalf("conversation after restart=%#v want %#v", gotConversation, conversation)
	}
	gotRun, err := restarted.GetRun(ctx, record.ID)
	if err != nil {
		t.Fatal(err)
	}
	if gotRun.Handle.Fingerprint != record.Handle.Fingerprint || gotRun.InputDigest != record.InputDigest {
		t.Fatalf("run binding after restart=%#v", gotRun)
	}
	events, err := restarted.ListEvents(ctx, conversation.Ref.ID, 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].Sequence != first.Sequence || events[0].Type != runtimesdk.EventRunStarted {
		t.Fatalf("events after restart=%#v", events)
	}
}

func TestPostgresAgentServerStoreConcurrentEventsAreContiguous(t *testing.T) {
	store, _ := newPostgresAgentServerStore(t)
	ctx := context.Background()
	at := time.Date(2026, 10, 7, 14, 10, 0, 0, time.UTC)
	conversation := pgConversation("ordered-conversation", at)
	if _, _, err := store.PutConversation(ctx, conversation); err != nil {
		t.Fatal(err)
	}

	const n = 32
	var wg sync.WaitGroup
	errs := make(chan error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, err := store.AppendEvent(ctx, runtimesdk.EventEnvelope{
				Type:           runtimesdk.EventType("server.heartbeat"),
				ConversationID: conversation.Ref.ID,
				OccurredAt:     at.Add(time.Duration(i+1) * time.Nanosecond),
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

	events, err := store.ListEvents(ctx, conversation.Ref.ID, 0, n+1)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != n {
		t.Fatalf("event count=%d want %d", len(events), n)
	}
	for i, event := range events {
		want := uint64(i + 1)
		if event.Sequence != want {
			t.Fatalf("event[%d].sequence=%d want %d", i, event.Sequence, want)
		}
	}
}

func TestPostgresAgentServerStoreRunBindingIsIdempotentAndConflictsFailClosed(t *testing.T) {
	store, _ := newPostgresAgentServerStore(t)
	ctx := context.Background()
	at := time.Date(2026, 10, 7, 14, 20, 0, 0, time.UTC)
	conversation := pgConversation("binding-conversation", at)
	if _, _, err := store.PutConversation(ctx, conversation); err != nil {
		t.Fatal(err)
	}
	record := pgRun(t, conversation, "bound-run", "same input", at.Add(time.Second))
	first, created, err := store.PutRun(ctx, record)
	if err != nil || !created {
		t.Fatalf("first PutRun created=%v err=%v", created, err)
	}
	second, created, err := store.PutRun(ctx, record)
	if err != nil || created {
		t.Fatalf("second PutRun created=%v err=%v", created, err)
	}
	if first.Handle.Fingerprint != second.Handle.Fingerprint {
		t.Fatalf("idempotent fingerprint changed")
	}

	conflict := record
	conflict.Input = "different input"
	conflict.InputDigest = InputDigest(conflict.Input)
	if _, _, err := store.PutRun(ctx, conflict); !errors.Is(err, ErrConflict) {
		t.Fatalf("conflicting PutRun error=%v want ErrConflict", err)
	}
}

func TestPostgresAgentServerStoreCursorResumesAfterReconstruction(t *testing.T) {
	store, db := newPostgresAgentServerStore(t)
	ctx := context.Background()
	at := time.Date(2026, 10, 7, 14, 30, 0, 0, time.UTC)
	conversation := pgConversation("cursor-conversation", at)
	if _, _, err := store.PutConversation(ctx, conversation); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		if _, err := store.AppendEvent(ctx, runtimesdk.EventEnvelope{
			Type:           runtimesdk.EventType("server.progress"),
			ConversationID: conversation.Ref.ID,
			OccurredAt:     at.Add(time.Duration(i+1) * time.Second),
		}); err != nil {
			t.Fatal(err)
		}
	}

	restarted, err := NewPostgresStore(db)
	if err != nil {
		t.Fatal(err)
	}
	events, err := restarted.ListEvents(ctx, conversation.Ref.ID, 1, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 || events[0].Sequence != 2 || events[1].Sequence != 3 {
		t.Fatalf("resumed events=%#v", events)
	}
}

func TestPostgresAgentServerStoreRejectsEventRunConversationMismatch(t *testing.T) {
	store, _ := newPostgresAgentServerStore(t)
	ctx := context.Background()
	at := time.Date(2026, 10, 7, 14, 40, 0, 0, time.UTC)
	c1 := pgConversation("event-c1", at)
	c2 := pgConversation("event-c2", at)
	if _, _, err := store.PutConversation(ctx, c1); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.PutConversation(ctx, c2); err != nil {
		t.Fatal(err)
	}
	record := pgRun(t, c1, "event-run", "work", at.Add(time.Second))
	if _, _, err := store.PutRun(ctx, record); err != nil {
		t.Fatal(err)
	}
	_, err := store.AppendEvent(ctx, runtimesdk.EventEnvelope{
		Type:           runtimesdk.EventRunStarted,
		RunID:          record.ID,
		ConversationID: c2.Ref.ID,
		OccurredAt:     at.Add(2 * time.Second),
	})
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("error=%v want ErrConflict", err)
	}
}
