package agentserver

import (
	"context"
	"errors"
	"testing"

	"github.com/achirothmane/governed-agent-runtime/dotdecision"
	"github.com/achirothmane/governed-agent-runtime/dotdurable"
)

func TestPostgresStorePersistsDotDecisionIdempotently(t *testing.T) {
	db, store := openAgentServerPostgres(t)
	ctx := context.Background()
	if _, err := db.ExecContext(ctx, "TRUNCATE portfolio_dot_decisions"); err != nil {
		t.Fatal(err)
	}
	record := dotdurable.Record{
		DecisionID:         "dots-decision-1",
		SnapshotDigest:     "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		WorkItemID:         "dots-durable-decision-loop-004",
		Kind:               dotdecision.ProposeNextGate,
		RequestedAuthority: "PREPARE",
		RequestedAction:    "prepare-durable-decision-loop",
		Rationale:          "validated D4 decision",
		DecisionDigest:     "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
	}
	first, created, err := store.PutCommittedDecision(ctx, record)
	if err != nil || !created {
		t.Fatalf("first put created=%v err=%v", created, err)
	}
	second, created, err := store.PutCommittedDecision(ctx, record)
	if err != nil || created {
		t.Fatalf("second put created=%v err=%v", created, err)
	}
	if !dotdurable.SameRecord(first, second) {
		t.Fatalf("idempotent read changed record: first=%#v second=%#v", first, second)
	}

	conflict := record
	conflict.Rationale = "different decision bytes"
	conflict.DecisionDigest = "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
	if _, _, err := store.PutCommittedDecision(ctx, conflict); !errors.Is(err, dotdurable.ErrDecisionConflict) {
		t.Fatalf("conflict error=%v", err)
	}
}
