package sdk

import (
	"context"
	"errors"
	"strings"
	"testing"
)

type controlBackend struct {
	inspect RunHandle
	signal  struct {
		id      RunID
		name    string
		payload []byte
	}
	cancel struct {
		id     RunID
		reason string
	}
}

func (b *controlBackend) Name() string { return "control" }
func (b *controlBackend) Start(context.Context, RunRequest) (RunHandle, error) {
	return RunHandle{}, errors.New("not used")
}
func (b *controlBackend) Inspect(context.Context, RunID) (RunHandle, error) { return b.inspect, nil }
func (b *controlBackend) Signal(_ context.Context, id RunID, name string, payload []byte) error {
	b.signal.id = id
	b.signal.name = name
	b.signal.payload = append([]byte(nil), payload...)
	return nil
}
func (b *controlBackend) Cancel(_ context.Context, id RunID, reason string) error {
	b.cancel.id = id
	b.cancel.reason = reason
	return nil
}

func TestRuntimeControlSurfaceValidatesBackendHandles(t *testing.T) {
	backend := &controlBackend{inspect: RunHandle{
		ID:          "run-1",
		Backend:     "control",
		Fingerprint: "fingerprint",
		State:       RunRunning,
	}}
	runtime := Runtime{Backend: backend}

	handle, err := runtime.Inspect(context.Background(), "run-1")
	if err != nil {
		t.Fatal(err)
	}
	if handle.State != RunRunning {
		t.Fatalf("state=%s, want RUNNING", handle.State)
	}
	if err := runtime.Signal(context.Background(), "run-1", "approve", []byte(`{"ok":true}`)); err != nil {
		t.Fatal(err)
	}
	if backend.signal.id != "run-1" || backend.signal.name != "approve" {
		t.Fatalf("unexpected signal: %#v", backend.signal)
	}
	if err := runtime.Cancel(context.Background(), "run-1", "operator request"); err != nil {
		t.Fatal(err)
	}
	if backend.cancel.id != "run-1" || backend.cancel.reason != "operator request" {
		t.Fatalf("unexpected cancel: %#v", backend.cancel)
	}
}

func TestRuntimeStartRejectsUnboundMCPTool(t *testing.T) {
	catalog, err := NewToolCatalog([]ToolDescriptor{
		{Name: "knowledge", Protocol: ToolProtocolMCP, Endpoint: "https://mcp.example.test"},
	})
	if err != nil {
		t.Fatal(err)
	}
	runtime := Runtime{
		Agent: AgentSpec{
			ID:            "agent",
			Mission:       "Use bounded capabilities.",
			RequiredTools: []ToolName{"knowledge"},
			Workspace:     WorkspaceSpec{ID: "ws", Kind: WorkspaceRemote},
		},
		Tools:   catalog,
		Backend: &fakeBackend{},
	}
	_, err = runtime.Start(context.Background(), StartRequest{RunID: "run", ConversationID: "conv", Input: "work"})
	if err == nil || !strings.Contains(err.Error(), "capability snapshot") {
		t.Fatalf("error=%v, want missing MCP capability snapshot", err)
	}
}
