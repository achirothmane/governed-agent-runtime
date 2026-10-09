//go:build integration

package dotsdurable

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	enumspb "go.temporal.io/api/enums/v1"
	"go.temporal.io/sdk/testsuite"

	"github.com/achirothmane/governed-agent-runtime/agentserver"
	"github.com/achirothmane/governed-agent-runtime/dotdecision"
	"github.com/achirothmane/governed-agent-runtime/dotdurable"
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

type disconnectReasoner struct {
	started chan struct{}
	release chan struct{}
	once    sync.Once

	mu    sync.Mutex
	calls int
}

func newDisconnectReasoner() *disconnectReasoner {
	return &disconnectReasoner{
		started: make(chan struct{}),
		release: make(chan struct{}),
	}
}

func (r *disconnectReasoner) Decide(ctx context.Context, view portfoliocontext.ReasoningView) (dotdecision.Decision, error) {
	r.mu.Lock()
	r.calls++
	r.mu.Unlock()

	r.once.Do(func() { close(r.started) })
	select {
	case <-r.release:
	case <-ctx.Done():
		return dotdecision.Decision{}, ctx.Err()
	}

	if len(view.RunnableItems) != 1 {
		return dotdecision.Decision{}, errors.New("expected exactly one runnable Dots item")
	}
	item := view.RunnableItems[0]
	if item.ID != "dots-durable-decision-loop-004" {
		return dotdecision.Decision{}, errors.New("current sealed context is not at D4")
	}
	return dotdecision.Decision{
		Kind:               dotdecision.ProposeNextGate,
		SnapshotDigest:     view.SnapshotDigest,
		WorkItemID:         item.ID,
		RequestedAuthority: item.Authority,
		RequestedAction:    "prepare-durable-decision-loop",
		Rationale:          "The sealed Portfolio Context admits D4 with PREPARE authority; commit the validated proposal as inert durable data.",
	}, nil
}

func (r *disconnectReasoner) Count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.calls
}

func TestCurrentPortfolioDecisionIsDurablyCommittedAndReplayed(t *testing.T) {
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
	if len(view.RunnableItems) != 1 || view.RunnableItems[0].ID != "dots-durable-decision-loop-004" {
		t.Fatalf("current runnable items=%#v", view.RunnableItems)
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

	const taskQueue = "portfolio-dot-d4"
	reasoner := newDisconnectReasoner()
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
		DecisionID:     "portfolio-dot-d4-current",
		SnapshotDigest: snapshot.SnapshotDigest,
	}

	firstCtx, cancelFirst := context.WithCancel(context.Background())
	firstDone := make(chan error, 1)
	go func() {
		_, err := executor.Decide(firstCtx, req)
		firstDone <- err
	}()

	select {
	case <-reasoner.started:
	case <-time.After(30 * time.Second):
		t.Fatal("D4 reasoner did not start")
	}
	cancelFirst()
	close(reasoner.release)

	select {
	case <-firstDone:
	case <-time.After(30 * time.Second):
		t.Fatal("disconnected caller did not return")
	}

	second, err := executor.Decide(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	if second.DecisionID != req.DecisionID ||
		second.SnapshotDigest != snapshot.SnapshotDigest ||
		second.WorkItemID != "dots-durable-decision-loop-004" ||
		second.Kind != dotdecision.ProposeNextGate ||
		second.RequestedAuthority != "PREPARE" {
		t.Fatalf("committed decision=%#v", second)
	}
	if len(second.DecisionDigest) != 64 {
		t.Fatalf("decision digest=%q", second.DecisionDigest)
	}
	if reasoner.Count() != 1 {
		t.Fatalf("reasoner calls=%d want=1", reasoner.Count())
	}

	persisted, err := store.GetCommittedDecision(ctx, req.DecisionID)
	if err != nil {
		t.Fatal(err)
	}
	if !dotdurable.SameRecord(persisted, second) {
		t.Fatalf("postgres decision=%#v workflow=%#v", persisted, second)
	}

	worker.Stop()
	third, err := executor.Decide(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	if !dotdurable.SameRecord(second, third) {
		t.Fatalf("replay changed committed decision: second=%#v third=%#v", second, third)
	}
	if reasoner.Count() != 1 {
		t.Fatalf("reasoner was recalled after committed replay: %d", reasoner.Count())
	}

	restartedStore, err := agentserver.NewPostgresStore(db)
	if err != nil {
		t.Fatal(err)
	}
	afterRestart, err := restartedStore.GetCommittedDecision(ctx, req.DecisionID)
	if err != nil {
		t.Fatal(err)
	}
	if !dotdurable.SameRecord(second, afterRestart) {
		t.Fatalf("restart changed decision: %#v", afterRestart)
	}

	var toolCount int
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM agent_server_tool_invocations").Scan(&toolCount); err != nil {
		t.Fatal(err)
	}
	if toolCount != 0 {
		t.Fatalf("D4 executed tools: count=%d", toolCount)
	}

	history := dev.Client().GetWorkflowHistory(
		ctx,
		"portfolio-dot-decision/"+req.DecisionID,
		"",
		false,
		enumspb.HISTORY_EVENT_FILTER_TYPE_ALL_EVENT,
	)
	for history.HasNext() {
		event, err := history.Next()
		if err != nil {
			t.Fatal(err)
		}
		encoded, err := json.Marshal(event)
		if err != nil {
			t.Fatal(err)
		}
		text := string(encoded)
		for _, forbidden := range []string{"SYSTEM-MAP.md", "portfolio/execution-queue.yaml", "source_bindings"} {
			if strings.Contains(text, forbidden) {
				t.Fatalf("Temporal history leaked raw Portfolio source identity %q: %s", forbidden, text)
			}
		}
	}

	if evidencePath := strings.TrimSpace(os.Getenv("DOT_DECISION_EVIDENCE_FILE")); evidencePath != "" {
		evidence := map[string]any{
			"state":               "PASS",
			"decision":            second,
			"reasoner_calls":      reasoner.Count(),
			"tool_invocations":    toolCount,
			"workflow_id":         "portfolio-dot-decision/" + req.DecisionID,
			"history_source_leak": false,
			"store_restart_match": true,
			"caller_rejoin_match": true,
		}
		raw, err := json.MarshalIndent(evidence, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(evidencePath, raw, 0o644); err != nil {
			t.Fatal(err)
		}
	}
}
