package dataengine

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/achirothmane/governed-agent-runtime/mcptransport"
	runtimesdk "github.com/achirothmane/governed-agent-runtime/sdk"
)

const (
	ProfileTool        runtimesdk.ToolName = "data.profile"
	ExpectedServerName                     = "data-engine"
)

var (
	ErrUnexpectedServer  = errors.New("unexpected data engine MCP server identity")
	ErrCapabilityChanged = errors.New("data engine capability snapshot changed")
	ErrToolRejected      = errors.New("data engine tool is not admitted")
)

type Provider struct {
	Endpoint  string
	Connector mcptransport.Connector
	Backend   runtimesdk.ExecutionBackend
	AgentSpec runtimesdk.AgentSpec
}

func (p Provider) Agent(_ context.Context, id runtimesdk.AgentID) (runtimesdk.AgentSpec, error) {
	agent := p.agent()
	if id != agent.ID {
		return runtimesdk.AgentSpec{}, fmt.Errorf("data engine agent %q is unavailable", id)
	}
	if err := agent.Validate(); err != nil {
		return runtimesdk.AgentSpec{}, fmt.Errorf("data engine agent: %w", err)
	}
	return agent, nil
}

func (p Provider) Runtime(ctx context.Context, id runtimesdk.AgentID) (runtimesdk.Runtime, error) {
	agent, err := p.Agent(ctx, id)
	if err != nil {
		return runtimesdk.Runtime{}, err
	}
	if p.Backend == nil {
		return runtimesdk.Runtime{}, errors.New("data engine execution backend is required")
	}
	snapshot, err := p.discover(ctx)
	if err != nil {
		return runtimesdk.Runtime{}, err
	}
	descriptor, err := snapshot.Descriptor(ProfileTool)
	if err != nil {
		return runtimesdk.Runtime{}, err
	}
	// This is server-owned admission based on our known Data Engine contract.
	// The remote MCP readOnlyHint alone never grants this authority.
	descriptor.ReadOnly = true
	catalog, err := runtimesdk.NewToolCatalog([]runtimesdk.ToolDescriptor{descriptor})
	if err != nil {
		return runtimesdk.Runtime{}, err
	}
	return runtimesdk.Runtime{Agent: agent, Tools: catalog, Backend: p.Backend}, nil
}

func (p Provider) Invoke(ctx context.Context, tool runtimesdk.ToolDescriptor, arguments map[string]any) (mcptransport.Result, error) {
	if err := p.validateAdmittedTool(tool); err != nil {
		return mcptransport.Result{}, err
	}
	transport, err := p.Connector.ConnectHTTP(ctx, p.Endpoint)
	if err != nil {
		return mcptransport.Result{}, err
	}
	defer transport.Close()

	snapshot, err := transport.Discover(ctx)
	if err != nil {
		return mcptransport.Result{}, err
	}
	if snapshot.Server.Name != ExpectedServerName {
		return mcptransport.Result{}, fmt.Errorf("%w: got %q", ErrUnexpectedServer, snapshot.Server.Name)
	}
	if snapshot.Digest != tool.SnapshotDigest {
		return mcptransport.Result{}, fmt.Errorf("%w: bound=%s current=%s", ErrCapabilityChanged, tool.SnapshotDigest, snapshot.Digest)
	}
	return transport.Invoke(ctx, mcptransport.Invocation{
		SnapshotDigest: snapshot.Digest,
		Tool:           ProfileTool,
		Arguments:      arguments,
	})
}

func (p Provider) discover(ctx context.Context) (mcptransport.CapabilitySnapshot, error) {
	if strings.TrimSpace(p.Endpoint) == "" {
		return mcptransport.CapabilitySnapshot{}, errors.New("data engine MCP endpoint is required")
	}
	transport, err := p.Connector.ConnectHTTP(ctx, p.Endpoint)
	if err != nil {
		return mcptransport.CapabilitySnapshot{}, err
	}
	defer transport.Close()
	snapshot, err := transport.Discover(ctx)
	if err != nil {
		return mcptransport.CapabilitySnapshot{}, err
	}
	if snapshot.Server.Name != ExpectedServerName {
		return mcptransport.CapabilitySnapshot{}, fmt.Errorf("%w: got %q", ErrUnexpectedServer, snapshot.Server.Name)
	}
	if _, err := snapshot.Descriptor(ProfileTool); err != nil {
		return mcptransport.CapabilitySnapshot{}, fmt.Errorf("required data engine profile tool: %w", err)
	}
	return snapshot, nil
}

func (p Provider) validateAdmittedTool(tool runtimesdk.ToolDescriptor) error {
	switch {
	case tool.Name != ProfileTool:
		return fmt.Errorf("%w: %s", ErrToolRejected, tool.Name)
	case tool.Protocol != runtimesdk.ToolProtocolMCP:
		return fmt.Errorf("%w: profile tool protocol is %q", ErrToolRejected, tool.Protocol)
	case !tool.ReadOnly:
		return fmt.Errorf("%w: profile tool is not server-admitted read-only", ErrToolRejected)
	case strings.TrimSpace(tool.SnapshotDigest) == "":
		return fmt.Errorf("%w: profile tool has no snapshot digest", ErrToolRejected)
	case strings.TrimSpace(p.Endpoint) == "" || tool.Endpoint != p.Endpoint:
		return fmt.Errorf("%w: endpoint mismatch", ErrToolRejected)
	default:
		return nil
	}
}

func (p Provider) agent() runtimesdk.AgentSpec {
	agent := p.AgentSpec
	if agent.ID == "" {
		agent.ID = "data-profiler"
	}
	if strings.TrimSpace(agent.Mission) == "" {
		agent.Mission = "Profile data through the Data Engine without transforming source values."
	}
	if len(agent.RequiredTools) == 0 {
		agent.RequiredTools = []runtimesdk.ToolName{ProfileTool}
	}
	if agent.Workspace.ID == "" {
		agent.Workspace = runtimesdk.WorkspaceSpec{ID: "data-engine", Kind: runtimesdk.WorkspaceRemote}
	}
	return agent
}
