package sdk

import (
	"context"
	"errors"
	"testing"
)

type fakeBackend struct {
	started *RunRequest
	handle  RunHandle
	err     error
}

func (f *fakeBackend) Name() string { return "fake-durable" }

func (f *fakeBackend) Start(_ context.Context, run RunRequest) (RunHandle, error) {
	if f.err != nil {
		return RunHandle{}, f.err
	}
	copy := run
	f.started = &copy
	fingerprint, err := run.Fingerprint()
	if err != nil {
		return RunHandle{}, err
	}
	if f.handle.ID == "" {
		return RunHandle{
			ID:          run.ID,
			Backend:     f.Name(),
			ExternalID:  "workflow/run-1",
			Fingerprint: fingerprint,
			State:       RunQueued,
		}, nil
	}
	return f.handle, nil
}

func (f *fakeBackend) Inspect(context.Context, RunID) (RunHandle, error)   { return f.handle, nil }
func (f *fakeBackend) Signal(context.Context, RunID, string, []byte) error { return nil }
func (f *fakeBackend) Cancel(context.Context, RunID, string) error         { return nil }

func TestRuntimeStartBindsAgentWorkspaceToolsAndConversation(t *testing.T) {
	catalog, err := NewToolCatalog([]ToolDescriptor{
		{Name: "github", Protocol: ToolProtocolNative},
		{Name: "knowledge", Protocol: ToolProtocolMCP, Endpoint: "https://mcp.example.test"},
	})
	if err != nil {
		t.Fatal(err)
	}
	backend := &fakeBackend{}
	runtime := Runtime{
		Agent: AgentSpec{
			ID:            "release-engineer",
			Mission:       "Diagnose failing CI and prepare a verified repair.",
			RequiredTools: []ToolName{"knowledge", "github"},
			Workspace: WorkspaceSpec{
				ID:   "repo-42",
				Kind: WorkspaceEphemeral,
			},
			PolicyRef: "effects/default",
		},
		Tools:   catalog,
		Backend: backend,
	}

	handle, err := runtime.Start(context.Background(), StartRequest{
		RunID:          "run-7",
		ConversationID: "conv-3",
		Input:          "Repair the failing test suite.",
	})
	if err != nil {
		t.Fatal(err)
	}
	if handle.State != RunQueued {
		t.Fatalf("expected queued handle, got %s", handle.State)
	}
	if backend.started == nil {
		t.Fatal("backend was not started")
	}
	if backend.started.Conversation.AgentID != "release-engineer" {
		t.Fatalf("unexpected agent binding: %#v", backend.started.Conversation)
	}
	if backend.started.Workspace.ID != "repo-42" || backend.started.Conversation.WorkspaceID != "repo-42" {
		t.Fatalf("workspace binding was not preserved: %#v", backend.started)
	}
	if got := backend.started.Tools; len(got) != 2 || got[0].Name != "github" || got[1].Name != "knowledge" {
		t.Fatalf("expected canonical tool order, got %#v", got)
	}
	if backend.started.Tools[1].Protocol != ToolProtocolMCP {
		t.Fatalf("expected MCP tool to remain an explicit protocol boundary")
	}
}

func TestRuntimeFailsClosedWhenRequiredToolIsUnavailable(t *testing.T) {
	catalog, err := NewToolCatalog([]ToolDescriptor{{Name: "github", Protocol: ToolProtocolNative}})
	if err != nil {
		t.Fatal(err)
	}
	backend := &fakeBackend{}
	runtime := Runtime{
		Agent: AgentSpec{
			ID:            "researcher",
			Mission:       "Research evidence.",
			RequiredTools: []ToolName{"github", "knowledge"},
			Workspace:     WorkspaceSpec{ID: "ws", Kind: WorkspaceRemote},
		},
		Tools:   catalog,
		Backend: backend,
	}

	_, err = runtime.Start(context.Background(), StartRequest{RunID: "run", ConversationID: "conv", Input: "Investigate."})
	if !errors.Is(err, ErrToolUnavailable) {
		t.Fatalf("expected ErrToolUnavailable, got %v", err)
	}
	if backend.started != nil {
		t.Fatal("backend must not start when required capabilities are unavailable")
	}
}

func TestRuntimeRejectsBackendThatChangesBoundRun(t *testing.T) {
	catalog, err := NewToolCatalog(nil)
	if err != nil {
		t.Fatal(err)
	}
	backend := &fakeBackend{handle: RunHandle{
		ID:          "other-run",
		Backend:     "fake-durable",
		Fingerprint: "wrong",
		State:       RunQueued,
	}}
	runtime := Runtime{
		Agent: AgentSpec{
			ID:        "agent",
			Mission:   "Do bounded work.",
			Workspace: WorkspaceSpec{ID: "ws", Kind: WorkspaceRemote},
		},
		Tools:   catalog,
		Backend: backend,
	}

	_, err = runtime.Start(context.Background(), StartRequest{RunID: "run", ConversationID: "conv", Input: "Do it."})
	if err == nil {
		t.Fatal("expected backend binding mismatch to fail")
	}
}

func TestMCPToolRequiresEndpoint(t *testing.T) {
	_, err := NewToolCatalog([]ToolDescriptor{{Name: "mcp", Protocol: ToolProtocolMCP}})
	if err == nil {
		t.Fatal("expected MCP tool without endpoint to fail")
	}
}
