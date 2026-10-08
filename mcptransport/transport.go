package mcptransport

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	runtimesdk "github.com/achirothmane/governed-agent-runtime/sdk"
)

var (
	ErrSnapshotUnknown          = errors.New("mcp capability snapshot is unknown")
	ErrSnapshotMismatch         = errors.New("mcp capability snapshot digest mismatch")
	ErrSnapshotStale            = errors.New("mcp capability snapshot is stale")
	ErrSnapshotChangedDiscovery = errors.New("mcp tool list changed during discovery")
	ErrToolNotInSnapshot        = errors.New("mcp tool is not present in admitted snapshot")
)

// ServerIdentity records the remote MCP implementation observed during discovery.
type ServerIdentity struct {
	Name    string `json:"name,omitempty"`
	Version string `json:"version,omitempty"`
}

// ToolCapability is the immutable view of one remotely discovered MCP tool.
// DeclaredReadOnly is retained as server-provided metadata only. It is never
// promoted into runtime authority by this package.
type ToolCapability struct {
	Name             runtimesdk.ToolName `json:"name"`
	Title            string              `json:"title,omitempty"`
	Description      string              `json:"description,omitempty"`
	InputSchema      json.RawMessage     `json:"input_schema"`
	OutputSchema     json.RawMessage     `json:"output_schema,omitempty"`
	DeclaredReadOnly bool                `json:"declared_read_only"`
}

// CapabilitySnapshot binds an endpoint, negotiated protocol, server identity,
// and canonical tool schemas into one digest. Generation is local observation
// state and is deliberately excluded from Digest.
type CapabilitySnapshot struct {
	Endpoint        string           `json:"endpoint"`
	ProtocolVersion string           `json:"protocol_version"`
	Server          ServerIdentity   `json:"server"`
	Tools           []ToolCapability `json:"tools"`
	Digest          string           `json:"digest"`
	Generation      uint64           `json:"-"`
}

// Descriptor returns a runtime descriptor bound to this exact snapshot.
// ReadOnly is conservative by design: MCP annotations are hints, not authority.
func (s CapabilitySnapshot) Descriptor(name runtimesdk.ToolName) (runtimesdk.ToolDescriptor, error) {
	for _, tool := range s.Tools {
		if tool.Name == name {
			return runtimesdk.ToolDescriptor{
				Name:           tool.Name,
				Protocol:       runtimesdk.ToolProtocolMCP,
				Endpoint:       s.Endpoint,
				Title:          tool.Title,
				Description:    tool.Description,
				InputSchema:    append(json.RawMessage(nil), tool.InputSchema...),
				OutputSchema:   append(json.RawMessage(nil), tool.OutputSchema...),
				ReadOnly:       false,
				SnapshotDigest: s.Digest,
			}, nil
		}
	}
	return runtimesdk.ToolDescriptor{}, fmt.Errorf("%w: %s", ErrToolNotInSnapshot, name)
}

// Descriptors projects the whole snapshot into deterministic runtime tool descriptors.
func (s CapabilitySnapshot) Descriptors() []runtimesdk.ToolDescriptor {
	out := make([]runtimesdk.ToolDescriptor, 0, len(s.Tools))
	for _, tool := range s.Tools {
		out = append(out, runtimesdk.ToolDescriptor{
			Name:           tool.Name,
			Protocol:       runtimesdk.ToolProtocolMCP,
			Endpoint:       s.Endpoint,
			Title:          tool.Title,
			Description:    tool.Description,
			InputSchema:    append(json.RawMessage(nil), tool.InputSchema...),
			OutputSchema:   append(json.RawMessage(nil), tool.OutputSchema...),
			ReadOnly:       false,
			SnapshotDigest: s.Digest,
		})
	}
	return out
}

// Invocation is an MCP call admitted against a previously captured snapshot.
type Invocation struct {
	SnapshotDigest string
	Tool           runtimesdk.ToolName
	Arguments      map[string]any
}

// Result is an SDK-neutral normalization of an MCP tool result. Tool-level
// errors remain data (IsError=true) so an agent can inspect and recover from
// them. Protocol/transport failures are returned as Go errors.
type Result struct {
	Tool              runtimesdk.ToolName `json:"tool"`
	SnapshotDigest    string              `json:"snapshot_digest"`
	IsError           bool                `json:"is_error"`
	NeedsInput        bool                `json:"needs_input"`
	Content           []json.RawMessage   `json:"content"`
	StructuredContent json.RawMessage     `json:"structured_content,omitempty"`
	InputRequests     json.RawMessage     `json:"input_requests,omitempty"`
	RequestState      string              `json:"request_state,omitempty"`
}

type session interface {
	ProtocolVersion() string
	ServerIdentity() ServerIdentity
	Tools(context.Context) ([]*mcp.Tool, error)
	CallTool(context.Context, *mcp.CallToolParams) (*mcp.CallToolResult, error)
	Close() error
}

type sdkSession struct {
	*mcp.ClientSession
}

func (s sdkSession) ProtocolVersion() string {
	if s.ClientSession == nil || s.InitializeResult() == nil {
		return ""
	}
	return s.InitializeResult().ProtocolVersion
}

func (s sdkSession) ServerIdentity() ServerIdentity {
	if s.ClientSession == nil || s.InitializeResult() == nil || s.InitializeResult().ServerInfo == nil {
		return ServerIdentity{}
	}
	return ServerIdentity{
		Name:    s.InitializeResult().ServerInfo.Name,
		Version: s.InitializeResult().ServerInfo.Version,
	}
}

func (s sdkSession) Tools(ctx context.Context) ([]*mcp.Tool, error) {
	var tools []*mcp.Tool
	for tool, err := range s.ClientSession.Tools(ctx, nil) {
		if err != nil {
			return nil, err
		}
		tools = append(tools, tool)
	}
	return tools, nil
}

// Connector creates MCP transports using the official MCP Go SDK. The default
// HTTP path uses Streamable HTTP and negotiates the newest mutually supported
// protocol version.
type Connector struct {
	Implementation mcp.Implementation
	HTTPClient     *http.Client
	MaxRetries     int
	MaxEventSize   int
}

func (c Connector) implementation() mcp.Implementation {
	impl := c.Implementation
	if strings.TrimSpace(impl.Name) == "" {
		impl.Name = "ai-native-runtime"
	}
	if strings.TrimSpace(impl.Version) == "" {
		impl.Version = "a3"
	}
	return impl
}

func (c Connector) ConnectHTTP(ctx context.Context, endpoint string) (*Transport, error) {
	if strings.TrimSpace(endpoint) == "" {
		return nil, errors.New("mcp endpoint is required")
	}
	mt := &mcp.StreamableClientTransport{
		Endpoint:     endpoint,
		HTTPClient:   c.HTTPClient,
		MaxRetries:   c.MaxRetries,
		MaxEventSize: c.MaxEventSize,
	}
	return c.Connect(ctx, endpoint, mt)
}

// Connect accepts any official MCP transport (Streamable HTTP, command, in-memory,
// or a future transport) while keeping the runtime boundary transport-neutral.
func (c Connector) Connect(ctx context.Context, endpoint string, mt mcp.Transport) (*Transport, error) {
	if strings.TrimSpace(endpoint) == "" {
		return nil, errors.New("mcp endpoint is required")
	}
	if mt == nil {
		return nil, errors.New("mcp transport is required")
	}

	changes := new(atomic.Uint64)
	impl := c.implementation()
	client := mcp.NewClient(&impl, &mcp.ClientOptions{
		Capabilities: &mcp.ClientCapabilities{},
		ToolListChangedHandler: func(context.Context, *mcp.ToolListChangedRequest) {
			changes.Add(1)
		},
	})
	s, err := client.Connect(ctx, mt, nil)
	if err != nil {
		return nil, fmt.Errorf("connect mcp server: %w", err)
	}
	return newTransport(endpoint, sdkSession{ClientSession: s}, changes), nil
}

// Transport owns one MCP session and any snapshots captured from that session.
type Transport struct {
	endpoint string
	session  session
	changes  *atomic.Uint64

	mu        sync.RWMutex
	snapshots map[string]CapabilitySnapshot
}

func newTransport(endpoint string, s session, changes *atomic.Uint64) *Transport {
	if changes == nil {
		changes = new(atomic.Uint64)
	}
	return &Transport{
		endpoint:  endpoint,
		session:   s,
		changes:   changes,
		snapshots: make(map[string]CapabilitySnapshot),
	}
}

func (t *Transport) Close() error {
	if t == nil || t.session == nil {
		return nil
	}
	return t.session.Close()
}

// Discover captures and stores an immutable capability snapshot. A list-change
// notification racing with discovery invalidates the result rather than letting
// the caller bind an ambiguous surface.
func (t *Transport) Discover(ctx context.Context) (CapabilitySnapshot, error) {
	if t == nil || t.session == nil {
		return CapabilitySnapshot{}, errors.New("mcp session is required")
	}
	startGeneration := t.changes.Load()
	tools, err := t.session.Tools(ctx)
	if err != nil {
		return CapabilitySnapshot{}, fmt.Errorf("list mcp tools: %w", err)
	}
	if t.changes.Load() != startGeneration {
		return CapabilitySnapshot{}, ErrSnapshotChangedDiscovery
	}

	snapshot, err := buildSnapshot(t.endpoint, t.session.ProtocolVersion(), t.session.ServerIdentity(), tools)
	if err != nil {
		return CapabilitySnapshot{}, err
	}
	snapshot.Generation = startGeneration

	t.mu.Lock()
	t.snapshots[snapshot.Digest] = snapshot
	t.mu.Unlock()
	return snapshot, nil
}

// Invoke calls only tools that existed in the referenced admitted snapshot.
// A list-changed notification invalidates the old surface; authority never
// expands automatically to newly exposed tools.
func (t *Transport) Invoke(ctx context.Context, in Invocation) (Result, error) {
	if t == nil || t.session == nil {
		return Result{}, errors.New("mcp session is required")
	}
	if strings.TrimSpace(in.SnapshotDigest) == "" {
		return Result{}, ErrSnapshotUnknown
	}
	if strings.TrimSpace(string(in.Tool)) == "" {
		return Result{}, errors.New("mcp tool name is required")
	}

	t.mu.RLock()
	snapshot, ok := t.snapshots[in.SnapshotDigest]
	t.mu.RUnlock()
	if !ok {
		return Result{}, fmt.Errorf("%w: %s", ErrSnapshotUnknown, in.SnapshotDigest)
	}
	if snapshot.Digest != in.SnapshotDigest {
		return Result{}, ErrSnapshotMismatch
	}
	if t.changes.Load() != snapshot.Generation {
		return Result{}, fmt.Errorf("%w: snapshot=%s", ErrSnapshotStale, snapshot.Digest)
	}
	if !snapshotHasTool(snapshot, in.Tool) {
		return Result{}, fmt.Errorf("%w: %s", ErrToolNotInSnapshot, in.Tool)
	}

	args := in.Arguments
	if args == nil {
		args = map[string]any{}
	}
	res, err := t.session.CallTool(ctx, &mcp.CallToolParams{
		Name:      string(in.Tool),
		Arguments: args,
	})
	if err != nil {
		return Result{}, fmt.Errorf("call mcp tool %s: %w", in.Tool, err)
	}
	return normalizeResult(in.Tool, snapshot.Digest, res)
}

func snapshotHasTool(snapshot CapabilitySnapshot, name runtimesdk.ToolName) bool {
	i := sort.Search(len(snapshot.Tools), func(i int) bool { return snapshot.Tools[i].Name >= name })
	return i < len(snapshot.Tools) && snapshot.Tools[i].Name == name
}

func buildSnapshot(endpoint, protocolVersion string, server ServerIdentity, tools []*mcp.Tool) (CapabilitySnapshot, error) {
	if strings.TrimSpace(endpoint) == "" {
		return CapabilitySnapshot{}, errors.New("mcp endpoint is required")
	}
	if strings.TrimSpace(protocolVersion) == "" {
		return CapabilitySnapshot{}, errors.New("negotiated mcp protocol version is required")
	}

	capabilities := make([]ToolCapability, 0, len(tools))
	seen := make(map[runtimesdk.ToolName]struct{}, len(tools))
	for _, tool := range tools {
		if tool == nil {
			return CapabilitySnapshot{}, errors.New("mcp tool definition is nil")
		}
		name := runtimesdk.ToolName(strings.TrimSpace(tool.Name))
		if name == "" {
			return CapabilitySnapshot{}, errors.New("mcp tool name is required")
		}
		if _, exists := seen[name]; exists {
			return CapabilitySnapshot{}, fmt.Errorf("duplicate mcp tool %q", name)
		}
		seen[name] = struct{}{}
		if tool.InputSchema == nil {
			return CapabilitySnapshot{}, fmt.Errorf("mcp tool %q has no input schema", name)
		}
		input, err := canonicalJSON(tool.InputSchema)
		if err != nil {
			return CapabilitySnapshot{}, fmt.Errorf("canonicalize input schema for %q: %w", name, err)
		}
		var output json.RawMessage
		if tool.OutputSchema != nil {
			output, err = canonicalJSON(tool.OutputSchema)
			if err != nil {
				return CapabilitySnapshot{}, fmt.Errorf("canonicalize output schema for %q: %w", name, err)
			}
		}
		declaredReadOnly := false
		if tool.Annotations != nil {
			declaredReadOnly = tool.Annotations.ReadOnlyHint
		}
		capabilities = append(capabilities, ToolCapability{
			Name:             name,
			Title:            tool.Title,
			Description:      tool.Description,
			InputSchema:      input,
			OutputSchema:     output,
			DeclaredReadOnly: declaredReadOnly,
		})
	}
	sort.Slice(capabilities, func(i, j int) bool { return capabilities[i].Name < capabilities[j].Name })

	snapshot := CapabilitySnapshot{
		Endpoint:        endpoint,
		ProtocolVersion: protocolVersion,
		Server:          server,
		Tools:           capabilities,
	}
	digest, err := snapshotDigest(snapshot)
	if err != nil {
		return CapabilitySnapshot{}, err
	}
	snapshot.Digest = digest
	return snapshot, nil
}

func snapshotDigest(snapshot CapabilitySnapshot) (string, error) {
	payload := struct {
		Endpoint        string           `json:"endpoint"`
		ProtocolVersion string           `json:"protocol_version"`
		Server          ServerIdentity   `json:"server"`
		Tools           []ToolCapability `json:"tools"`
	}{
		Endpoint:        snapshot.Endpoint,
		ProtocolVersion: snapshot.ProtocolVersion,
		Server:          snapshot.Server,
		Tools:           snapshot.Tools,
	}
	b, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("marshal mcp capability snapshot: %w", err)
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:]), nil
}

func canonicalJSON(v any) (json.RawMessage, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	var normalized any
	if err := dec.Decode(&normalized); err != nil {
		return nil, err
	}
	return json.Marshal(normalized)
}

func normalizeResult(tool runtimesdk.ToolName, digest string, res *mcp.CallToolResult) (Result, error) {
	if res == nil {
		return Result{}, errors.New("mcp returned a nil tool result")
	}
	out := Result{
		Tool:           tool,
		SnapshotDigest: digest,
		IsError:        res.IsError,
		NeedsInput:     res.NeedsInput(),
		RequestState:   res.RequestState,
		Content:        make([]json.RawMessage, 0, len(res.Content)),
	}
	for i, content := range res.Content {
		if content == nil {
			return Result{}, fmt.Errorf("mcp result content %d is nil", i)
		}
		b, err := json.Marshal(content)
		if err != nil {
			return Result{}, fmt.Errorf("marshal mcp result content %d: %w", i, err)
		}
		out.Content = append(out.Content, b)
	}
	if res.StructuredContent != nil {
		b, err := canonicalJSON(res.StructuredContent)
		if err != nil {
			return Result{}, fmt.Errorf("normalize mcp structured content: %w", err)
		}
		out.StructuredContent = b
	}
	if res.InputRequests != nil {
		b, err := json.Marshal(res.InputRequests)
		if err != nil {
			return Result{}, fmt.Errorf("normalize mcp input requests: %w", err)
		}
		out.InputRequests = b
	}
	return out, nil
}
