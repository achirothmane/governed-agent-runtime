//go:build integration

package dotsrealcontext

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/achirothmane/governed-agent-runtime/agentserver"
	"github.com/achirothmane/governed-agent-runtime/dotdecision"
	"github.com/achirothmane/governed-agent-runtime/dotdurable"
	"github.com/achirothmane/governed-agent-runtime/dotproviders/ollama"
	"github.com/achirothmane/governed-agent-runtime/portfoliocontext"
	_ "github.com/jackc/pgx/v5/stdlib"
	"go.temporal.io/sdk/testsuite"
)

// Unlike D4's prior LOCAL DETERMINISTIC decision, this test calls an
// actual downloaded local language model. It provides no tools or write access.
func TestActualLocalLanguageModelDecidesOverRealPublicPortfolio(t *testing.T) {
	if os.Getenv("DOTS_ALLOW_LOCAL_LLM_TEST") != "1" {
		t.Skip("actual local LLM test requires explicit CI flag")
	}
	root := os.Getenv("PORTFOLIO_ROOT")
	snapshotFile := os.Getenv("PORTFOLIO_CONTEXT_FILE")
	dbURL := os.Getenv("DATABASE_URL")
	model := os.Getenv("DOTS_OLLAMA_MODEL")
	if root == "" || snapshotFile == "" || dbURL == "" || model == "" {
		t.Fatal("actual source, local DB and local model required")
	}
	if !strings.Contains(dbURL, "@localhost:5432/") && !strings.Contains(dbURL, "@127.0.0.1:5432/") {
		t.Fatal("refusing remote database")
	}
	raw, err := os.ReadFile(snapshotFile)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := portfoliocontext.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	if err := checkSources(root, snapshot.SourceBindings); err != nil {
		t.Fatal(err)
	}
	view := snapshot.ReasoningView()
	if len(view.RunnableItems) != 1 || view.RunnableItems[0].ID != oneRealItemID || view.RunnableItems[0].Authority != "OBSERVE" {
		t.Fatal("real ONE-task read-only admission is missing")
	}
	if !snapshot.RequiresHuman("merge") || !snapshot.RequiresHuman("paid-spend") {
		t.Fatal("human authority missing")
	}
	reasoner, err := ollama.New(ollama.Config{Model: model, Endpoint: "http://127.0.0.1:11434/api/chat"})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 7*time.Minute)
	defer cancel()
	db, err := sql.Open("pgx", dbURL)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.PingContext(ctx); err != nil {
		t.Fatal(err)
	}
	store, err := agentserver.NewPostgresStore(db)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	dev, err := testsuite.StartDevServer(ctx, testsuite.DevServerOptions{
		CachedDownload: testsuite.CachedDownload{Version: "default"}, LogLevel: "error",
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = dev.Stop() }()
	worker, err := dotdurable.NewWorker(dotdurable.WorkerConfig{
		Client: dev.Client(), TaskQueue: "dots-s2-real-local-model",
		Snapshots: sourceBoundLoader{snapshot: snapshot, root: root}, Reasoner: reasoner, Store: store,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := worker.Start(); err != nil {
		t.Fatal(err)
	}
	defer worker.Stop()
	executor := dotdurable.Executor{Client: dev.Client(), TaskQueue: "dots-s2-real-local-model"}
	req := dotdurable.Request{
		DecisionID:     "dots-s2-actual-local-model-" + snapshot.SnapshotDigest[:16],
		SnapshotDigest: snapshot.SnapshotDigest,
	}
	committed, err := executor.Decide(ctx, req)
	if err != nil {
		t.Fatalf("real local model could not produce an admitted D3/D4 decision: %v", err)
	}
	if committed.WorkItemID != oneRealItemID || committed.RequestedAuthority != "OBSERVE" ||
		committed.RequestedAction != "" || committed.SnapshotDigest != snapshot.SnapshotDigest ||
		(committed.Kind != dotdecision.ProposeNextGate && committed.Kind != dotdecision.Refuse) {
		t.Fatalf("model decision escaped boundary: %#v", committed)
	}
	if strings.TrimSpace(committed.Rationale) == "" {
		t.Fatal("model produced no explanation")
	}
	persistent, err := store.GetCommittedDecision(ctx, req.DecisionID)
	if err != nil || !dotdurable.SameRecord(committed, persistent) {
		t.Fatalf("Postgres decision differs: %v", err)
	}
	same, err := executor.Decide(ctx, req)
	if err != nil || !dotdurable.SameRecord(same, committed) {
		t.Fatalf("Temporal replay mismatch: %v", err)
	}
	var count int
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM agent_server_tool_invocations").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("agent tool ledger unexpectedly contains %d effects", count)
	}
	// Record what happened (no invented "human decision score"). The independent
	// rubric runs in a separate CI step; the model never receives its answers.
	evidence := map[string]any{
		"schema_version": 1, "source": "real_public_portfolio_checked_out_at_pinned_sha",
		"kind": committed.Kind, "rationale": committed.Rationale,
		"authority": committed.RequestedAuthority, "requested_action": committed.RequestedAction,
		"work_item_id":    committed.WorkItemID,
		"snapshot_digest": committed.SnapshotDigest, "decision_digest": committed.DecisionDigest,
		"model": model, "reasoner_kind": "actual_local_ollama_language_model",
		"temporal_postgres_commit": true, "same_decision_replay": true,
		"tool_invocations": count, "paid_provider_calls": 0,
		"decision_quality":                 "NOT_YET_INDEPENDENTLY_SCORED",
		"autonomous_management_authorized": false,
	}
	file := os.Getenv("DOTS_S2_EVIDENCE_FILE")
	if file == "" {
		t.Fatal("evidence output file is required for independent scoring")
	}
	body, err := json.MarshalIndent(evidence, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, body, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Logf("DOTS_S2_REAL_LOCAL_MODEL_DURABLE_DECISION provider_calls=0 tool_invocations=%d (independent quality pending)", count)
}
