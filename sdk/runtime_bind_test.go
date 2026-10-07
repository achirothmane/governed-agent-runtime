package sdk

import "testing"

func TestRuntimeBindPinsExactCapabilitySnapshot(t *testing.T) {
	catalog, err := NewToolCatalog([]ToolDescriptor{{
		Name:           "data.profile",
		Protocol:       ToolProtocolMCP,
		Endpoint:       "http://data-engine.test/mcp",
		ReadOnly:       true,
		SnapshotDigest: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
	}})
	if err != nil {
		t.Fatal(err)
	}
	runtime := Runtime{
		Agent: AgentSpec{
			ID:            "data-profiler",
			Mission:       "Profile data without changing it.",
			RequiredTools: []ToolName{"data.profile"},
			Workspace:     WorkspaceSpec{ID: "data", Kind: WorkspaceRemote},
		},
		Tools: catalog,
	}
	req := StartRequest{RunID: "run-1", ConversationID: "conv-1", Input: "profile rows"}
	bound, fingerprint, err := runtime.Bind(req)
	if err != nil {
		t.Fatal(err)
	}
	if fingerprint == "" || len(bound.Tools) != 1 {
		t.Fatalf("bound=%#v fingerprint=%q", bound, fingerprint)
	}
	if bound.Tools[0].SnapshotDigest != "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" {
		t.Fatalf("snapshot=%q", bound.Tools[0].SnapshotDigest)
	}

	changedCatalog, err := NewToolCatalog([]ToolDescriptor{{
		Name:           "data.profile",
		Protocol:       ToolProtocolMCP,
		Endpoint:       "http://data-engine.test/mcp",
		ReadOnly:       true,
		SnapshotDigest: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
	}})
	if err != nil {
		t.Fatal(err)
	}
	runtime.Tools = changedCatalog
	_, changedFingerprint, err := runtime.Bind(req)
	if err != nil {
		t.Fatal(err)
	}
	if fingerprint == changedFingerprint {
		t.Fatal("capability snapshot change did not alter run fingerprint")
	}
}
