package temporalagent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/achirothmane/governed-agent-runtime/agentloop"
	runtimesdk "github.com/achirothmane/governed-agent-runtime/sdk"
	"github.com/achirothmane/governed-agent-runtime/temporaltools"
)

var (
	ErrExecutionNotFound = errors.New("agent execution not found")
	ErrExecutionConflict = errors.New("agent execution binding conflict")
	ErrDecisionNotFound  = errors.New("reasoning decision not found")
	ErrDecisionConflict  = errors.New("reasoning decision binding conflict")
)

type ExecutionRef struct {
	RunID          runtimesdk.RunID          `json:"run_id"`
	ConversationID runtimesdk.ConversationID `json:"conversation_id"`
	RunFingerprint string                    `json:"run_fingerprint"`
	MissionDigest  string                    `json:"mission_digest"`
	MaxSteps       int                       `json:"max_steps"`
}

func (r ExecutionRef) Validate() error {
	if strings.TrimSpace(string(r.RunID)) == "" || strings.TrimSpace(string(r.ConversationID)) == "" {
		return errors.New("run id and conversation id are required")
	}
	if len(r.RunFingerprint) != 64 || len(r.MissionDigest) != 64 {
		return errors.New("run fingerprint and mission digest must be SHA-256 hex")
	}
	if r.MaxSteps < 1 || r.MaxSteps > 64 {
		return errors.New("agent max steps must be between 1 and 64")
	}
	return nil
}

type ObservationRef struct {
	Step         int    `json:"step"`
	InvocationID string `json:"invocation_id"`
	ResultDigest string `json:"result_digest"`
}

func (r ObservationRef) Validate() error {
	if r.Step < 1 || strings.TrimSpace(r.InvocationID) == "" || len(r.ResultDigest) != 64 {
		return errors.New("invalid observation reference")
	}
	return nil
}

type DecisionRecord struct {
	RunID         runtimesdk.RunID   `json:"run_id"`
	Step          int                `json:"step"`
	Decision      agentloop.Decision `json:"decision"`
	DecisionDigest string            `json:"decision_digest"`
}

func (r DecisionRecord) Validate() error {
	if strings.TrimSpace(string(r.RunID)) == "" || r.Step < 1 {
		return errors.New("decision run id and positive step are required")
	}
	if err := r.Decision.Validate(); err != nil {
		return err
	}
	digest, err := DecisionDigest(r.Decision)
	if err != nil {
		return err
	}
	if r.DecisionDigest != digest {
		return ErrDecisionConflict
	}
	return nil
}

type ExecutionStore interface {
	PrepareExecution(context.Context, ExecutionRef) (ExecutionRef, bool, error)
	GetExecution(context.Context, runtimesdk.RunID) (ExecutionRef, error)
}

type StepStore interface {
	PutDecision(context.Context, DecisionRecord) (DecisionRecord, bool, error)
	GetDecision(context.Context, runtimesdk.RunID, int) (DecisionRecord, error)
}

type RunResolver interface {
	Resolve(context.Context, runtimesdk.RunID) (runtimesdk.RunRequest, string, error)
}

func MissionDigest(mission string) string {
	sum := sha256.Sum256([]byte(mission))
	return hex.EncodeToString(sum[:])
}

func DecisionDigest(decision agentloop.Decision) (string, error) {
	payload, err := json.Marshal(decision)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:]), nil
}

func executionRef(run runtimesdk.RunRequest, mission string, maxSteps int) (ExecutionRef, error) {
	if err := run.Validate(); err != nil {
		return ExecutionRef{}, err
	}
	if strings.TrimSpace(mission) == "" {
		return ExecutionRef{}, errors.New("agent mission is required")
	}
	fingerprint, err := run.Fingerprint()
	if err != nil {
		return ExecutionRef{}, err
	}
	ref := ExecutionRef{
		RunID:          run.ID,
		ConversationID: run.Conversation.ID,
		RunFingerprint: fingerprint,
		MissionDigest:  MissionDigest(mission),
		MaxSteps:       maxSteps,
	}
	if err := ref.Validate(); err != nil {
		return ExecutionRef{}, fmt.Errorf("execution ref: %w", err)
	}
	return ref, nil
}

func sameExecution(a, b ExecutionRef) bool {
	return a == b
}

func toolRef(run runtimesdk.RunRequest, step int, decision agentloop.Decision) (temporaltools.InvocationRef, map[string]any, error) {
	var tool runtimesdk.ToolDescriptor
	found := false
	for _, candidate := range run.Tools {
		if candidate.Name == decision.Tool {
			tool = candidate
			found = true
			break
		}
	}
	if !found {
		return temporaltools.InvocationRef{}, nil, fmt.Errorf("%w: %s", agentloop.ErrUnboundTool, decision.Tool)
	}
	if !tool.ReadOnly {
		return temporaltools.InvocationRef{}, nil, fmt.Errorf("%w: %s", agentloop.ErrMutatingTool, decision.Tool)
	}
	id, err := agentloop.InvocationID(run.ID, step, decision)
	if err != nil {
		return temporaltools.InvocationRef{}, nil, err
	}
	args := decision.Arguments
	if args == nil {
		args = map[string]any{}
	}
	digest, err := temporaltools.ArgumentsDigest(args)
	if err != nil {
		return temporaltools.InvocationRef{}, nil, err
	}
	ref := temporaltools.InvocationRef{
		InvocationID:    id,
		RunID:           run.ID,
		ConversationID:  run.Conversation.ID,
		Tool:            tool,
		ArgumentsDigest: digest,
	}
	if err := ref.Validate(); err != nil {
		return temporaltools.InvocationRef{}, nil, err
	}
	return ref, args, nil
}
