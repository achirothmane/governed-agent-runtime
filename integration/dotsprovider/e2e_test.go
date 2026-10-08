//go:build integration

package dotsprovider

import (
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	"go.temporal.io/sdk/testsuite"

	"github.com/achirothmane/governed-agent-runtime/agentserver"
	"github.com/achirothmane/governed-agent-runtime/dotdecision"
	"github.com/achirothmane/governed-agent-runtime/dotdurable"
	openaireasoner "github.com/achirothmane/governed-agent-runtime/dotproviders/openai"
	"github.com/achirothmane/governed-agent-runtime/portfoliocontext"
)

type fileSnapshotLoader struct {
	path string
}

func (l fileSnapshotLoader) LoadSnapshot(_ context.Context, digest string) (portfoliocontext.Snapshot, error) {
	raw, err := os.ReadFile(l.path)
	if err != nil {
		return portfoliocontext.Snapshot{}, err
	}
	snapshot, err := portfoliocontext.Parse(raw)
	if err != nil {
		return portfoliocontext.Snapshot{}, err
	}
	if snapshot.SnapshotDigest != digest {
		return portfoliocontext.Snapshot{}, dotdurable.ErrSnapshotMismatch
	}
	return snapshot, nil
}

func TestCurrentPortfolioSnapshotFlowsThroughOpenAIAdapterAndDurableGate(t *testing.T) {
	contextPath := strings.TrimSpace(os.Getenv("PORTFOLIO_CONTEXT_FILE"))
	databaseURL := strings.TrimSpace(os.Getenv("DATABASE_URL"))
	if contextPath == "" || databaseURL == "" {
		t.Skip("PORTFOLIO_CONTEXT_FILE and DATABASE_URL are required")
	}

	raw, err := os.ReadFile(contextPath)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := portfoliocontext.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	view := snapshot.ReasoningView()
	if len(view.RunnableItems) != 1 || view.RunnableItems[0].ID != "dots-model-provider-adapter-005" {
		t.Fatalf("current runnable items=%#v", view.RunnableItems)
	}

	var (
		requestMu sync.Mutex
		captured  []byte
		requests  int
	)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		body, err := io.ReadAll(req.Body)
		if err != nil {
			t.Errorf("read provider request: %v", err)
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		requestMu.Lock()
		requests++
		captured = append([]byte(nil), body...)
		requestMu.Unlock()

		decisionJSON, err := json.Marshal(map[string]any{
			"kind":                string(dotdecision.ProposeNextGate),
			"snapshot_digest":     snapshot.SnapshotDigest,
			"work_item_id":        "dots-model-provider-adapter-005",
			"requested_authority": "PREPARE",
			"requested_action":    "prepare-openai-provider-adapter",
			"rationale":           "The sealed Portfolio view admits D5 with PREPARE authority.",
		})
		if err != nil {
			t.Errorf("encode provider decision: %v", err)
			http.Error(w, "encode error", http.StatusInternalServerError)
			return
		}
		response := map[string]any{
			"status": "completed",
			"output": []any{
				map[string]any{
					"type": "message",
					"role": "assistant",
					"content": []any{
						map[string]any{
							"type": "output_text",
							"text": string(decisionJSON),
						},
					},
				},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(response); err != nil {
			t.Errorf("write provider response: %v", err)
		}
	}))
	defer server.Close()

	reasoner, err := openaireasoner.New(openaireasoner.Config{
		Endpoint:   server.URL,
		Model:      "fake-structured-model",
		APIKey:     "fake-ci-key",
		HTTPClient: server.Client(),
	})
	if err != nil {
		t.Fatal(err)
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
	if _, err := db.ExecContext(ctx, "TRUNCATE portfolio_dot_decisions, agent_server_tool_invocations, agent_server_reasoning_steps, agent_server_agent_executions, agent_server_events, agent_server_runs, agent_server_conversations CASCADE"); err != nil {
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

	const taskQueue = "portfolio-dot-d5-provider"
	worker, err := dotdurable.NewWorker(dotdurable.WorkerConfig{
		Client:    dev.Client(),
		TaskQueue: taskQueue,
		Snapshots: fileSnapshotLoader{path: contextPath},
		Reasoner:  reasoner,
		Store:     store,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := worker.Start(); err != nil {
		t.Fatal(err)
	}
	defer worker.Stop()

	executor := dotdurable.Executor{Client: dev.Client(), TaskQueue: taskQueue}
	req := dotdurable.Request{
		DecisionID:     "portfolio-dot-d5-provider-current",
		SnapshotDigest: snapshot.SnapshotDigest,
	}
	record, err := executor.Decide(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	if record.Kind != dotdecision.ProposeNextGate ||
		record.WorkItemID != "dots-model-provider-adapter-005" ||
		record.RequestedAuthority != "PREPARE" ||
		record.SnapshotDigest != snapshot.SnapshotDigest {
		t.Fatalf("committed provider decision=%#v", record)
	}

	persisted, err := store.GetCommittedDecision(ctx, req.DecisionID)
	if err != nil {
		t.Fatal(err)
	}
	if !dotdurable.SameRecord(record, persisted) {
		t.Fatalf("provider decision was not durably committed: workflow=%#v postgres=%#v", record, persisted)
	}

	requestMu.Lock()
	capturedRequest := append([]byte(nil), captured...)
	requestCount := requests
	requestMu.Unlock()
	if requestCount != 1 {
		t.Fatalf("provider request count=%d want=1", requestCount)
	}
	requestText := string(capturedRequest)
	for _, forbidden := range []string{
		"source_bindings",
		"SYSTEM-MAP.md",
		"portfolio/execution-queue.yaml",
		"\"tools\"",
	} {
		if strings.Contains(requestText, forbidden) {
			t.Fatalf("provider request leaked forbidden surface %q: %s", forbidden, requestText)
		}
	}
	if !strings.Contains(requestText, snapshot.SnapshotDigest) ||
		!strings.Contains(requestText, "dots-model-provider-adapter-005") {
		t.Fatalf("provider request is not bound to current snapshot/work item: %s", requestText)
	}

	var toolCount int
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM agent_server_tool_invocations").Scan(&toolCount); err != nil {
		t.Fatal(err)
	}
	if toolCount != 0 {
		t.Fatalf("D5 provider reasoning executed tools: count=%d", toolCount)
	}

	if evidencePath := strings.TrimSpace(os.Getenv("DOT_PROVIDER_EVIDENCE_FILE")); evidencePath != "" {
		evidence := map[string]any{
			"state":                   "PASS",
			"snapshot_digest":         snapshot.SnapshotDigest,
			"decision":                record,
			"provider_requests":       requestCount,
			"provider_source_leak":    false,
			"tool_invocations":        toolCount,
			"transport":               "local-fake-openai-responses",
			"live_provider_call":      false,
			"paid_provider_call":      false,
			"d3_validation_before_d4": true,
			"durable_postgres_commit": true,
		}
		evidenceRaw, err := json.MarshalIndent(evidence, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(evidencePath, evidenceRaw, 0o644); err != nil {
			t.Fatal(err)
		}
	}
}
