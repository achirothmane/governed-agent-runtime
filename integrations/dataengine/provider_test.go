package dataengine

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	runtimesdk "github.com/achirothmane/governed-agent-runtime/sdk"
)

type testBackend struct{}

func (testBackend) Name() string { return "test-durable" }

func (testBackend) Start(_ context.Context, run runtimesdk.RunRequest) (runtimesdk.RunHandle, error) {
	fingerprint, err := run.Fingerprint()
	if err != nil {
		return runtimesdk.RunHandle{}, err
	}
	return runtimesdk.RunHandle{
		ID:          run.ID,
		Backend:     "test-durable",
		ExternalID:  "test/" + string(run.ID),
		Fingerprint: fingerprint,
		State:       runtimesdk.RunQueued,
	}, nil
}

func (testBackend) Inspect(context.Context, runtimesdk.RunID) (runtimesdk.RunHandle, error) {
	return runtimesdk.RunHandle{}, errors.New("unused")
}

func (testBackend) Signal(context.Context, runtimesdk.RunID, string, []byte) error { return nil }
func (testBackend) Cancel(context.Context, runtimesdk.RunID, string) error         { return nil }

type profileInput struct {
	Rows []map[string]any `json:"rows"`
}

type profileOutput struct {
	Stage    string `json:"stage"`
	Decision string `json:"decision"`
	Rows     int    `json:"rows"`
}

type reconcileInput struct {
	Source struct {
		Columns []string            `json:"columns"`
		Rows    []map[string]string `json:"rows"`
	} `json:"source"`
	Target struct {
		Columns []string            `json:"columns"`
		Rows    []map[string]string `json:"rows"`
	} `json:"target"`
	Contract struct {
		IdentityField string   `json:"identity_field"`
		Fields        []string `json:"fields"`
		Comparison    string   `json:"comparison"`
	} `json:"contract"`
}

type reconcileOutput struct {
	Report struct {
		State  string `json:"state"`
		Reason string `json:"reason"`
	} `json:"report"`
}

func newDataEngineMCPServer(t *testing.T) (*httptest.Server, *mcp.Server) {
	t.Helper()
	server := mcp.NewServer(&mcp.Implementation{Name: ExpectedServerName, Version: "a5"}, nil)
	mcp.AddTool(server, &mcp.Tool{
		Name: string(ProfileTool),
		Annotations: &mcp.ToolAnnotations{
			ReadOnlyHint:   true,
			IdempotentHint: true,
		},
	}, func(_ context.Context, _ *mcp.CallToolRequest, input profileInput) (*mcp.CallToolResult, profileOutput, error) {
		return nil, profileOutput{
			Stage:    "PRE_SEMANTIC_PROFILE",
			Decision: "KNOWN",
			Rows:     len(input.Rows),
		}, nil
	})
	mcp.AddTool(server, &mcp.Tool{
		Name: string(ReconcileTool),
		Annotations: &mcp.ToolAnnotations{
			ReadOnlyHint:   true,
			IdempotentHint: true,
		},
	}, func(_ context.Context, _ *mcp.CallToolRequest, input reconcileInput) (*mcp.CallToolResult, reconcileOutput, error) {
		var out reconcileOutput
		out.Report.State = "DIVERGED"
		out.Report.Reason = "SOURCE_TARGET_DIVERGED"
		if len(input.Source.Rows) == len(input.Target.Rows) && len(input.Source.Rows) == 0 {
			out.Report.State = "MATCH"
			out.Report.Reason = "EXACT_RAW_PARITY_PROVEN"
		}
		return nil, out, nil
	})
	httpServer := httptest.NewServer(mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server {
		return server
	}, nil))
	t.Cleanup(httpServer.Close)
	return httpServer, server
}

func TestProviderDiscoversPinsAndInvokesDataProfile(t *testing.T) {
	httpServer, _ := newDataEngineMCPServer(t)
	provider := Provider{
		Endpoint: httpServer.URL,
		Backend:  testBackend{},
	}
	ctx := context.Background()
	runtime, err := provider.Runtime(ctx, "data-profiler")
	if err != nil {
		t.Fatal(err)
	}
	bound, fingerprint, err := runtime.Bind(runtimesdk.StartRequest{
		RunID:          "run-1",
		ConversationID: "conv-1",
		Input:          "profile the supplied rows",
	})
	if err != nil {
		t.Fatal(err)
	}
	if fingerprint == "" || len(bound.Tools) != 1 {
		t.Fatalf("bound=%#v fingerprint=%q", bound, fingerprint)
	}
	tool := bound.Tools[0]
	if tool.Name != ProfileTool || !tool.ReadOnly || tool.SnapshotDigest == "" {
		t.Fatalf("tool=%#v", tool)
	}

	result, err := provider.Invoke(ctx, tool, map[string]any{
		"rows": []any{
			map[string]any{"id": "a", "price": 20},
			map[string]any{"id": "b", "price": 21},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError || result.SnapshotDigest != tool.SnapshotDigest {
		t.Fatalf("result=%#v", result)
	}
	var output profileOutput
	if err := json.Unmarshal(result.StructuredContent, &output); err != nil {
		t.Fatal(err)
	}
	if output.Stage != "PRE_SEMANTIC_PROFILE" || output.Rows != 2 {
		t.Fatalf("output=%#v", output)
	}
}

func TestProviderRejectsCapabilityExpansionAfterRunBinding(t *testing.T) {
	httpServer, server := newDataEngineMCPServer(t)
	provider := Provider{Endpoint: httpServer.URL, Backend: testBackend{}}
	ctx := context.Background()
	runtime, err := provider.Runtime(ctx, "data-profiler")
	if err != nil {
		t.Fatal(err)
	}
	bound, _, err := runtime.Bind(runtimesdk.StartRequest{
		RunID:          "run-1",
		ConversationID: "conv-1",
		Input:          "profile",
	})
	if err != nil {
		t.Fatal(err)
	}
	tool := bound.Tools[0]

	type extraInput struct {
		Value string `json:"value"`
	}
	mcp.AddTool(server, &mcp.Tool{Name: "data.extra"}, func(_ context.Context, _ *mcp.CallToolRequest, input extraInput) (*mcp.CallToolResult, extraInput, error) {
		return nil, input, nil
	})

	_, err = provider.Invoke(ctx, tool, map[string]any{"rows": []any{map[string]any{"x": 1}}})
	if !errors.Is(err, ErrCapabilityChanged) {
		t.Fatalf("error=%v want ErrCapabilityChanged", err)
	}
}

func TestProviderAdmitsReconcileForExplicitAgentSpec(t *testing.T) {
	httpServer, _ := newDataEngineMCPServer(t)
	provider := Provider{
		Endpoint: httpServer.URL,
		Backend:  testBackend{},
		AgentSpec: runtimesdk.AgentSpec{
			ID:            "portfolio-dot-data",
			Mission:       "Inspect pinned data evidence without mutating it.",
			RequiredTools: []runtimesdk.ToolName{ProfileTool, ReconcileTool},
			Workspace:     runtimesdk.WorkspaceSpec{ID: "portfolio-dot", Kind: runtimesdk.WorkspaceRemote},
		},
	}
	ctx := context.Background()
	runtime, err := provider.Runtime(ctx, "portfolio-dot-data")
	if err != nil {
		t.Fatal(err)
	}
	bound, _, err := runtime.Bind(runtimesdk.StartRequest{
		RunID:          "run-dots-reconcile",
		ConversationID: "conv-dots-reconcile",
		Input:          "compare the supplied source and target snapshots",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(bound.Tools) != 2 {
		t.Fatalf("bound tools=%#v", bound.Tools)
	}
	var reconcileTool runtimesdk.ToolDescriptor
	for _, tool := range bound.Tools {
		if tool.Name == ReconcileTool {
			reconcileTool = tool
			break
		}
	}
	if reconcileTool.Name != ReconcileTool || !reconcileTool.ReadOnly || reconcileTool.SnapshotDigest == "" {
		t.Fatalf("reconcile tool=%#v", reconcileTool)
	}

	result, err := provider.Invoke(ctx, reconcileTool, map[string]any{
		"source": map[string]any{
			"columns": []any{"id", "amount"},
			"rows":    []any{map[string]any{"id": "1", "amount": "10"}},
		},
		"target": map[string]any{
			"columns": []any{"id", "amount"},
			"rows":    []any{map[string]any{"id": "1", "amount": "11"}},
		},
		"contract": map[string]any{
			"identity_field": "id",
			"fields":         []any{"amount"},
			"comparison":     "EXACT_RAW",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError || result.SnapshotDigest != reconcileTool.SnapshotDigest {
		t.Fatalf("result=%#v", result)
	}
	var output reconcileOutput
	if err := json.Unmarshal(result.StructuredContent, &output); err != nil {
		t.Fatal(err)
	}
	if output.Report.State != "DIVERGED" || output.Report.Reason != "SOURCE_TARGET_DIVERGED" {
		t.Fatalf("output=%#v", output)
	}
}

func TestProviderRejectsUndeclaredDataEngineToolInAgentSpec(t *testing.T) {
	httpServer, _ := newDataEngineMCPServer(t)
	provider := Provider{
		Endpoint: httpServer.URL,
		Backend:  testBackend{},
		AgentSpec: runtimesdk.AgentSpec{
			ID:            "bad-dot",
			Mission:       "bad",
			RequiredTools: []runtimesdk.ToolName{"data.unadmitted"},
			Workspace:     runtimesdk.WorkspaceSpec{ID: "portfolio-dot", Kind: runtimesdk.WorkspaceRemote},
		},
	}
	_, err := provider.Runtime(context.Background(), "bad-dot")
	if !errors.Is(err, ErrToolRejected) {
		t.Fatalf("error=%v want ErrToolRejected", err)
	}
}
