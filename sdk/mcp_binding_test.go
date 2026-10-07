package sdk

import "testing"

func TestRunFingerprintBindsMCPSnapshotDigest(t *testing.T) {
	base := RunRequest{
		ID: "run-1",
		Conversation: ConversationRef{
			ID:          "conv-1",
			AgentID:     "agent-1",
			WorkspaceID: "ws-1",
		},
		Workspace: WorkspaceSpec{ID: "ws-1", Kind: WorkspaceRemote},
		Input:     "inspect repository",
		Tools: []ToolDescriptor{
			{
				Name:           "repo.read",
				Protocol:       ToolProtocolMCP,
				Endpoint:       "https://example.invalid/mcp",
				SnapshotDigest: "snapshot-a",
			},
		},
	}

	a, err := base.Fingerprint()
	if err != nil {
		t.Fatal(err)
	}
	changed := base
	changed.Tools = append([]ToolDescriptor(nil), base.Tools...)
	changed.Tools[0].SnapshotDigest = "snapshot-b"
	b, err := changed.Fingerprint()
	if err != nil {
		t.Fatal(err)
	}
	if a == b {
		t.Fatal("changing the MCP capability snapshot did not change run identity")
	}
}
