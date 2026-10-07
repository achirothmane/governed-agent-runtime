package temporaltools

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/achirothmane/governed-agent-runtime/mcptransport"
	runtimesdk "github.com/achirothmane/governed-agent-runtime/sdk"
)

type InvocationState string

const (
	InvocationPrepared InvocationState = "PREPARED"
	InvocationFailed   InvocationState = "FAILED"
	InvocationComplete InvocationState = "COMPLETE"
)

var (
	ErrInvocationNotFound = errors.New("tool invocation not found")
	ErrInvocationConflict = errors.New("tool invocation conflicts with stored binding")
)

type InvocationRef struct {
	InvocationID   string                    `json:"invocation_id"`
	RunID          runtimesdk.RunID          `json:"run_id"`
	ConversationID runtimesdk.ConversationID `json:"conversation_id"`
	Tool           runtimesdk.ToolDescriptor `json:"tool"`
	ArgumentsDigest string                   `json:"arguments_digest"`
}

func (r InvocationRef) Validate() error {
	if strings.TrimSpace(r.InvocationID) == "" {
		return errors.New("invocation id is required")
	}
	if len(r.InvocationID) > 128 {
		return errors.New("invocation id exceeds 128 bytes")
	}
	if strings.TrimSpace(string(r.RunID)) == "" || strings.TrimSpace(string(r.ConversationID)) == "" {
		return errors.New("run id and conversation id are required")
	}
	if err := r.Tool.Validate(); err != nil {
		return fmt.Errorf("tool: %w", err)
	}
	if r.Tool.Protocol != runtimesdk.ToolProtocolMCP {
		return errors.New("A5.1 supports MCP tools only")
	}
	if !r.Tool.ReadOnly {
		return errors.New("A5.1 supports server-admitted read-only tools only")
	}
	if len(strings.TrimSpace(r.Tool.SnapshotDigest)) != 64 {
		return errors.New("tool snapshot digest must be SHA-256 hex")
	}
	if len(r.ArgumentsDigest) != 64 {
		return errors.New("arguments digest must be SHA-256 hex")
	}
	return nil
}

type Invocation struct {
	Ref          InvocationRef       `json:"ref"`
	Arguments    map[string]any      `json:"arguments,omitempty"`
	State        InvocationState     `json:"state"`
	Result       *mcptransport.Result `json:"result,omitempty"`
	ResultDigest string              `json:"result_digest,omitempty"`
	LastError    string              `json:"last_error,omitempty"`
}

type InvocationStore interface {
	Prepare(context.Context, InvocationRef, map[string]any) (Invocation, bool, error)
	Get(context.Context, string) (Invocation, error)
	Complete(context.Context, string, mcptransport.Result, string) (Invocation, error)
	Fail(context.Context, string, string) error
}

type ToolInvoker interface {
	Invoke(context.Context, runtimesdk.ToolDescriptor, map[string]any) (mcptransport.Result, error)
}

func ArgumentsDigest(arguments map[string]any) (string, error) {
	if arguments == nil {
		arguments = map[string]any{}
	}
	payload, err := json.Marshal(arguments)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:]), nil
}

func ResultDigest(result mcptransport.Result) (string, error) {
	payload, err := json.Marshal(result)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:]), nil
}

func SameBinding(a, b InvocationRef) bool {
	return a.InvocationID == b.InvocationID &&
		a.RunID == b.RunID &&
		a.ConversationID == b.ConversationID &&
		a.Tool.Name == b.Tool.Name &&
		a.Tool.Protocol == b.Tool.Protocol &&
		a.Tool.Endpoint == b.Tool.Endpoint &&
		a.Tool.ReadOnly == b.Tool.ReadOnly &&
		a.Tool.SnapshotDigest == b.Tool.SnapshotDigest &&
		a.ArgumentsDigest == b.ArgumentsDigest
}
