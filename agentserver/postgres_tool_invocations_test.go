package agentserver

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/achirothmane/governed-agent-runtime/mcptransport"
	runtimesdk "github.com/achirothmane/governed-agent-runtime/sdk"
	"github.com/achirothmane/governed-agent-runtime/temporaltools"
)

func TestPostgresToolInvocationSurvivesStoreRecreationAndIsIdempotent(t *testing.T) {
	db, store := openAgentServerPostgres(t)
	conversation, run := seedPostgresConversationRun(t, store)
	ctx := context.Background()
	arguments := map[string]any{
		"rows": []any{
			map[string]any{"id": "a", "price": 20.0},
			map[string]any{"id": "b", "price": 21.0},
		},
	}
	digest, err := temporaltools.ArgumentsDigest(arguments)
	if err != nil {
		t.Fatal(err)
	}
	ref := temporaltools.InvocationRef{
		InvocationID:   "profile-1",
		RunID:          run.ID,
		ConversationID: conversation.Ref.ID,
		Tool: runtimesdk.ToolDescriptor{
			Name:           "data.profile",
			Protocol:       runtimesdk.ToolProtocolMCP,
			Endpoint:       "http://data-engine.test/mcp",
			ReadOnly:       true,
			SnapshotDigest: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		},
		ArgumentsDigest: digest,
	}

	first, created, err := store.Prepare(ctx, ref, arguments)
	if err != nil {
		t.Fatal(err)
	}
	if !created || first.State != temporaltools.InvocationPrepared {
		t.Fatalf("first=%#v created=%v", first, created)
	}
	second, created, err := store.Prepare(ctx, ref, arguments)
	if err != nil {
		t.Fatal(err)
	}
	if created || !temporaltools.SameBinding(first.Ref, second.Ref) {
		t.Fatalf("second=%#v created=%v", second, created)
	}

	restarted, err := NewPostgresStore(db)
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := restarted.Get(ctx, ref.InvocationID)
	if err != nil {
		t.Fatal(err)
	}
	if !temporaltools.SameBinding(loaded.Ref, ref) {
		t.Fatalf("loaded ref=%#v want=%#v", loaded.Ref, ref)
	}
	raw, _ := json.Marshal(loaded.Arguments)
	if string(raw) == "" || !json.Valid(raw) {
		t.Fatalf("loaded arguments=%s", raw)
	}

	result := mcptransport.Result{
		Tool:              ref.Tool.Name,
		SnapshotDigest:    ref.Tool.SnapshotDigest,
		StructuredContent: json.RawMessage(`{"stage":"PRE_SEMANTIC_PROFILE","decision":"KNOWN"}`),
	}
	resultDigest, err := temporaltools.ResultDigest(result)
	if err != nil {
		t.Fatal(err)
	}
	completed, err := restarted.Complete(ctx, ref.InvocationID, result, resultDigest)
	if err != nil {
		t.Fatal(err)
	}
	if completed.State != temporaltools.InvocationComplete || completed.Result == nil || completed.ResultDigest != resultDigest {
		t.Fatalf("completed=%#v", completed)
	}

	restartedAgain, err := NewPostgresStore(db)
	if err != nil {
		t.Fatal(err)
	}
	recovered, err := restartedAgain.Get(ctx, ref.InvocationID)
	if err != nil {
		t.Fatal(err)
	}
	if recovered.State != temporaltools.InvocationComplete || recovered.Result == nil || recovered.ResultDigest != resultDigest {
		t.Fatalf("recovered=%#v", recovered)
	}
	if _, err := restartedAgain.Complete(ctx, ref.InvocationID, result, resultDigest); err != nil {
		t.Fatalf("idempotent complete: %v", err)
	}
}

func TestPostgresToolInvocationRejectsInvocationIDRebinding(t *testing.T) {
	_, store := openAgentServerPostgres(t)
	conversation, run := seedPostgresConversationRun(t, store)
	ctx := context.Background()
	args := map[string]any{"rows": []any{map[string]any{"id": "a"}}}
	digest, err := temporaltools.ArgumentsDigest(args)
	if err != nil {
		t.Fatal(err)
	}
	ref := temporaltools.InvocationRef{
		InvocationID:   "profile-rebind",
		RunID:          run.ID,
		ConversationID: conversation.Ref.ID,
		Tool: runtimesdk.ToolDescriptor{
			Name:           "data.profile",
			Protocol:       runtimesdk.ToolProtocolMCP,
			Endpoint:       "http://data-engine.test/mcp",
			ReadOnly:       true,
			SnapshotDigest: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		},
		ArgumentsDigest: digest,
	}
	if _, _, err := store.Prepare(ctx, ref, args); err != nil {
		t.Fatal(err)
	}

	changed := ref
	changed.Tool.SnapshotDigest = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	if _, _, err := store.Prepare(ctx, changed, args); !errors.Is(err, temporaltools.ErrInvocationConflict) {
		t.Fatalf("error=%v want ErrInvocationConflict", err)
	}
}

func TestPostgresToolInvocationFailureDoesNotOverwriteCompletion(t *testing.T) {
	_, store := openAgentServerPostgres(t)
	conversation, run := seedPostgresConversationRun(t, store)
	ctx := context.Background()
	args := map[string]any{"rows": []any{map[string]any{"id": "a"}}}
	digest, _ := temporaltools.ArgumentsDigest(args)
	ref := temporaltools.InvocationRef{
		InvocationID:   "profile-complete",
		RunID:          run.ID,
		ConversationID: conversation.Ref.ID,
		Tool: runtimesdk.ToolDescriptor{
			Name:           "data.profile",
			Protocol:       runtimesdk.ToolProtocolMCP,
			Endpoint:       "http://data-engine.test/mcp",
			ReadOnly:       true,
			SnapshotDigest: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		},
		ArgumentsDigest: digest,
	}
	if _, _, err := store.Prepare(ctx, ref, args); err != nil {
		t.Fatal(err)
	}
	result := mcptransport.Result{Tool: ref.Tool.Name, SnapshotDigest: ref.Tool.SnapshotDigest}
	resultDigest, _ := temporaltools.ResultDigest(result)
	if _, err := store.Complete(ctx, ref.InvocationID, result, resultDigest); err != nil {
		t.Fatal(err)
	}
	if err := store.Fail(ctx, ref.InvocationID, "late failure"); err != nil {
		t.Fatal(err)
	}
	got, err := store.Get(ctx, ref.InvocationID)
	if err != nil {
		t.Fatal(err)
	}
	if got.State != temporaltools.InvocationComplete || got.LastError != "" {
		t.Fatalf("completed invocation was overwritten: %#v", got)
	}
}
