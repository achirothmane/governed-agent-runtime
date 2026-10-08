package sdk

import (
	"encoding/json"
	"testing"
)

func TestRunFingerprintIsStableAcrossRequiredToolDeclarationOrder(t *testing.T) {
	catalog, err := NewToolCatalog([]ToolDescriptor{
		{Name: "b", Protocol: ToolProtocolNative},
		{Name: "a", Protocol: ToolProtocolNative},
	})
	if err != nil {
		t.Fatal(err)
	}
	tools1, err := catalog.Resolve([]ToolName{"b", "a"})
	if err != nil {
		t.Fatal(err)
	}
	tools2, err := catalog.Resolve([]ToolName{"a", "b"})
	if err != nil {
		t.Fatal(err)
	}
	base := RunRequest{
		ID:           "run",
		Conversation: ConversationRef{ID: "conv", AgentID: "agent", WorkspaceID: "ws"},
		Workspace:    WorkspaceSpec{ID: "ws", Kind: WorkspaceRemote},
		Input:        "work",
	}
	r1 := base
	r1.Tools = tools1
	r2 := base
	r2.Tools = tools2
	f1, err := r1.Fingerprint()
	if err != nil {
		t.Fatal(err)
	}
	f2, err := r2.Fingerprint()
	if err != nil {
		t.Fatal(err)
	}
	if f1 != f2 {
		t.Fatalf("equivalent capability sets must bind identically: %s != %s", f1, f2)
	}
}


func TestRunFingerprintBindsToolSchema(t *testing.T) {
	base := RunRequest{
		ID:           "run-schema",
		Conversation: ConversationRef{ID: "conv", AgentID: "agent", WorkspaceID: "ws"},
		Workspace:    WorkspaceSpec{ID: "ws", Kind: WorkspaceRemote},
		Input:        "work",
		Tools: []ToolDescriptor{{
			Name:           "data.profile",
			Protocol:       ToolProtocolMCP,
			Endpoint:       "https://data.example/mcp",
			ReadOnly:       true,
			SnapshotDigest: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
			InputSchema:    json.RawMessage(`{"type":"object","properties":{"rows":{"type":"array"}}}`),
		}},
	}
	first, err := base.Fingerprint()
	if err != nil {
		t.Fatal(err)
	}
	changed := base
	changed.Tools = append([]ToolDescriptor(nil), base.Tools...)
	changed.Tools[0].InputSchema = json.RawMessage(`{"type":"object","properties":{"rows":{"type":"array"},"identity_field":{"type":"string"}}}`)
	second, err := changed.Fingerprint()
	if err != nil {
		t.Fatal(err)
	}
	if first == second {
		t.Fatal("changing a model-visible tool schema must change the run fingerprint")
	}
}

func TestToolDescriptorRejectsInvalidModelVisibleSchema(t *testing.T) {
	tool := ToolDescriptor{
		Name:        "data.profile",
		Protocol:    ToolProtocolMCP,
		Endpoint:    "https://data.example/mcp",
		InputSchema: json.RawMessage(`{"type":`),
	}
	if err := tool.Validate(); err == nil {
		t.Fatal("invalid tool input schema must be rejected")
	}
}
