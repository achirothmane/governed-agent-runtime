package temporaltools

import (
	"context"
	"errors"
	"fmt"

	"go.temporal.io/sdk/temporal"

	"github.com/achirothmane/governed-agent-runtime/mcptransport"
)

type Activity struct {
	Store    InvocationStore
	Invoker  ToolInvoker
	Evidence EvidenceSink
}

type ActivityResult struct {
	InvocationID string              `json:"invocation_id"`
	Result       mcptransport.Result `json:"result"`
	ResultDigest string              `json:"result_digest"`
	Reused       bool                `json:"reused"`
}

func (a Activity) Execute(ctx context.Context, ref InvocationRef) (ActivityResult, error) {
	if a.Store == nil {
		return ActivityResult{}, errors.New("tool invocation store is required")
	}
	if a.Invoker == nil {
		return ActivityResult{}, errors.New("tool invoker is required")
	}
	if err := ref.Validate(); err != nil {
		return ActivityResult{}, nonRetryable("INVALID_TOOL_INVOCATION", err)
	}

	record, err := a.Store.Get(ctx, ref.InvocationID)
	if err != nil {
		return ActivityResult{}, fmt.Errorf("load invocation: %w", err)
	}
	if !SameBinding(record.Ref, ref) {
		return ActivityResult{}, nonRetryable("INVOCATION_BINDING_CONFLICT", ErrInvocationConflict)
	}
	if record.State == InvocationComplete {
		if record.Result == nil || record.ResultDigest == "" {
			return ActivityResult{}, nonRetryable("INVALID_STORED_RESULT", errors.New("completed invocation has no stored result"))
		}
		if a.Evidence != nil {
			if err := a.Evidence.Returned(ctx, ref, *record.Result, record.ResultDigest, true); err != nil {
				return ActivityResult{}, fmt.Errorf("record reused tool result evidence: %w", err)
			}
		}
		return ActivityResult{
			InvocationID: ref.InvocationID,
			Result:       *record.Result,
			ResultDigest: record.ResultDigest,
			Reused:       true,
		}, nil
	}

	digest, err := ArgumentsDigest(record.Arguments)
	if err != nil {
		return ActivityResult{}, nonRetryable("INVALID_STORED_ARGUMENTS", fmt.Errorf("digest stored arguments: %w", err))
	}
	if digest != ref.ArgumentsDigest {
		return ActivityResult{}, nonRetryable("INVOCATION_BINDING_CONFLICT", fmt.Errorf("%w: arguments digest mismatch", ErrInvocationConflict))
	}

	if a.Evidence != nil {
		if err := a.Evidence.Called(ctx, ref); err != nil {
			return ActivityResult{}, fmt.Errorf("record tool.called evidence: %w", err)
		}
	}

	result, err := a.Invoker.Invoke(ctx, ref.Tool, record.Arguments)
	if err != nil {
		_ = a.Store.Fail(ctx, ref.InvocationID, err.Error())
		if a.Evidence != nil {
			if evidenceErr := a.Evidence.Failed(ctx, ref, err.Error()); evidenceErr != nil {
				return ActivityResult{}, fmt.Errorf("invoke tool: %v; record tool.failed evidence: %w", err, evidenceErr)
			}
		}
		if errors.Is(err, mcptransport.ErrSnapshotMismatch) ||
			errors.Is(err, mcptransport.ErrSnapshotStale) ||
			errors.Is(err, mcptransport.ErrSnapshotUnknown) ||
			errors.Is(err, mcptransport.ErrToolNotInSnapshot) {
			return ActivityResult{}, nonRetryable("CAPABILITY_CHANGED", err)
		}
		return ActivityResult{}, err
	}

	resultDigest, err := ResultDigest(result)
	if err != nil {
		_ = a.Store.Fail(ctx, ref.InvocationID, err.Error())
		return ActivityResult{}, nonRetryable("INVALID_TOOL_RESULT", err)
	}
	stored, err := a.Store.Complete(ctx, ref.InvocationID, result, resultDigest)
	if err != nil {
		return ActivityResult{}, err
	}
	if stored.Result == nil || stored.ResultDigest != resultDigest {
		return ActivityResult{}, nonRetryable("INVOCATION_BINDING_CONFLICT", errors.New("stored invocation result does not match activity result"))
	}
	if a.Evidence != nil {
		if err := a.Evidence.Returned(ctx, ref, *stored.Result, stored.ResultDigest, false); err != nil {
			return ActivityResult{}, fmt.Errorf("record tool.returned evidence: %w", err)
		}
	}
	return ActivityResult{
		InvocationID: ref.InvocationID,
		Result:       *stored.Result,
		ResultDigest: stored.ResultDigest,
	}, nil
}

func nonRetryable(kind string, err error) error {
	return temporal.NewNonRetryableApplicationError(err.Error(), kind, err)
}
