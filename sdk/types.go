package sdk

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

type AgentID string
type ToolName string
type WorkspaceID string
type ConversationID string
type RunID string

type ToolProtocol string

const (
	ToolProtocolNative ToolProtocol = "native"
	ToolProtocolMCP    ToolProtocol = "mcp"
)

type WorkspaceKind string

const (
	WorkspaceLocal     WorkspaceKind = "local"
	WorkspaceEphemeral WorkspaceKind = "ephemeral"
	WorkspaceRemote    WorkspaceKind = "remote"
)

type ToolDescriptor struct {
	Name     ToolName     `json:"name"`
	Protocol ToolProtocol `json:"protocol"`
	Endpoint string       `json:"endpoint,omitempty"`
	ReadOnly bool         `json:"read_only"`
}

func (t ToolDescriptor) Validate() error {
	if strings.TrimSpace(string(t.Name)) == "" {
		return errors.New("tool name is required")
	}
	switch t.Protocol {
	case ToolProtocolNative:
		if strings.TrimSpace(t.Endpoint) != "" {
			return errors.New("native tool must not declare an endpoint")
		}
	case ToolProtocolMCP:
		if strings.TrimSpace(t.Endpoint) == "" {
			return errors.New("mcp tool endpoint is required")
		}
	default:
		return fmt.Errorf("unsupported tool protocol %q", t.Protocol)
	}
	return nil
}

type WorkspaceSpec struct {
	ID   WorkspaceID   `json:"id"`
	Kind WorkspaceKind `json:"kind"`
	Root string        `json:"root,omitempty"`
}

func (w WorkspaceSpec) Validate() error {
	if strings.TrimSpace(string(w.ID)) == "" {
		return errors.New("workspace id is required")
	}
	switch w.Kind {
	case WorkspaceLocal:
		if strings.TrimSpace(w.Root) == "" {
			return errors.New("local workspace root is required")
		}
	case WorkspaceEphemeral, WorkspaceRemote:
	default:
		return fmt.Errorf("unsupported workspace kind %q", w.Kind)
	}
	return nil
}

type AgentSpec struct {
	ID            AgentID       `json:"id"`
	Mission       string        `json:"mission"`
	RequiredTools []ToolName    `json:"required_tools"`
	Workspace     WorkspaceSpec `json:"workspace"`
	PolicyRef     string        `json:"policy_ref,omitempty"`
}

func (a AgentSpec) Validate() error {
	if strings.TrimSpace(string(a.ID)) == "" {
		return errors.New("agent id is required")
	}
	if strings.TrimSpace(a.Mission) == "" {
		return errors.New("agent mission is required")
	}
	if err := a.Workspace.Validate(); err != nil {
		return fmt.Errorf("workspace: %w", err)
	}
	seen := make(map[ToolName]struct{}, len(a.RequiredTools))
	for _, name := range a.RequiredTools {
		if strings.TrimSpace(string(name)) == "" {
			return errors.New("required tool name cannot be empty")
		}
		if _, ok := seen[name]; ok {
			return fmt.Errorf("duplicate required tool %q", name)
		}
		seen[name] = struct{}{}
	}
	return nil
}

type ConversationRef struct {
	ID          ConversationID `json:"id"`
	AgentID     AgentID        `json:"agent_id"`
	WorkspaceID WorkspaceID    `json:"workspace_id"`
}

func (c ConversationRef) Validate() error {
	if strings.TrimSpace(string(c.ID)) == "" {
		return errors.New("conversation id is required")
	}
	if strings.TrimSpace(string(c.AgentID)) == "" {
		return errors.New("conversation agent id is required")
	}
	if strings.TrimSpace(string(c.WorkspaceID)) == "" {
		return errors.New("conversation workspace id is required")
	}
	return nil
}

type RunRequest struct {
	ID           RunID            `json:"id"`
	Conversation ConversationRef  `json:"conversation"`
	Workspace    WorkspaceSpec    `json:"workspace"`
	Input        string           `json:"input"`
	Tools        []ToolDescriptor `json:"tools"`
	PolicyRef    string           `json:"policy_ref,omitempty"`
}

func (r RunRequest) Validate() error {
	if strings.TrimSpace(string(r.ID)) == "" {
		return errors.New("run id is required")
	}
	if err := r.Conversation.Validate(); err != nil {
		return fmt.Errorf("conversation: %w", err)
	}
	if err := r.Workspace.Validate(); err != nil {
		return fmt.Errorf("workspace: %w", err)
	}
	if r.Conversation.WorkspaceID != r.Workspace.ID {
		return errors.New("conversation workspace does not match run workspace")
	}
	if strings.TrimSpace(r.Input) == "" {
		return errors.New("run input is required")
	}
	seen := make(map[ToolName]struct{}, len(r.Tools))
	for _, tool := range r.Tools {
		if err := tool.Validate(); err != nil {
			return fmt.Errorf("tool %q: %w", tool.Name, err)
		}
		if _, ok := seen[tool.Name]; ok {
			return fmt.Errorf("duplicate run tool %q", tool.Name)
		}
		seen[tool.Name] = struct{}{}
	}
	return nil
}

func (r RunRequest) Fingerprint() (string, error) {
	if err := r.Validate(); err != nil {
		return "", err
	}
	payload, err := json.Marshal(r)
	if err != nil {
		return "", fmt.Errorf("marshal run request: %w", err)
	}
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:]), nil
}

type RunState string

const (
	RunQueued    RunState = "QUEUED"
	RunRunning   RunState = "RUNNING"
	RunWaiting   RunState = "WAITING"
	RunSucceeded RunState = "SUCCEEDED"
	RunFailed    RunState = "FAILED"
	RunCanceled  RunState = "CANCELED"
	RunUnknown   RunState = "UNKNOWN"
)

type RunHandle struct {
	ID          RunID    `json:"id"`
	Backend     string   `json:"backend"`
	ExternalID  string   `json:"external_id,omitempty"`
	Fingerprint string   `json:"fingerprint"`
	State       RunState `json:"state"`
}

type EventType string

const (
	EventRunStarted   EventType = "run.started"
	EventRunWaiting   EventType = "run.waiting"
	EventToolCalled   EventType = "tool.called"
	EventToolReturned EventType = "tool.returned"
	EventRunCompleted EventType = "run.completed"
	EventRunFailed    EventType = "run.failed"
)

type EventEnvelope struct {
	Sequence       uint64          `json:"sequence"`
	Type           EventType       `json:"type"`
	RunID          RunID           `json:"run_id"`
	ConversationID ConversationID  `json:"conversation_id"`
	OccurredAt     time.Time       `json:"occurred_at"`
	Payload        json.RawMessage `json:"payload,omitempty"`
}

func (e EventEnvelope) Validate() error {
	if e.Sequence == 0 {
		return errors.New("event sequence must be positive")
	}
	if strings.TrimSpace(string(e.Type)) == "" {
		return errors.New("event type is required")
	}
	if strings.TrimSpace(string(e.RunID)) == "" {
		return errors.New("event run id is required")
	}
	if strings.TrimSpace(string(e.ConversationID)) == "" {
		return errors.New("event conversation id is required")
	}
	if e.OccurredAt.IsZero() {
		return errors.New("event occurred_at is required")
	}
	return nil
}
