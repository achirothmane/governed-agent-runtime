//go:build integration

package a5_2

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

	"github.com/achirothmane/governed-agent-runtime/agentserver"
	"github.com/achirothmane/governed-agent-runtime/integrations/dataengine"
	"github.com/achirothmane/governed-agent-runtime/mcptransport"
	runtimesdk "github.com/achirothmane/governed-agent-runtime/sdk"
	"github.com/achirothmane/governed-agent-runtime/temporaltools"
)

type bindingBackend struct{}

func (bindingBackend) Name() string { return "a5-2-binding" }
func (bindingBackend) Start(context.Context, runtimesdk.RunRequest) (runtimesdk.RunHandle, error) {
	return runtimesdk.RunHandle{}, errors.New("A5.2 binding backend does not start parent runs")
}
func (bindingBackend) Inspect(context.Context, runtimesdk.RunID) (runtimesdk.RunHandle, error) {
	return runtimesdk.RunHandle{}, errors.New("A5.2 binding backend does not inspect parent runs")
}
func (bindingBackend) Signal(context.Context, runtimesdk.RunID, string, []byte) error {
	return errors.New("A5.2 binding backend does not signal parent runs")
}
func (bindingBackend) Cancel(context.Context, runtimesdk.RunID, string) error {
	return errors.New("A5.2 binding backend does not cancel parent runs")
}

func TestRealWorkerDataEngineEndToEnd(t *testing.T) {
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

	const taskQueue = "a5-2-real-tools"
	provider := dataengine.Provider{
		Endpoint:  dataEngineURL,
		Connector: mcptransport.Connector{},
		Backend:   bindingBackend{},
	}
	activityWorker, err := temporaltools.NewWorker(temporaltools.WorkerConfig{
		Client:    dev.Client(),
		TaskQueue: taskQueue,
		Store:     store,
		Invoker:   provider,
		Evidence:  agentserver.ToolActivityEvidence{Store: store},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := activityWorker.Start(); err != nil {
		t.Fatal(err)
	}
	defer activityWorker.Stop()

	runtime, err := provider.Runtime(ctx, "data-profiler")
	if err != nil {
		t.Fatal(err)
	}

	const (
		conversationID = runtimesdk.ConversationID("conv-a5-2")
		runID          = runtimesdk.RunID("run-a5-2")
		input          = "profile the supplied raw rows"
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
			Backend:     "a5-2-binding",
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

	executor := temporaltools.Executor{
		Client:    dev.Client(),
		TaskQueue: taskQueue,
		Store:     store,
	}
	service, err := agentserver.New(agentserver.Config{
		Provider:          provider,
		DurableInvoker:    executor,
		Store:             store,
		BearerToken:       "a5-2-secret",
		HeartbeatInterval: time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	httpServer := httptest.NewServer(service)
	defer httpServer.Close()

	requestBody := map[string]any{
		"invocation_id": "profile-e2e-1",
		"arguments": map[string]any{
			"identity_field": "id",
			"rows": []any{
				map[string]any{"id": "a", "price": 20.0, "secret": "e2e-sensitive"},
				map[string]any{"id": "a", "price": 21.0, "secret": "e2e-sensitive"},
			},
		},
	}

	first := callTool(t, httpServer.URL, requestBody)
	if first.EventPersisted {
		t.Fatal("HTTP response must not claim Temporal activity evidence persistence")
	}
	if first.Result.Tool != dataengine.ProfileTool || first.Result.SnapshotDigest == "" {
		t.Fatalf("unexpected result binding: %#v", first.Result)
	}
	var profile struct {
		Stage    string `json:"stage"`
		Decision string `json:"decision"`
	}
	if err := json.Unmarshal(first.Result.StructuredContent, &profile); err != nil {
		t.Fatal(err)
	}
	if profile.Stage != "PRE_SEMANTIC_PROFILE" || profile.Decision != "CONFLICTING" {
		t.Fatalf("profile=%#v", profile)
	}

	invocation, err := store.Get(ctx, "profile-e2e-1")
	if err != nil {
		t.Fatal(err)
	}
	if invocation.State != temporaltools.InvocationComplete || invocation.Result == nil || invocation.ResultDigest == "" {
		t.Fatalf("invocation=%#v", invocation)
	}

	events, err := store.ListEvents(ctx, conversationID, 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 || events[0].Type != runtimesdk.EventToolCalled || events[1].Type != runtimesdk.EventToolReturned {
		t.Fatalf("events=%#v", events)
	}
	for _, event := range events {
		if strings.Contains(string(event.Payload), "e2e-sensitive") {
			t.Fatalf("event leaked raw Data Engine input: %s", event.Payload)
		}
	}

	second := callTool(t, httpServer.URL, requestBody)
	if second.Result.SnapshotDigest != first.Result.SnapshotDigest {
		t.Fatalf("retry snapshot changed: first=%s second=%s", first.Result.SnapshotDigest, second.Result.SnapshotDigest)
	}
	eventsAfterRetry, err := store.ListEvents(ctx, conversationID, 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(eventsAfterRetry) != 2 {
		t.Fatalf("idempotent retry created new activity evidence: %#v", eventsAfterRetry)
	}
}

type invokeResponse struct {
	Result         mcptransport.Result `json:"result"`
	EventPersisted bool                `json:"event_persisted"`
}

func callTool(t *testing.T, baseURL string, body any) invokeResponse {
	t.Helper()
	payload, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	request, err := http.NewRequest(http.MethodPost, baseURL+"/v1/runs/run-a5-2/tools/data.profile", bytes.NewReader(payload))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer a5-2-secret")
	request.Header.Set("Content-Type", "application/json")
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
		t.Fatalf("tool status=%d body=%s", response.StatusCode, raw)
	}
	var decoded invokeResponse
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	return decoded
}
