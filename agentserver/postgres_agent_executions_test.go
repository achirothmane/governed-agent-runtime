package agentserver

import (
	"context"
	"errors"
	"testing"

	"github.com/achirothmane/governed-agent-runtime/agentloop"
	"github.com/achirothmane/governed-agent-runtime/temporalagent"
)

func TestPostgresAgentExecutionAndDecisionSurviveStoreRecreation(t *testing.T) {
	db, store := openAgentServerPostgres(t)
	conversation, run := seedPostgresConversationRun(t, store)
	ctx := context.Background()

	execution := temporalagent.ExecutionRef{
		RunID:          run.ID,
		ConversationID: conversation.Ref.ID,
		RunFingerprint: run.Handle.Fingerprint,
		MissionDigest:  temporalagent.MissionDigest("Inspect evidence."),
		MaxSteps:       8,
	}
	stored, created, err := store.PrepareExecution(ctx, execution)
	if err != nil {
		t.Fatal(err)
	}
	if !created || stored != execution {
		t.Fatalf("execution created=%v stored=%#v", created, stored)
	}

	decision := agentloop.Decision{
		Kind: agentloop.DecisionTool,
		Tool: "data.profile",
		Arguments: map[string]any{
			"rows": []any{map[string]any{"id": "a", "price": 20}},
		},
	}
	digest, err := temporalagent.DecisionDigest(decision)
	if err != nil {
		t.Fatal(err)
	}
	record := temporalagent.DecisionRecord{
		RunID:          run.ID,
		Step:           1,
		Decision:       decision,
		DecisionDigest: digest,
	}
	gotDecision, decisionCreated, err := store.PutDecision(ctx, record)
	if err != nil {
		t.Fatal(err)
	}
	if !decisionCreated || gotDecision.DecisionDigest != digest {
		t.Fatalf("decision created=%v stored=%#v", decisionCreated, gotDecision)
	}

	restarted, err := NewPostgresStore(db)
	if err != nil {
		t.Fatal(err)
	}
	gotExecution, err := restarted.GetExecution(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if gotExecution != execution {
		t.Fatalf("execution after restart=%#v", gotExecution)
	}
	gotDecision, err = restarted.GetDecision(ctx, run.ID, 1)
	if err != nil {
		t.Fatal(err)
	}
	if gotDecision.DecisionDigest != digest || gotDecision.Decision.Tool != "data.profile" {
		t.Fatalf("decision after restart=%#v", gotDecision)
	}
}

func TestPostgresAgentExecutionRejectsRebinding(t *testing.T) {
	_, store := openAgentServerPostgres(t)
	conversation, run := seedPostgresConversationRun(t, store)
	ctx := context.Background()

	first := temporalagent.ExecutionRef{
		RunID:          run.ID,
		ConversationID: conversation.Ref.ID,
		RunFingerprint: run.Handle.Fingerprint,
		MissionDigest:  temporalagent.MissionDigest("first mission"),
		MaxSteps:       8,
	}
	if _, _, err := store.PrepareExecution(ctx, first); err != nil {
		t.Fatal(err)
	}
	changed := first
	changed.MissionDigest = temporalagent.MissionDigest("changed mission")
	if _, _, err := store.PrepareExecution(ctx, changed); !errors.Is(err, temporalagent.ErrExecutionConflict) {
		t.Fatalf("error=%v want ErrExecutionConflict", err)
	}
}

func TestPostgresReasoningStepRejectsDifferentCommittedDecision(t *testing.T) {
	_, store := openAgentServerPostgres(t)
	conversation, run := seedPostgresConversationRun(t, store)
	ctx := context.Background()
	execution := temporalagent.ExecutionRef{
		RunID:          run.ID,
		ConversationID: conversation.Ref.ID,
		RunFingerprint: run.Handle.Fingerprint,
		MissionDigest:  temporalagent.MissionDigest("mission"),
		MaxSteps:       8,
	}
	if _, _, err := store.PrepareExecution(ctx, execution); err != nil {
		t.Fatal(err)
	}
	first := agentloop.Decision{Kind: agentloop.DecisionFinish, Message: "first"}
	firstDigest, _ := temporalagent.DecisionDigest(first)
	if _, _, err := store.PutDecision(ctx, temporalagent.DecisionRecord{
		RunID: run.ID, Step: 1, Decision: first, DecisionDigest: firstDigest,
	}); err != nil {
		t.Fatal(err)
	}

	second := agentloop.Decision{Kind: agentloop.DecisionFinish, Message: "different"}
	secondDigest, _ := temporalagent.DecisionDigest(second)
	if _, _, err := store.PutDecision(ctx, temporalagent.DecisionRecord{
		RunID: run.ID, Step: 1, Decision: second, DecisionDigest: secondDigest,
	}); !errors.Is(err, temporalagent.ErrDecisionConflict) {
		t.Fatalf("error=%v want ErrDecisionConflict", err)
	}
}
