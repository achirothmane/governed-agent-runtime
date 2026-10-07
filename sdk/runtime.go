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

func (r Runtime) Start(ctx context.Context, req StartRequest) (RunHandle, error) {
	if err := r.Agent.Validate(); err != nil {
		return RunHandle{}, fmt.Errorf("agent: %w", err)
	}
	if r.Backend == nil {
		return RunHandle{}, errors.New("execution backend is required")
	}
	if strings.TrimSpace(r.Backend.Name()) == "" {
		return RunHandle{}, errors.New("execution backend name is required")
	}
	if strings.TrimSpace(string(req.RunID)) == "" {
		return RunHandle{}, errors.New("run id is required")
	}
	if strings.TrimSpace(string(req.ConversationID)) == "" {
		return RunHandle{}, errors.New("conversation id is required")
	}
	if strings.TrimSpace(req.Input) == "" {
		return RunHandle{}, errors.New("run input is required")
	}

	tools, err := r.Tools.Resolve(r.Agent.RequiredTools)
	if err != nil {
		return RunHandle{}, err
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
		return RunHandle{}, fmt.Errorf("bind run: %w", err)
	}

	handle, err := r.Backend.Start(ctx, run)
	if err != nil {
		return RunHandle{}, fmt.Errorf("start %s backend: %w", r.Backend.Name(), err)
	}
	if handle.ID != run.ID {
		return RunHandle{}, errors.New("backend returned mismatched run id")
	}
	if handle.Fingerprint != fingerprint {
		return RunHandle{}, errors.New("backend returned mismatched run fingerprint")
	}
	if strings.TrimSpace(handle.Backend) == "" {
		return RunHandle{}, errors.New("backend returned empty backend name")
	}
	return handle, nil
}
