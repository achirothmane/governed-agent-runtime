//go:build integration

package a7

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
	openai "github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
	"github.com/openai/openai-go/v3/responses"
	"go.temporal.io/sdk/testsuite"

	"github.com/achirothmane/governed-agent-runtime/agentloop"
	"github.com/achirothmane/governed-agent-runtime/agentserver"
	"github.com/achirothmane/governed-agent-runtime/integrations/dataengine"
	"github.com/achirothmane/governed-agent-runtime/mcptransport"
	openairesponses "github.com/achirothmane/governed-agent-runtime/reasoners/openairesponses"
	runtimesdk "github.com/achirothmane/governed-agent-runtime/sdk"
	"github.com/achirothmane/governed-agent-runtime/temporalagent"
	"github.com/achirothmane/governed-agent-runtime/temporaltools"
)

type bindingBackend struct{}

func (bindingBackend) Name() string { return "a7-binding" }
func (bindingBackend) Start(context.Context, runtimesdk.RunRequest) (runtimesdk.RunHandle, error) {
	return runtimesdk.RunHandle{}, errors.New("A7 binding backend does not start parent runs")
}
func (bindingBackend) Inspect(context.Context, runtimesdk.RunID) (runtimesdk.RunHandle, error) {
	return runtimesdk.RunHandle{}, errors.New("A7 binding backend does not inspect parent runs")
}
func (bindingBackend) Signal(context.Context, runtimesdk.RunID, string, []byte) error {
	return errors.New("A7 binding backend does not signal parent runs")
}
func (bindingBackend) Cancel(context.Context, runtimesdk.RunID, string) error {
	return errors.New("A7 binding backend does not cancel parent runs")
}

type sdkResponsesClient struct {
	client openai.Client
}

func (c sdkResponsesClient) Create(ctx context.Context, params responses.ResponseNewParams) (string, error) {
	response, err := c.client.Responses.New(ctx, params)
	if err != nil {
		return "", err
	}
	return response.OutputText(), nil
}

type fakeProvider struct {
	mu       sync.Mutex
	calls    int
	requests [][]byte
}

func (p *fakeProvider) handler(w http.ResponseWriter, req *http.Request) {
	if req.Method != http.MethodPost || req.URL.Path != "/responses" {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	if req.Header.Get("Authorization") != "Bearer test-key" {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	raw, err := io.ReadAll(req.Body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	p.mu.Lock()
	p.calls++
	call := p.calls
	p.requests = append(p.requests, append([]byte(nil), raw...))
	p.mu.Unlock()

	var body struct {
		Input string `json:"input"`
		Store bool   `json:"store"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	for _, forbidden := range []string{
		"DATA_ENGINE_MCP_URL",
		"ai-native-agent/",
		"ai-native-tool/",
		"conv-a7",
		"run-a7",
	} {
		if strings.Contains(string(raw), forbidden) {
			http.Error(w, "authority/internal identity leaked: "+forbidden, http.StatusBadRequest)
			return
		}
	}
	if body.Store {
		http.Error(w, "store must be false", http.StatusBadRequest)
		return
	}

	var decision string
	switch call {
	case 1:
		if !strings.Contains(body.Input, "data.profile") ||
			!strings.Contains(body.Input, "input_schema") ||
			!strings.Contains(body.Input, "Profile raw tabular rows") {
			http.Error(w, "bound Data Engine tool contract missing", http.StatusBadRequest)
			return
		}
		decision = `{"kind":"TOOL","tool":"data.profile","arguments_json":"{\"identity_field\":\"id\",\"rows\":[{\"id\":\"a\",\"price\":20},{\"id\":\"a\",\"price\":21}]}","message":""}`
	case 2:
		if !strings.Contains(body.Input, "PRE_SEMANTIC_PROFILE") ||
			!strings.Contains(body.Input, "CONFLICTING") {
			http.Error(w, "normalized Data Engine observation missing", http.StatusBadRequest)
			return
		}
		decision = `{"kind":"FINISH","tool":"","arguments_json":"{}","message":"The raw evidence conflicts for the same identity."}`
	default:
		http.Error(w, "unexpected extra model call", http.StatusConflict)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	encodedDecision, _ := json.Marshal(decision)
	_, _ = io.WriteString(w, `{
		"id":"resp_a7",
		"object":"response",
		"created_at":1,
		"status":"completed",
		"model":"test-model",
		"output":[{
			"id":"msg_a7",
			"type":"message",
			"role":"assistant",
			"status":"completed",
			"content":[{
				"type":"output_text",
				"text":`+string(encodedDecision)+`,
				"annotations":[]
			}]
		}]
	}`)
}

func (p *fakeProvider) callCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.calls
}

func TestOpenAIAdapterDrivesDurableDataEngineLoop(t *testing.T) {
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

	fake := &fakeProvider{}
	modelServer := httptest.NewServer(http.HandlerFunc(fake.handler))
	defer modelServer.Close()

	openAIClient := openai.NewClient(
		option.WithUnsafeAllowHTTP(),
		option.WithBaseURL(modelServer.URL),
		option.WithAPIKey("test-key"),
		option.WithMaxRetries(0),
	)
	reasoner, err := openairesponses.NewWithClient(
		openairesponses.Config{Model: "test-model"},
		sdkResponsesClient{client: openAIClient},
	)
	if err != nil {
		t.Fatal(err)
	}

	const (
		toolQueue  = "a7-real-tools"
		agentQueue = "a7-real-agent"
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
	if len(runtime.Agent.RequiredTools) != 1 {
		t.Fatalf("required tools=%#v", runtime.Agent.RequiredTools)
	}
	boundTools, err := runtime.Tools.Resolve(runtime.Agent.RequiredTools)
	if err != nil {
		t.Fatal(err)
	}
	if len(boundTools) != 1 || len(boundTools[0].InputSchema) == 0 {
		t.Fatalf("bound tool schema missing: %#v", boundTools)
	}

	const (
		conversationID = runtimesdk.ConversationID("conv-a7")
		runID          = runtimesdk.RunID("run-a7")
		input          = "Profile the supplied evidence and report whether identical raw identities conflict."
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
			Backend:     "a7-binding",
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
		BearerToken:       "a7-secret",
		HeartbeatInterval: time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	httpServer := httptest.NewServer(service)
	defer httpServer.Close()

	req, err := http.NewRequest(http.MethodPost, httpServer.URL+"/v1/runs/run-a7/execute", bytes.NewReader(nil))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer a7-secret")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("execute status=%d body=%s", resp.StatusCode, raw)
	}

	var decoded struct {
		Outcome agentloop.Outcome `json:"outcome"`
	}
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Outcome.Kind != agentloop.OutcomeFinished ||
		decoded.Outcome.Steps != 2 ||
		len(decoded.Outcome.Observations) != 1 ||
		decoded.Outcome.Message != "The raw evidence conflicts for the same identity." {
		t.Fatalf("outcome=%#v", decoded.Outcome)
	}
	if fake.callCount() != 2 {
		t.Fatalf("model calls=%d want 2", fake.callCount())
	}

	first, err := store.GetDecision(ctx, runID, 1)
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.GetDecision(ctx, runID, 2)
	if err != nil {
		t.Fatal(err)
	}
	if first.Kind != agentloop.DecisionTool || first.Tool != dataengine.ProfileTool ||
		second.Kind != agentloop.DecisionFinish {
		t.Fatalf("committed decisions first=%#v second=%#v", first, second)
	}

	invocation := decoded.Outcome.Observations[0]
	storedInvocation, err := store.Get(ctx, invocation.InvocationID)
	if err != nil {
		t.Fatal(err)
	}
	if storedInvocation.State != temporaltools.InvocationComplete || storedInvocation.Result == nil {
		t.Fatalf("stored invocation=%#v", storedInvocation)
	}
	if !strings.Contains(string(storedInvocation.Result.StructuredContent), "CONFLICTING") {
		t.Fatalf("result=%s", storedInvocation.Result.StructuredContent)
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
}
