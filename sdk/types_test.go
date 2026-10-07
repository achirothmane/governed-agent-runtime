package sdk

import "testing"

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
