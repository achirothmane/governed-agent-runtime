package agentserver

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/achirothmane/governed-agent-runtime/mcptransport"
	runtimesdk "github.com/achirothmane/governed-agent-runtime/sdk"
)

var (
	ErrAgentNotFound        = errors.New("agent not found")
	ErrConversationNotFound = errors.New("conversation not found")
	ErrRunNotFound          = errors.New("run not found")
	ErrConflict             = errors.New("resource conflicts with existing binding")
)

// RuntimeProvider is the server-owned authority for agent/runtime construction.
// Implementations may discover MCP capabilities before returning Runtime. Clients
// never supply a runtime, backend, or tool catalog directly to the HTTP service.
type RuntimeProvider interface {
	Agent(context.Context, runtimesdk.AgentID) (runtimesdk.AgentSpec, error)
	Runtime(context.Context, runtimesdk.AgentID) (runtimesdk.Runtime, error)
}

// ToolInvoker is the server-owned invocation boundary for run-scoped tools.
// A5 uses it only for explicitly admitted read-only MCP tools.
type ToolInvoker interface {
	Invoke(context.Context, runtimesdk.ToolDescriptor, map[string]any) (mcptransport.Result, error)
}

// DurableToolInvoker executes a run-scoped tool through a durable substrate.
// A5.1 implementations persist raw arguments outside Temporal history and use
// invocationID as the idempotent execution identity.
type DurableToolInvoker interface {
	Execute(
		context.Context,
		runtimesdk.RunID,
		runtimesdk.ConversationID,
		string,
		runtimesdk.ToolDescriptor,
		map[string]any,
	) (mcptransport.Result, error)
}

// StaticProvider is useful for composition, tests, and small deployments. Dynamic
// deployments can implement RuntimeProvider and perform fresh MCP discovery before
// constructing each Runtime.
type StaticProvider struct {
	Runtimes map[runtimesdk.AgentID]runtimesdk.Runtime
}

func (p StaticProvider) Agent(_ context.Context, id runtimesdk.AgentID) (runtimesdk.AgentSpec, error) {
	runtime, ok := p.Runtimes[id]
	if !ok {
		return runtimesdk.AgentSpec{}, fmt.Errorf("%w: %s", ErrAgentNotFound, id)
	}
	if err := runtime.Agent.Validate(); err != nil {
		return runtimesdk.AgentSpec{}, fmt.Errorf("agent %s: %w", id, err)
	}
	if runtime.Agent.ID != id {
		return runtimesdk.AgentSpec{}, fmt.Errorf("%w: provider key=%s runtime agent=%s", ErrConflict, id, runtime.Agent.ID)
	}
	return runtime.Agent, nil
}

func (p StaticProvider) Runtime(ctx context.Context, id runtimesdk.AgentID) (runtimesdk.Runtime, error) {
	runtime, ok := p.Runtimes[id]
	if !ok {
		return runtimesdk.Runtime{}, fmt.Errorf("%w: %s", ErrAgentNotFound, id)
	}
	if _, err := p.Agent(ctx, id); err != nil {
		return runtimesdk.Runtime{}, err
	}
	return runtime, nil
}

type Conversation struct {
	Ref       runtimesdk.ConversationRef `json:"ref"`
	CreatedAt time.Time                  `json:"created_at"`
}

func (c Conversation) Validate() error {
	if err := c.Ref.Validate(); err != nil {
		return err
	}
	if c.CreatedAt.IsZero() {
		return errors.New("conversation created_at is required")
	}
	return nil
}

type RunRecord struct {
	ID             runtimesdk.RunID          `json:"id"`
	ConversationID runtimesdk.ConversationID `json:"conversation_id"`
	AgentID        runtimesdk.AgentID        `json:"agent_id"`
	Input          string                    `json:"input"`
	InputDigest    string                    `json:"input_digest"`
	Handle         runtimesdk.RunHandle      `json:"handle"`
	CreatedAt      time.Time                 `json:"created_at"`
}

func (r RunRecord) Validate() error {
	if strings.TrimSpace(string(r.ID)) == "" {
		return errors.New("run id is required")
	}
	if strings.TrimSpace(string(r.ConversationID)) == "" {
		return errors.New("run conversation id is required")
	}
	if strings.TrimSpace(string(r.AgentID)) == "" {
		return errors.New("run agent id is required")
	}
	if strings.TrimSpace(r.Input) == "" {
		return errors.New("run input is required")
	}
	if strings.TrimSpace(r.InputDigest) == "" || r.InputDigest != InputDigest(r.Input) {
		return errors.New("run input digest does not match input")
	}
	if r.Handle.ID != r.ID {
		return errors.New("run handle id does not match run id")
	}
	if strings.TrimSpace(r.Handle.Backend) == "" || strings.TrimSpace(r.Handle.Fingerprint) == "" {
		return errors.New("run handle binding is incomplete")
	}
	switch r.Handle.State {
	case runtimesdk.RunQueued, runtimesdk.RunRunning, runtimesdk.RunWaiting,
		runtimesdk.RunSucceeded, runtimesdk.RunFailed, runtimesdk.RunCanceled, runtimesdk.RunUnknown:
	default:
		return fmt.Errorf("unsupported run state %q", r.Handle.State)
	}
	if r.CreatedAt.IsZero() {
		return errors.New("run created_at is required")
	}
	return nil
}

func sameRunBinding(a, b RunRecord) bool {
	return a.ID == b.ID &&
		a.ConversationID == b.ConversationID &&
		a.AgentID == b.AgentID &&
		a.InputDigest == b.InputDigest &&
		a.Handle.Backend == b.Handle.Backend &&
		a.Handle.ExternalID == b.Handle.ExternalID &&
		a.Handle.Fingerprint == b.Handle.Fingerprint
}

func NewRunRecord(conversation Conversation, input string, handle runtimesdk.RunHandle, at time.Time) (RunRecord, error) {
	if err := conversation.Validate(); err != nil {
		return RunRecord{}, fmt.Errorf("conversation: %w", err)
	}
	if strings.TrimSpace(input) == "" {
		return RunRecord{}, errors.New("run input is required")
	}
	if strings.TrimSpace(string(handle.ID)) == "" || strings.TrimSpace(handle.Fingerprint) == "" || strings.TrimSpace(handle.Backend) == "" {
		return RunRecord{}, errors.New("run handle is incomplete")
	}
	if at.IsZero() {
		return RunRecord{}, errors.New("run created_at is required")
	}
	sum := sha256.Sum256([]byte(input))
	record := RunRecord{
		ID:             handle.ID,
		ConversationID: conversation.Ref.ID,
		AgentID:        conversation.Ref.AgentID,
		Input:          input,
		InputDigest:    hex.EncodeToString(sum[:]),
		Handle:         handle,
		CreatedAt:      at,
	}
	if err := record.Validate(); err != nil {
		return RunRecord{}, err
	}
	return record, nil
}

func InputDigest(input string) string {
	sum := sha256.Sum256([]byte(input))
	return hex.EncodeToString(sum[:])
}

// Store owns server metadata and the replayable event stream. Durable execution
// remains the responsibility of ExecutionBackend (Temporal in A2).
type Store interface {
	PutConversation(context.Context, Conversation) (Conversation, bool, error)
	GetConversation(context.Context, runtimesdk.ConversationID) (Conversation, error)
	PutRun(context.Context, RunRecord) (RunRecord, bool, error)
	GetRun(context.Context, runtimesdk.RunID) (RunRecord, error)
	AppendEvent(context.Context, runtimesdk.EventEnvelope) (runtimesdk.EventEnvelope, error)
	ListEvents(context.Context, runtimesdk.ConversationID, uint64, int) ([]runtimesdk.EventEnvelope, error)
	Watch(context.Context, runtimesdk.ConversationID) (<-chan struct{}, func(), error)
}
