//go:build integration

package dots

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	"go.temporal.io/sdk/testsuite"

	"github.com/achirothmane/governed-agent-runtime/agentserver"
	"github.com/achirothmane/governed-agent-runtime/integrations/dataengine"
	"github.com/achirothmane/governed-agent-runtime/mcptransport"
	runtimesdk "github.com/achirothmane/governed-agent-runtime/sdk"
	"github.com/achirothmane/governed-agent-runtime/temporaltools"
)

type bindingBackend struct{}

func (bindingBackend) Name() string { return "dots-binding" }
func (bindingBackend) Start(context.Context, runtimesdk.RunRequest) (runtimesdk.RunHandle, error) {
	return runtimesdk.RunHandle{}, errors.New("Dots binding backend does not start parent runs")
}
func (bindingBackend) Inspect(context.Context, runtimesdk.RunID) (runtimesdk.RunHandle, error) {
	return runtimesdk.RunHandle{}, errors.New("Dots binding backend does not inspect parent runs")
}
func (bindingBackend) Signal(context.Context, runtimesdk.RunID, string, []byte) error {
	return errors.New("Dots binding backend does not signal parent runs")
}
func (bindingBackend) Cancel(context.Context, runtimesdk.RunID, string) error {
	return errors.New("Dots binding backend does not cancel parent runs")
}

func reportMap(t *testing.T, raw json.RawMessage) map[string]any {
	t.Helper()
	var outer map[string]any
	if err := json.Unmarshal(raw, &outer); err != nil {
		t.Fatal(err)
	}
	report, ok := outer["report"].(map[string]any)
	if !ok {
		t.Fatalf("missing report in %#v", outer)
	}
	return report
}

func TestDotsDurablyInvokesDataReconcile(t *testing.T) {
	dataEngineURL := strings.TrimSpace(os.Getenv("DATA_ENGINE_MCP_URL"))
	databaseURL := strings.TrimSpace(os.Getenv("DATABASE_URL"))
	if dataEngineURL == "" || databaseURL == "" {
		t.Skip("DATA_ENGINE_MCP_URL and DATABASE_URL are required")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	db, err := sql.Open("pgx", databaseURL)
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
	if _, err := db.ExecContext(ctx, "TRUNCATE agent_server_tool_invocations, agent_server_events, agent_server_runs, agent_server_conversations CASCADE"); err != nil {
		t.Fatal(err)
	}

	dev, err := testsuite.StartDevServer(ctx, testsuite.DevServerOptions{
		CachedDownload: testsuite.CachedDownload{Version: "default"},
		LogLevel:       "error",
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := dev.Stop(); err != nil {
			t.Logf("stop Temporal dev server: %v", err)
		}
	}()

	const taskQueue = "dots-data-tools"
	provider := dataengine.Provider{
		Endpoint:  dataEngineURL,
		Connector: mcptransport.Connector{},
		Backend:   bindingBackend{},
		AgentSpec: runtimesdk.AgentSpec{
			ID:            "portfolio-dot-data",
			Mission:       "Inspect pinned data evidence and preserve Data Engine boundary decisions.",
			RequiredTools: []runtimesdk.ToolName{dataengine.ProfileTool, dataengine.ReconcileTool},
			Workspace:     runtimesdk.WorkspaceSpec{ID: "portfolio-dot", Kind: runtimesdk.WorkspaceRemote},
		},
	}
	worker, err := temporaltools.NewWorker(temporaltools.WorkerConfig{
		Client:    dev.Client(),
		TaskQueue: taskQueue,
		Store:     store,
		Invoker:   provider,
		Evidence:  agentserver.ToolActivityEvidence{Store: store},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := worker.Start(); err != nil {
		t.Fatal(err)
	}
	defer worker.Stop()

	runtime, err := provider.Runtime(ctx, "portfolio-dot-data")
	if err != nil {
		t.Fatal(err)
	}

	const (
		conversationID = runtimesdk.ConversationID("conv-dots-data-reconcile")
		runID          = runtimesdk.RunID("run-dots-data-reconcile")
		input          = "Compare pinned source and target snapshots without overriding Data Engine boundary decisions."
	)
	conversation := agentserver.Conversation{
		Ref: runtimesdk.ConversationRef{
			ID:          conversationID,
			AgentID:     runtime.Agent.ID,
			WorkspaceID: runtime.Agent.Workspace.ID,
		},
		CreatedAt: time.Now().UTC(),
	}
	if _, created, err := store.PutConversation(ctx, conversation); err != nil || !created {
		t.Fatalf("put conversation created=%v err=%v", created, err)
	}

	bound, fingerprint, err := runtime.Bind(runtimesdk.StartRequest{
		RunID:          runID,
		ConversationID: conversationID,
		Input:          input,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(bound.Tools) != 2 {
		t.Fatalf("bound tools=%#v", bound.Tools)
	}
	record, err := agentserver.NewRunRecord(
		conversation,
		input,
		runtimesdk.RunHandle{
			ID:          runID,
			Backend:     "dots-binding",
			Fingerprint: fingerprint,
			State:       runtimesdk.RunQueued,
		},
		time.Now().UTC(),
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, created, err := store.PutRun(ctx, record); err != nil || !created {
		t.Fatalf("put run created=%v err=%v", created, err)
	}

	var reconcileTool runtimesdk.ToolDescriptor
	for _, tool := range bound.Tools {
		if tool.Name == dataengine.ReconcileTool {
			reconcileTool = tool
			break
		}
	}
	if reconcileTool.Name != dataengine.ReconcileTool || !reconcileTool.ReadOnly || reconcileTool.SnapshotDigest == "" {
		t.Fatalf("reconcile tool=%#v", reconcileTool)
	}

	executor := temporaltools.Executor{Client: dev.Client(), TaskQueue: taskQueue, Store: store}
	args := map[string]any{
		"source": map[string]any{
			"ref":     "source://orders",
			"columns": []any{"id", "amount", "status", "secret"},
			"rows": []any{
				map[string]any{"id": "ord-001", "amount": "100.00", "status": "paid", "secret": "dots-sensitive"},
				map[string]any{"id": "ord-002", "amount": "50.00", "status": "pending", "secret": "dots-sensitive"},
				map[string]any{"id": "ord-003", "amount": "75.00", "status": "paid", "secret": "dots-sensitive"},
			},
		},
		"target": map[string]any{
			"ref":     "target://orders",
			"columns": []any{"id", "amount", "status", "secret"},
			"rows": []any{
				map[string]any{"id": "ord-001", "amount": "100.00", "status": "paid", "secret": "dots-sensitive"},
				map[string]any{"id": "ord-002", "amount": "55.00", "status": "pending", "secret": "dots-sensitive"},
				map[string]any{"id": "ord-004", "amount": "75.00", "status": "paid", "secret": "dots-sensitive"},
			},
		},
		"contract": map[string]any{
			"identity_field": "id",
			"fields":         []any{"amount", "status"},
			"comparison":     "EXACT_RAW",
		},
	}

	first, err := executor.Execute(ctx, runID, conversationID, "dots-reconcile-1", reconcileTool, args)
	if err != nil {
		t.Fatal(err)
	}
	if first.IsError || first.SnapshotDigest != reconcileTool.SnapshotDigest {
		t.Fatalf("result=%#v", first)
	}
	report := reportMap(t, first.StructuredContent)
	if report["state"] != "DIVERGED" || report["reason"] != "SOURCE_TARGET_DIVERGED" {
		t.Fatalf("report=%#v", report)
	}
	if len(report["source_sha256"].(string)) != 64 || len(report["target_sha256"].(string)) != 64 {
		t.Fatalf("hashes missing: %#v", report)
	}
	mismatches := report["value_mismatches"].([]any)
	if len(mismatches) != 1 {
		t.Fatalf("mismatches=%#v", mismatches)
	}
	mismatch := mismatches[0].(map[string]any)
	if mismatch["key"] != "ord-002" || mismatch["field"] != "amount" {
		t.Fatalf("mismatch=%#v", mismatch)
	}
	missing := report["missing_in_target"].([]any)
	extra := report["extra_in_target"].([]any)
	if len(missing) != 1 || missing[0] != "ord-003" || len(extra) != 1 || extra[0] != "ord-004" {
		t.Fatalf("missing=%#v extra=%#v", missing, extra)
	}
	boundary := report["boundary"].(map[string]any)
	for _, key := range []string{"transforms_values", "infers_mappings", "repairs_data", "declares_cutover_safe"} {
		if boundary[key] != false {
			t.Fatalf("boundary escaped %s=%#v", key, boundary[key])
		}
	}

	invocation, err := store.Get(ctx, "dots-reconcile-1")
	if err != nil {
		t.Fatal(err)
	}
	if invocation.State != temporaltools.InvocationComplete || invocation.Result == nil || invocation.ResultDigest == "" {
		t.Fatalf("invocation=%#v", invocation)
	}

	second, err := executor.Execute(ctx, runID, conversationID, "dots-reconcile-1", reconcileTool, args)
	if err != nil {
		t.Fatal(err)
	}
	if second.SnapshotDigest != first.SnapshotDigest || string(second.StructuredContent) != string(first.StructuredContent) {
		t.Fatal("idempotent retry changed durable reconciliation result")
	}

	unknownArgs := map[string]any{
		"source": map[string]any{
			"columns": []any{"id", "amount"},
			"rows":    []any{map[string]any{"id": "1", "amount": "10"}},
		},
		"target": map[string]any{
			"columns": []any{"id", "amount"},
			"rows":    []any{map[string]any{"id": "1", "amount": "10.0"}},
		},
		"contract": map[string]any{
			"identity_field": "id",
			"fields":         []any{"amount"},
			"comparison":     "COERCE_NUMERIC",
		},
	}
	unknown, err := executor.Execute(ctx, runID, conversationID, "dots-reconcile-unknown", reconcileTool, unknownArgs)
	if err != nil {
		t.Fatal(err)
	}
	unknownReport := reportMap(t, unknown.StructuredContent)
	if unknownReport["state"] != "UNKNOWN" || unknownReport["reason"] != "UNSUPPORTED_COMPARISON_CONTRACT" {
		t.Fatalf("unknown report=%#v", unknownReport)
	}

	events, err := store.ListEvents(ctx, conversationID, 0, 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 4 {
		t.Fatalf("events=%#v", events)
	}
	for _, event := range events {
		if strings.Contains(string(event.Payload), "dots-sensitive") {
			t.Fatalf("event leaked raw reconciliation evidence: %s", event.Payload)
		}
	}
}
