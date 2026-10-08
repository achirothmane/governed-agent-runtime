package mcptransport

import (
	"context"
	"encoding/json"
	"errors"
	"sync/atomic"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	runtimesdk "github.com/achirothmane/governed-agent-runtime/sdk"
)

type fakeSession struct {
	protocol string
	server   ServerIdentity
	tools    []*mcp.Tool
	listErr  error
	callRes  *mcp.CallToolResult
	callErr  error
	called   *mcp.CallToolParams
	closed   bool
}

func (f *fakeSession) ProtocolVersion() string        { return f.protocol }
func (f *fakeSession) ServerIdentity() ServerIdentity { return f.server }
func (f *fakeSession) Tools(context.Context) ([]*mcp.Tool, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	return f.tools, nil
}
func (f *fakeSession) CallTool(_ context.Context, params *mcp.CallToolParams) (*mcp.CallToolResult, error) {
	f.called = params
	return f.callRes, f.callErr
}
func (f *fakeSession) Close() error { f.closed = true; return nil }

func testTools() []*mcp.Tool {
	readOnly := true
	return []*mcp.Tool{
		{
			Name:        "write_issue",
			Description: "write an issue",
			InputSchema: map[string]any{
				"type":       "object",
				"properties": map[string]any{"title": map[string]any{"type": "string"}},
			},
		},
		{
			Name:        "read_repo",
			Description: "read a repository",
			InputSchema: map[string]any{
				"properties": map[string]any{"repo": map[string]any{"type": "string"}},
				"type":       "object",
			},
			OutputSchema: map[string]any{"type": "object"},
			Annotations:  &mcp.ToolAnnotations{ReadOnlyHint: readOnly},
		},
	}
}

func newFakeTransport(t *testing.T) (*Transport, CapabilitySnapshot, *fakeSession, *atomic.Uint64) {
	t.Helper()
	f := &fakeSession{
		protocol: "2026-07-28",
		server:   ServerIdentity{Name: "test-server", Version: "1.0.0"},
		tools:    testTools(),
		callRes: &mcp.CallToolResult{
			Content:           []mcp.Content{&mcp.TextContent{Text: "ok"}},
			StructuredContent: map[string]any{"ok": true},
		},
	}
	changes := new(atomic.Uint64)
	tr := newTransport("https://example.invalid/mcp", f, changes)
	snapshot, err := tr.Discover(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return tr, snapshot, f, changes
}

func TestSnapshotDigestIsDeterministicAcrossToolOrder(t *testing.T) {
	tools := testTools()
	a, err := buildSnapshot("https://example.invalid/mcp", "2026-07-28", ServerIdentity{Name: "s", Version: "1"}, tools)
	if err != nil {
		t.Fatal(err)
	}
	b, err := buildSnapshot("https://example.invalid/mcp", "2026-07-28", ServerIdentity{Name: "s", Version: "1"}, []*mcp.Tool{tools[1], tools[0]})
	if err != nil {
		t.Fatal(err)
	}
	if a.Digest != b.Digest {
		t.Fatalf("digest changed with tool ordering: %s != %s", a.Digest, b.Digest)
	}
	if a.Tools[0].Name != "read_repo" || a.Tools[1].Name != "write_issue" {
		t.Fatalf("tools are not canonically ordered: %#v", a.Tools)
	}
}

func TestSnapshotDigestChangesWithSchema(t *testing.T) {
	tools := testTools()
	a, err := buildSnapshot("https://example.invalid/mcp", "2026-07-28", ServerIdentity{Name: "s", Version: "1"}, tools)
	if err != nil {
		t.Fatal(err)
	}
	changed := testTools()
	changed[1].InputSchema = map[string]any{
		"type": "object",
		"properties": map[string]any{
			"repo":   map[string]any{"type": "string"},
			"branch": map[string]any{"type": "string"},
		},
	}
	b, err := buildSnapshot("https://example.invalid/mcp", "2026-07-28", ServerIdentity{Name: "s", Version: "1"}, changed)
	if err != nil {
		t.Fatal(err)
	}
	if a.Digest == b.Digest {
		t.Fatal("schema change did not change snapshot digest")
	}
}

func TestDescriptorBindsSnapshotWithoutTrustingReadOnlyHint(t *testing.T) {
	_, snapshot, _, _ := newFakeTransport(t)
	desc, err := snapshot.Descriptor(runtimesdk.ToolName("read_repo"))
	if err != nil {
		t.Fatal(err)
	}
	if desc.Protocol != runtimesdk.ToolProtocolMCP || desc.Endpoint != snapshot.Endpoint {
		t.Fatalf("unexpected descriptor: %#v", desc)
	}
	if desc.SnapshotDigest != snapshot.Digest {
		t.Fatalf("snapshot digest = %q, want %q", desc.SnapshotDigest, snapshot.Digest)
	}
	if desc.ReadOnly {
		t.Fatal("remote MCP readOnly hint was incorrectly promoted into runtime authority")
	}
	if !snapshot.Tools[0].DeclaredReadOnly {
		t.Fatal("server readOnly hint was not retained as descriptive metadata")
	}
}

func TestInvokeRejectsToolOutsideSnapshot(t *testing.T) {
	tr, snapshot, f, _ := newFakeTransport(t)
	_, err := tr.Invoke(context.Background(), Invocation{
		SnapshotDigest: snapshot.Digest,
		Tool:           "new_tool",
	})
	if !errors.Is(err, ErrToolNotInSnapshot) {
		t.Fatalf("error = %v, want ErrToolNotInSnapshot", err)
	}
	if f.called != nil {
		t.Fatal("remote tool was called despite missing snapshot authority")
	}
}

func TestInvokeRejectsStaleSnapshotAfterListChange(t *testing.T) {
	tr, snapshot, f, changes := newFakeTransport(t)
	changes.Add(1)
	_, err := tr.Invoke(context.Background(), Invocation{
		SnapshotDigest: snapshot.Digest,
		Tool:           "read_repo",
	})
	if !errors.Is(err, ErrSnapshotStale) {
		t.Fatalf("error = %v, want ErrSnapshotStale", err)
	}
	if f.called != nil {
		t.Fatal("remote tool was called with stale snapshot")
	}
}

func TestInvokeNormalizesResultAndKeepsToolErrorsAsData(t *testing.T) {
	tr, snapshot, f, _ := newFakeTransport(t)
	f.callRes.IsError = true

	result, err := tr.Invoke(context.Background(), Invocation{
		SnapshotDigest: snapshot.Digest,
		Tool:           "read_repo",
		Arguments:      map[string]any{"repo": "achirothmane/data-engine"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if f.called == nil || f.called.Name != "read_repo" {
		t.Fatalf("unexpected call: %#v", f.called)
	}
	if !result.IsError {
		t.Fatal("tool-level error was not preserved as result data")
	}
	if result.SnapshotDigest != snapshot.Digest {
		t.Fatalf("result digest = %q, want %q", result.SnapshotDigest, snapshot.Digest)
	}
	if len(result.Content) != 1 {
		t.Fatalf("content count = %d, want 1", len(result.Content))
	}
	var content map[string]any
	if err := json.Unmarshal(result.Content[0], &content); err != nil {
		t.Fatal(err)
	}
	if content["text"] != "ok" {
		t.Fatalf("content = %#v", content)
	}
	var structured map[string]any
	if err := json.Unmarshal(result.StructuredContent, &structured); err != nil {
		t.Fatal(err)
	}
	if structured["ok"] != true {
		t.Fatalf("structured content = %#v", structured)
	}
}

func TestDiscoverRejectsMissingProtocolVersion(t *testing.T) {
	f := &fakeSession{tools: testTools()}
	tr := newTransport("https://example.invalid/mcp", f, new(atomic.Uint64))
	_, err := tr.Discover(context.Background())
	if err == nil {
		t.Fatal("expected discovery failure")
	}
}
