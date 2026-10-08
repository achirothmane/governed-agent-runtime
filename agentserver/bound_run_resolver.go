package agentserver

import (
	"context"
	"errors"
	"fmt"

	runtimesdk "github.com/achirothmane/governed-agent-runtime/sdk"
)

type BoundRunResolver struct {
	Provider RuntimeProvider
	Store    Store
}

func (r BoundRunResolver) Resolve(ctx context.Context, runID runtimesdk.RunID) (runtimesdk.RunRequest, string, error) {
	if r.Provider == nil || r.Store == nil {
		return runtimesdk.RunRequest{}, "", errors.New("bound run resolver is not configured")
	}
	record, err := r.Store.GetRun(ctx, runID)
	if err != nil {
		return runtimesdk.RunRequest{}, "", err
	}
	runtime, err := r.Provider.Runtime(ctx, record.AgentID)
	if err != nil {
		return runtimesdk.RunRequest{}, "", err
	}
	bound, fingerprint, err := runtime.Bind(runtimesdk.StartRequest{
		RunID:          record.ID,
		ConversationID: record.ConversationID,
		Input:          record.Input,
	})
	if err != nil {
		return runtimesdk.RunRequest{}, "", err
	}
	if fingerprint != record.Handle.Fingerprint {
		return runtimesdk.RunRequest{}, "", fmt.Errorf("%w: current runtime fingerprint differs from stored run", ErrConflict)
	}
	return bound, runtime.Agent.Mission, nil
}
