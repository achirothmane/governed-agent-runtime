//go:build integration

package a6_1

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
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

	"github.com/achirothmane/governed-agent-runtime/agentloop"
	"github.com/achirothmane/governed-agent-runtime/agentserver"
	"github.com/achirothmane/governed-agent-runtime/integrations/dataengine"
	"github.com/achirothmane/governed-agent-runtime/mcptransport"
	runtimesdk "github.com/achirothmane/governed-agent-runtime/sdk"
	"github.com/achirothmane/governed-agent-runtime/temporalagent"
	"github.com/achirothmane/governed-agent-runtime/temporaltools"
)

type bindingBackend struct{}

func (bindingBackend) Name() string { return "a6-1-binding" }
func (bindingBackend) Start(context.Context, runtimesdk.RunRequest) (runtimesdk.RunHandle, error) {
	return runtimesdk.RunHandle{}, errors.New("A6.1 binding backend does not start parent runs")
}
func (bindingBackend) Inspect(context.Context, runtimesdk.RunID) (runtimesdk.RunHandle, error) {
	return runtimesdk.RunHandle{}, errors.New("A6.1 binding backend does not inspect parent runs")
}
func (bindingBackend) Signal(context.Context, runtimesdk.RunID, string, []byte) error {
	return errors.New("A6.1 binding backend does not signal parent runs")
}
func (bindingBackend) Cancel(context.Context, runtimesdk.RunID, string) error {
	return errors.New("A6.1 binding backend does not cancel parent runs")
}

type disconnectReasoner struct {
	started chan struct{}
	release chan struct{}
	once    sync.Once

	mu    sync.Mutex
	calls map[int]int
}

func newDisconnectReasoner() *disconnectReasoner {
	return &disconnectReasoner{
		started: make(chan struct{}),
		release: make(chan struct{}),
		calls:   map[int]int{},
	}
}

func (r *disconnectReasoner) Decide(ctx context.Context, turn agentloop.Turn) (agentloop.Decision, error) {
	r.mu.Lock()
	r.calls[turn.Step]++
	r.mu.Unlock()

	switch turn.Step {
	case 1:
		r.once.Do(func() { close(r.started) })
		select {
		case <-r.release:
		case <-ctx.Done():
			return agentloop.Decision{}, ctx.Err()
		}
		return agentloop.Decision{
			Kind: agentloop.DecisionTool,
			Tool: dataengine.ProfileTool,
			Arguments: map[string]any{
				"identity_field": "id",
				"rows": []any{
					map[string]any{"id": "a", "price": 20.0, "secret": "a6-1-sensitive"},
					map[string]any{"id": "a", "price": 21.0, "secret": "a6-1-sensitive"},
				},
			},
		}, nil
	case 2:
		if len(turn.Observations) != 1 {
			return agentloop.Decision{}, errors.New("expected one durable observation")
		}
		var profile struct {
			Stage    string `json:"stage"`
			Decision string `json:"decision"`
		}
		if err := json.Unmarshal(turn.Observations[0].Result.StructuredContent, &profile); err != nil {
			return agentloop.Decision{}, err
		}
		if profile.Stage != "PRE_SEMANTIC_PROFILE" || profile.Decision != "CONFLICTING" {
			return agentloop.Decision{}, errors.New("unexpected Data Engine observation")
		}
		return agentloop.Decision{
			Kind:    agentloop.DecisionFinish,
			Message: "The supplied rows conflict for the same raw identity.",
		}, nil
	default:
		return agentloop.Decision{Kind: agentloop.DecisionFail, Message: "unexpected extra reasoning step"}, nil
	}
}

func (r *disconnectReasoner) callCount(step int) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.calls[step]
}

func TestDurableAgentSurvivesCallerDisconnectAndResumesSameExecution(t *testing.T) {
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
	if _, err := db.ExecContext(ctx, "TRUNCATE agent_server_reasoning_steps, agent_server_agent_executions, agent_server_tool_invocations, agent_server_events, agent_server_runs, agent_server_conversations CASCADE"); err != nil {
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

	const (
		toolQueue  = "a6-1-real-tools"
		agentQueue = "a6-1-real-agent"
	)
	provider := dataengine.Provider{
		Endpoint:  dataEngineURL,
		Connector: mcptransport.Connector{},
		Backend:   bindingBackend{},
	}
	toolWorker, err := temporaltools.NewWorker(temporaltools.WorkerConfig{
		Client:    dev.Client(),
		TaskQueue: toolQueue,
		Store:     store,
		Invoker:   provider,
		Evidence:  agentserver.ToolActivityEvidence{Store: store},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := toolWorker.Start(); err != nil {
		t.Fatal(err)
	}
	defer toolWorker.Stop()

	toolExecutor := temporaltools.Executor{
		Client:    dev.Client(),
		TaskQueue: toolQueue,
		Store:     store,
	}
	reasoner := newDisconnectReasoner()
	agentWorker, err := temporalagent.NewWorker(temporalagent.WorkerConfig{
		Client:       dev.Client(),
		TaskQueue:    agentQueue,
		Resolver:     agentserver.BoundRunResolver{Provider: provider, Store: store},
		Reasoner:     reasoner,
		Executions:   store,
		Steps:        store,
		Invocations:  store,
		ToolExecutor: toolExecutor,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := agentWorker.Start(); err != nil {
		t.Fatal(err)
	}
	defer agentWorker.Stop()

	runtime, err := provider.Runtime(ctx, "data-profiler")
	if err != nil {
		t.Fatal(err)
	}
	const (
		conversationID = runtimesdk.ConversationID("conv-a6-1")
		runID          = runtimesdk.RunID("run-a6-1")
		input          = "Inspect the supplied rows and report whether the raw evidence conflicts."
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
	_, fingerprint, err := runtime.Bind(runtimesdk.StartRequest{
		RunID:          runID,
		ConversationID: conversationID,
		Input:          input,
	})
	if err != nil {
		t.Fatal(err)
	}
	record, err := agentserver.NewRunRecord(
		conversation,
		input,
		runtimesdk.RunHandle{
			ID:          runID,
			Backend:     "a6-1-binding",
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

	agentExecutor := temporalagent.Executor{
		Client:      dev.Client(),
		TaskQueue:   agentQueue,
		Executions:  store,
		Invocations: store,
		MaxSteps:    4,
	}
	service, err := agentserver.New(agentserver.Config{
		Provider:          provider,
		AgentRunner:       agentExecutor,
		Store:             store,
		BearerToken:       "a6-1-secret",
		HeartbeatInterval: time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	httpServer := httptest.NewServer(service)
	defer httpServer.Close()

	firstCtx, cancelFirst := context.WithCancel(context.Background())
	firstReq, err := http.NewRequestWithContext(firstCtx, http.MethodPost, httpServer.URL+"/v1/runs/run-a6-1/execute", bytes.NewReader(nil))
	if err != nil {
		t.Fatal(err)
	}
	firstReq.Header.Set("Authorization", "Bearer a6-1-secret")
	firstDone := make(chan error, 1)
	go func() {
		resp, err := http.DefaultClient.Do(firstReq)
		if resp != nil {
			_, _ = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()
		}
		firstDone <- err
	}()

	select {
	case <-reasoner.started:
	case <-time.After(30 * time.Second):
		t.Fatal("reasoner did not start")
	}
	cancelFirst()
	close(reasoner.release)

	select {
	case err := <-firstDone:
		if err == nil {
			t.Log("first request completed at the disconnect boundary; continuing recovery proof")
		}
	case <-time.After(30 * time.Second):
		t.Fatal("first request did not release after cancellation")
	}

	secondReq, err := http.NewRequest(http.MethodPost, httpServer.URL+"/v1/runs/run-a6-1/execute", bytes.NewReader(nil))
	if err != nil {
		t.Fatal(err)
	}
	secondReq.Header.Set("Authorization", "Bearer a6-1-secret")
	secondResp, err := http.DefaultClient.Do(secondReq)
	if err != nil {
		t.Fatal(err)
	}
	defer secondResp.Body.Close()
	raw, err := io.ReadAll(secondResp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if secondResp.StatusCode != http.StatusOK {
		t.Fatalf("recovery execute status=%d body=%s", secondResp.StatusCode, raw)
	}
	var decoded struct {
		Outcome agentloop.Outcome `json:"outcome"`
	}
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Outcome.Kind != agentloop.OutcomeFinished || decoded.Outcome.Steps != 2 || len(decoded.Outcome.Observations) != 1 {
		t.Fatalf("outcome=%#v", decoded.Outcome)
	}
	if reasoner.callCount(1) != 1 || reasoner.callCount(2) != 1 {
		t.Fatalf("reasoner calls step1=%d step2=%d", reasoner.callCount(1), reasoner.callCount(2))
	}

	execution, err := store.GetExecution(ctx, runID)
	if err != nil {
		t.Fatal(err)
	}
	if execution.MaxSteps != 4 || execution.RunFingerprint != fingerprint {
		t.Fatalf("execution=%#v", execution)
	}
	for step := 1; step <= 2; step++ {
		decision, err := store.GetDecision(ctx, runID, step)
		if err != nil {
			t.Fatal(err)
		}
		if decision.DecisionDigest == "" {
			t.Fatalf("step %d has no committed decision digest", step)
		}
	}
	invocationID := decoded.Outcome.Observations[0].InvocationID
	invocation, err := store.Get(ctx, invocationID)
	if err != nil {
		t.Fatal(err)
	}
	if invocation.State != temporaltools.InvocationComplete || invocation.Result == nil {
		t.Fatalf("invocation=%#v", invocation)
	}

	events, err := store.ListEvents(ctx, conversationID, 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 3 ||
		events[0].Type != runtimesdk.EventToolCalled ||
		events[1].Type != runtimesdk.EventToolReturned ||
		events[2].Type != runtimesdk.EventRunCompleted {
		t.Fatalf("events=%#v", events)
	}
	for _, event := range events {
		if strings.Contains(string(event.Payload), "a6-1-sensitive") {
			t.Fatalf("event leaked raw durable agent data: %s", event.Payload)
		}
	}
}
