package sdk

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

type ExecutionBackend interface {
	Name() string
	Start(context.Context, RunRequest) (RunHandle, error)
	Inspect(context.Context, RunID) (RunHandle, error)
	Signal(context.Context, RunID, string, []byte) error
	Cancel(context.Context, RunID, string) error
}

type StartRequest struct {
	RunID          RunID
	ConversationID ConversationID
	Input          string
}

type Runtime struct {
	Agent   AgentSpec
	Tools   ToolCatalog
	Backend ExecutionBackend
}

func (r Runtime) validateBackend() error {
	if r.Backend == nil {
		return errors.New("execution backend is required")
	}
	if strings.TrimSpace(r.Backend.Name()) == "" {
		return errors.New("execution backend name is required")
	}
	return nil
}

func (r Runtime) Bind(req StartRequest) (RunRequest, string, error) {
	if err := r.Agent.Validate(); err != nil {
		return RunRequest{}, "", fmt.Errorf("agent: %w", err)
	}
	if strings.TrimSpace(string(req.RunID)) == "" {
		return RunRequest{}, "", errors.New("run id is required")
	}
	if strings.TrimSpace(string(req.ConversationID)) == "" {
		return RunRequest{}, "", errors.New("conversation id is required")
	}
	if strings.TrimSpace(req.Input) == "" {
		return RunRequest{}, "", errors.New("run input is required")
	}

	tools, err := r.Tools.Resolve(r.Agent.RequiredTools)
	if err != nil {
		return RunRequest{}, "", err
	}
	for _, tool := range tools {
		if tool.Protocol == ToolProtocolMCP && strings.TrimSpace(tool.SnapshotDigest) == "" {
			return RunRequest{}, "", fmt.Errorf("mcp tool %q is not bound to a capability snapshot", tool.Name)
		}
	}

	run := RunRequest{
		ID: req.RunID,
		Conversation: ConversationRef{
			ID:          req.ConversationID,
			AgentID:     r.Agent.ID,
			WorkspaceID: r.Agent.Workspace.ID,
		},
		Workspace: r.Agent.Workspace,
		Input:     req.Input,
		Tools:     tools,
		PolicyRef: r.Agent.PolicyRef,
	}
	fingerprint, err := run.Fingerprint()
	if err != nil {
		return RunRequest{}, "", fmt.Errorf("bind run: %w", err)
	}
	return run, fingerprint, nil
}

func (r Runtime) Start(ctx context.Context, req StartRequest) (RunHandle, error) {
	if err := r.validateBackend(); err != nil {
		return RunHandle{}, err
	}
	run, fingerprint, err := r.Bind(req)
	if err != nil {
		return RunHandle{}, err
	}
	handle, err := r.Backend.Start(ctx, run)
	if err != nil {
		return RunHandle{}, fmt.Errorf("start %s backend: %w", r.Backend.Name(), err)
	}
	if err := validateHandle(handle, run.ID); err != nil {
		return RunHandle{}, err
	}
	if handle.Fingerprint != fingerprint {
		return RunHandle{}, errors.New("backend returned mismatched run fingerprint")
	}
	return handle, nil
}

func (r Runtime) Inspect(ctx context.Context, id RunID) (RunHandle, error) {
	if err := r.validateBackend(); err != nil {
		return RunHandle{}, err
	}
	if strings.TrimSpace(string(id)) == "" {
		return RunHandle{}, errors.New("run id is required")
	}
	handle, err := r.Backend.Inspect(ctx, id)
	if err != nil {
		return handle, fmt.Errorf("inspect %s backend: %w", r.Backend.Name(), err)
	}
	if err := validateHandle(handle, id); err != nil {
		return RunHandle{}, err
	}
	return handle, nil
}

func (r Runtime) Signal(ctx context.Context, id RunID, name string, payload []byte) error {
	if err := r.validateBackend(); err != nil {
		return err
	}
	if strings.TrimSpace(string(id)) == "" {
		return errors.New("run id is required")
	}
	if strings.TrimSpace(name) == "" {
		return errors.New("signal name is required")
	}
	if err := r.Backend.Signal(ctx, id, name, payload); err != nil {
		return fmt.Errorf("signal %s backend: %w", r.Backend.Name(), err)
	}
	return nil
}

func (r Runtime) Cancel(ctx context.Context, id RunID, reason string) error {
	if err := r.validateBackend(); err != nil {
		return err
	}
	if strings.TrimSpace(string(id)) == "" {
		return errors.New("run id is required")
	}
	if strings.TrimSpace(reason) == "" {
		return errors.New("cancellation reason is required")
	}
	if err := r.Backend.Cancel(ctx, id, reason); err != nil {
		return fmt.Errorf("cancel %s backend: %w", r.Backend.Name(), err)
	}
	return nil
}

func validateHandle(handle RunHandle, expectedID RunID) error {
	if handle.ID != expectedID {
		return errors.New("backend returned mismatched run id")
	}
	if strings.TrimSpace(handle.Backend) == "" {
		return errors.New("backend returned empty backend name")
	}
	if strings.TrimSpace(handle.Fingerprint) == "" {
		return errors.New("backend returned empty run fingerprint")
	}
	if strings.TrimSpace(string(handle.State)) == "" {
		return errors.New("backend returned empty run state")
	}
	return nil
}
