package agentserver

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	runtimesdk "github.com/achirothmane/governed-agent-runtime/sdk"
)

type fakeBackend struct {
	mu      sync.Mutex
	handles map[runtimesdk.RunID]runtimesdk.RunHandle
	signals []string
	cancels []string
}

func newFakeBackend() *fakeBackend {
	return &fakeBackend{handles: make(map[runtimesdk.RunID]runtimesdk.RunHandle)}
}

func (b *fakeBackend) Name() string { return "fake-durable" }

func (b *fakeBackend) Start(_ context.Context, run runtimesdk.RunRequest) (runtimesdk.RunHandle, error) {
	fingerprint, err := run.Fingerprint()
	if err != nil {
		return runtimesdk.RunHandle{}, err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if existing, ok := b.handles[run.ID]; ok {
		return existing, nil
	}
	handle := runtimesdk.RunHandle{
		ID:          run.ID,
		Backend:     b.Name(),
		ExternalID:  "external-" + string(run.ID),
		Fingerprint: fingerprint,
		State:       runtimesdk.RunQueued,
	}
	b.handles[run.ID] = handle
	return handle, nil
}

func (b *fakeBackend) Inspect(_ context.Context, id runtimesdk.RunID) (runtimesdk.RunHandle, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	handle := b.handles[id]
	handle.State = runtimesdk.RunRunning
	return handle, nil
}

func (b *fakeBackend) Signal(_ context.Context, id runtimesdk.RunID, name string, payload []byte) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.signals = append(b.signals, string(id)+":"+name+":"+string(payload))
	return nil
}

func (b *fakeBackend) Cancel(_ context.Context, id runtimesdk.RunID, reason string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.cancels = append(b.cancels, string(id)+":"+reason)
	return nil
}

func testService(t *testing.T) (*Service, *MemoryStore, *fakeBackend) {
	t.Helper()
	backend := newFakeBackend()
	catalog, err := runtimesdk.NewToolCatalog([]runtimesdk.ToolDescriptor{
		{Name: "repo.read", Protocol: runtimesdk.ToolProtocolNative, ReadOnly: true},
		{
			Name:           "knowledge.search",
			Protocol:       runtimesdk.ToolProtocolMCP,
			Endpoint:       "https://mcp.example.test",
			SnapshotDigest: strings.Repeat("a", 64),
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	runtime := runtimesdk.Runtime{
		Agent: runtimesdk.AgentSpec{
			ID:            "engineer",
			Mission:       "Inspect systems and prepare bounded work.",
			RequiredTools: []runtimesdk.ToolName{"repo.read", "knowledge.search"},
			Workspace:     runtimesdk.WorkspaceSpec{ID: "workspace-1", Kind: runtimesdk.WorkspaceRemote},
		},
		Tools:   catalog,
		Backend: backend,
	}
	store := NewMemoryStore()
	now := time.Date(2026, 10, 7, 9, 0, 0, 0, time.UTC)
	service, err := New(Config{
		Provider:    StaticProvider{Runtimes: map[runtimesdk.AgentID]runtimesdk.Runtime{"engineer": runtime}},
		Store:       store,
		BearerToken: "secret",
		Clock: func() time.Time {
			now = now.Add(time.Millisecond)
			return now
		},
		HeartbeatInterval: time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	return service, store, backend
}

func request(t *testing.T, service http.Handler, method, path, body string, auth bool) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if auth {
		req.Header.Set("Authorization", "Bearer secret")
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	rr := httptest.NewRecorder()
	service.ServeHTTP(rr, req)
	return rr
}

func TestServiceRequiresBearerAuthentication(t *testing.T) {
	service, _, _ := testService(t)
	rr := request(t, service, http.MethodPost, "/v1/conversations", `{"id":"c1","agent_id":"engineer"}`, false)
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusUnauthorized)
	}
}

func TestConversationRunInspectSignalCancelEndToEnd(t *testing.T) {
	service, store, backend := testService(t)

	rr := request(t, service, http.MethodPost, "/v1/conversations", `{"id":"c1","agent_id":"engineer"}`, true)
	if rr.Code != http.StatusCreated {
		t.Fatalf("create conversation status=%d body=%s", rr.Code, rr.Body.String())
	}

	rr = request(t, service, http.MethodPost, "/v1/conversations/c1/runs", `{"id":"r1","input":"inspect the repository"}`, true)
	if rr.Code != http.StatusCreated {
		t.Fatalf("start run status=%d body=%s", rr.Code, rr.Body.String())
	}
	var started runResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &started); err != nil {
		t.Fatal(err)
	}
	if !started.EventPersisted || started.Run.Handle.Fingerprint == "" {
		t.Fatalf("unexpected start response: %#v", started)
	}

	rr = request(t, service, http.MethodGet, "/v1/runs/r1", "", true)
	if rr.Code != http.StatusOK {
		t.Fatalf("inspect status=%d body=%s", rr.Code, rr.Body.String())
	}
	var inspected RunRecord
	if err := json.Unmarshal(rr.Body.Bytes(), &inspected); err != nil {
		t.Fatal(err)
	}
	if inspected.Handle.State != runtimesdk.RunRunning || inspected.Handle.Fingerprint != started.Run.Handle.Fingerprint {
		t.Fatalf("unexpected inspected run: %#v", inspected)
	}

	rr = request(t, service, http.MethodPost, "/v1/runs/r1/signals/approve", `{"payload":{"approved":true}}`, true)
	if rr.Code != http.StatusAccepted {
		t.Fatalf("signal status=%d body=%s", rr.Code, rr.Body.String())
	}
	rr = request(t, service, http.MethodPost, "/v1/runs/r1/cancel", `{"reason":"operator request"}`, true)
	if rr.Code != http.StatusAccepted {
		t.Fatalf("cancel status=%d body=%s", rr.Code, rr.Body.String())
	}

	backend.mu.Lock()
	if len(backend.signals) != 1 || len(backend.cancels) != 1 {
		t.Fatalf("signals=%#v cancels=%#v", backend.signals, backend.cancels)
	}
	backend.mu.Unlock()

	events, err := store.ListEvents(context.Background(), "c1", 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 3 {
		t.Fatalf("event count=%d, want 3: %#v", len(events), events)
	}
	if events[0].Type != runtimesdk.EventRunStarted || events[1].Type != eventRunSignaled || events[2].Type != eventRunCancelRequested {
		t.Fatalf("unexpected events: %#v", events)
	}
}

func TestRunIDCannotBeReboundToDifferentInput(t *testing.T) {
	service, _, _ := testService(t)
	if rr := request(t, service, http.MethodPost, "/v1/conversations", `{"id":"c1","agent_id":"engineer"}`, true); rr.Code != http.StatusCreated {
		t.Fatalf("create status=%d body=%s", rr.Code, rr.Body.String())
	}
	if rr := request(t, service, http.MethodPost, "/v1/conversations/c1/runs", `{"id":"r1","input":"first"}`, true); rr.Code != http.StatusCreated {
		t.Fatalf("start status=%d body=%s", rr.Code, rr.Body.String())
	}
	rr := request(t, service, http.MethodPost, "/v1/conversations/c1/runs", `{"id":"r1","input":"different"}`, true)
	if rr.Code != http.StatusConflict {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
}

func TestEventStreamReplaysFromCursor(t *testing.T) {
	service, _, _ := testService(t)
	if rr := request(t, service, http.MethodPost, "/v1/conversations", `{"id":"c1","agent_id":"engineer"}`, true); rr.Code != http.StatusCreated {
		t.Fatalf("create status=%d body=%s", rr.Code, rr.Body.String())
	}
	if rr := request(t, service, http.MethodPost, "/v1/conversations/c1/runs", `{"id":"r1","input":"first"}`, true); rr.Code != http.StatusCreated {
		t.Fatalf("start status=%d body=%s", rr.Code, rr.Body.String())
	}
	if rr := request(t, service, http.MethodPost, "/v1/runs/r1/signals/continue", `{"payload":{"n":1}}`, true); rr.Code != http.StatusAccepted {
		t.Fatalf("signal status=%d body=%s", rr.Code, rr.Body.String())
	}

	server := httptest.NewServer(service)
	defer server.Close()
	ctx, cancel := context.WithCancel(context.Background())
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, server.URL+"/v1/conversations/c1/events?after=1", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer secret")
	resp, err := server.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("stream status=%d body=%s", resp.StatusCode, body)
	}

	reader := bufio.NewReader(resp.Body)
	var block bytes.Buffer
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			t.Fatal(err)
		}
		block.WriteString(line)
		if line == "\n" {
			break
		}
	}
	cancel()
	got := block.String()
	if !strings.Contains(got, "id: 2\n") || !strings.Contains(got, "event: run.signaled\n") {
		t.Fatalf("unexpected SSE block: %q", got)
	}
	if strings.Contains(got, "id: 1\n") {
		t.Fatalf("cursor replayed an already acknowledged event: %q", got)
	}
}

func TestMemoryStoreRejectsConversationRebinding(t *testing.T) {
	store := NewMemoryStore()
	at := time.Now().UTC()
	first := Conversation{Ref: runtimesdk.ConversationRef{ID: "c", AgentID: "a", WorkspaceID: "w1"}, CreatedAt: at}
	if _, _, err := store.PutConversation(context.Background(), first); err != nil {
		t.Fatal(err)
	}
	second := Conversation{Ref: runtimesdk.ConversationRef{ID: "c", AgentID: "a", WorkspaceID: "w2"}, CreatedAt: at}
	if _, _, err := store.PutConversation(context.Background(), second); !errors.Is(err, ErrConflict) {
		t.Fatalf("error=%v, want conflict", err)
	}
}

func TestMemoryStoreRejectsEventForWrongConversation(t *testing.T) {
	store := NewMemoryStore()
	at := time.Now().UTC()
	for _, id := range []runtimesdk.ConversationID{"c1", "c2"} {
		conversation := Conversation{Ref: runtimesdk.ConversationRef{ID: id, AgentID: "a", WorkspaceID: "w"}, CreatedAt: at}
		if _, _, err := store.PutConversation(context.Background(), conversation); err != nil {
			t.Fatal(err)
		}
	}
	record := RunRecord{
		ID:             "r1",
		ConversationID: "c1",
		AgentID:        "a",
		Input:          "work",
		InputDigest:    InputDigest("work"),
		Handle:         runtimesdk.RunHandle{ID: "r1", Backend: "b", Fingerprint: "fp", State: runtimesdk.RunQueued},
		CreatedAt:      at,
	}
	if _, _, err := store.PutRun(context.Background(), record); err != nil {
		t.Fatal(err)
	}
	_, err := store.AppendEvent(context.Background(), runtimesdk.EventEnvelope{
		Type:           runtimesdk.EventRunStarted,
		RunID:          "r1",
		ConversationID: "c2",
		OccurredAt:     at,
	})
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("error=%v, want conflict", err)
	}
}
