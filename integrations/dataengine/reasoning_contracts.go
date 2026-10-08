package dataengine

import (
	"context"
	"fmt"

	"github.com/achirothmane/governed-agent-runtime/agentloop"
	runtimesdk "github.com/achirothmane/governed-agent-runtime/sdk"
)

var _ agentloop.ToolContractResolver = Provider{}

func (p Provider) ResolveToolContracts(ctx context.Context, run runtimesdk.RunRequest) ([]agentloop.ToolContract, error) {
	if err := run.Validate(); err != nil {
		return nil, fmt.Errorf("bound run: %w", err)
	}
	if len(run.Tools) != 1 {
		return nil, fmt.Errorf("%w: data engine agent expects exactly one bound tool", ErrToolRejected)
	}
	bound := run.Tools[0]
	if err := p.validateAdmittedTool(bound); err != nil {
		return nil, err
	}

	snapshot, err := p.discover(ctx)
	if err != nil {
		return nil, err
	}
	if snapshot.Digest != bound.SnapshotDigest {
		return nil, fmt.Errorf(
			"%w: bound=%s current=%s",
			ErrCapabilityChanged,
			bound.SnapshotDigest,
			snapshot.Digest,
		)
	}
	for _, capability := range snapshot.Tools {
		if capability.Name != bound.Name {
			continue
		}
		contracts := []agentloop.ToolContract{{
			Name:           capability.Name,
			Description:    capability.Description,
			InputSchema:    append([]byte(nil), capability.InputSchema...),
			SnapshotDigest: snapshot.Digest,
		}}
		if err := agentloop.ValidateToolContracts(run, contracts); err != nil {
			return nil, err
		}
		return contracts, nil
	}
	return nil, fmt.Errorf("%w: %s", ErrToolRejected, bound.Name)
}
