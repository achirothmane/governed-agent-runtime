//go:build integration

package a6

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
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	"go.temporal.io/sdk/testsuite"

	"github.com/achirothmane/governed-agent-runtime/agentloop"
	"github.com/achirothmane/governed-agent-runtime/agentserver"
	"github.com/achirothmane/governed-agent-runtime/integrations/dataengine"
	"github.com/achirothmane/governed-agent-runtime/mcptransport"
	runtimesdk "github.com/achirothmane/governed-agent-runtime/sdk"
	"github.com/achirothmane/governed-agent-runtime/temporaltools"
)

type bindingBackend struct{}

func (bindingBackend) Name() string { return "a6-binding" }
func (bindingBackend) Start(context.Context, runtimesdk.RunRequest) (runtimesdk.RunHandle, error) {
	return runtimesdk.RunHandle{}, errors.New("A6 binding backend does not start parent runs")
}
func (bindingBackend) Inspect(context.Context, runtimesdk.RunID) (runtimesdk.RunHandle, error) {
	return runtimesdk.RunHandle{}, errors.New("A6 binding backend does not inspect parent runs")
}
func (bindingBackend) Signal(context.Context, runtimesdk.RunID, string, []byte) error {
	return errors.New("A6 binding backend does not signal parent runs")
}
func (bindingBackend) Cancel(context.Context, runtimesdk.RunID, string) error {
	return errors.New("A6 binding backend does not cancel parent runs")
}

type profileReasoner struct {
	calls int
}

func (r *profileReasoner) Decide(_ context.Context, turn agentloop.Turn) (agentloop.Decision, error) {
	r.calls++
	switch turn.Step {
	case 1:
		return agentloop.Decision{
			Kind: agentloop.DecisionTool,
			Tool: dataengine.ProfileTool,
			Arguments: map[string]any{
				"identity_field": "id",
				"rows": []any{
					map[string]any{"id": "a", "price": 20.0, "secret": "a6-sensitive"},
					map[string]any{"id": "a", "price": 21.0, "secret": "a6-sensitive"},
				},
			},
		}, nil
	case 2:
		if len(turn.Observations) != 1 {
			return agentloop.Decision{}, errors.New("expected one tool observation")
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

func TestAgentLoopUsesRealTemporalAndDataEngine(t *testing.T) {
	dataEngineURL := strings.TrimSpace(os.Getenv("DATA_ENGINE_MCP_URL"))
	databaseURL := strings.TrimSpace(os.Getenv("DATABASE_URL"))
	if dataEngineURL == "" || databaseURL == "" {
		t.Skip("DATA_ENGINE_MCP_URL and DATABASE_URL are required")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
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

	const taskQueue = "a6-real-tools"
	provider := dataengine.Provider{
		Endpoint:  dataEngineURL,
		Connector: mcptransport.Connector{},
		Backend:   bindingBackend{},
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

	runtime, err := provider.Runtime(ctx, "data-profiler")
	if err != nil {
		t.Fatal(err)
	}
	const (
		conversationID = runtimesdk.ConversationID("conv-a6")
		runID          = runtimesdk.RunID("run-a6")
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
			Backend:     "a6-binding",
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

	executor := temporaltools.Executor{Client: dev.Client(), TaskQueue: taskQueue, Store: store}
	reasoner := &profileReasoner{}
	service, err := agentserver.New(agentserver.Config{
		Provider:          provider,
		DurableInvoker:    executor,
		Reasoner:          reasoner,
		AgentMaxSteps:     4,
		Store:             store,
		BearerToken:       "a6-secret",
		HeartbeatInterval: time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	httpServer := httptest.NewServer(service)
	defer httpServer.Close()

	request, err := http.NewRequest(http.MethodPost, httpServer.URL+"/v1/runs/run-a6/execute", bytes.NewReader(nil))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer a6-secret")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusOK {
		t.Fatalf("execute status=%d body=%s", response.StatusCode, raw)
	}
	var decoded struct {
		Outcome agentloop.Outcome `json:"outcome"`
	}
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Outcome.Kind != agentloop.OutcomeFinished || decoded.Outcome.Steps != 2 {
		t.Fatalf("outcome=%#v", decoded.Outcome)
	}
	if reasoner.calls != 2 || len(decoded.Outcome.Observations) != 1 {
		t.Fatalf("reasoner calls=%d outcome=%#v", reasoner.calls, decoded.Outcome)
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
		if strings.Contains(string(event.Payload), "a6-sensitive") {
			t.Fatalf("event leaked raw agent/tool data: %s", event.Payload)
		}
	}
}
